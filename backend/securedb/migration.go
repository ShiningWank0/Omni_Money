package securedb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sqlitePlaintextHeader = "SQLite format 3\x00"

// EnsureEncrypted migrates a valid plaintext SQLite database to SQLCipher.
// New or already-encrypted databases are left unchanged. The replacement is
// published with one same-directory rename only after logical and physical
// integrity checks succeed.
func EnsureEncrypted(ctx context.Context, path string, opener *Opener) error {
	if opener == nil || !opener.Encrypted() {
		return errors.New("encrypted opener is required")
	}
	header, err := readHeader(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(header) == 0 {
		return nil
	}
	if string(header) != sqlitePlaintextHeader {
		db, openErr := opener.Open(ctx, path, Writable)
		if openErr != nil {
			return fmt.Errorf("database is encrypted with another key or is corrupt: %w", openErr)
		}
		defer db.Close()
		return opener.CheckIntegrity(ctx, db)
	}
	return migratePlaintext(ctx, path, opener)
}

func readHeader(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- caller supplies the configured database path.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header := make([]byte, len(sqlitePlaintextHeader))
	n, err := io.ReadFull(file, header)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return header[:n], nil
	}
	return header, err
}

func migratePlaintext(ctx context.Context, path string, opener *Opener) (retErr error) {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	stageDir, err := os.MkdirTemp(dir, ".omni-cipher-migrate-")
	if err != nil {
		return fmt.Errorf("create private migration directory: %w", err)
	}
	if err := os.Chmod(stageDir, 0700); err != nil { // #nosec G302 -- this is a directory and must be traversable only by its owner.
		_ = os.RemoveAll(stageDir)
		return fmt.Errorf("secure migration directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(stageDir)
	}()
	stagePath := filepath.Join(stageDir, "encrypted.db")

	source, err := openPlain(path, Writable)
	if err != nil {
		return err
	}
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			_ = source.Close()
		}
	}()
	if err := source.PingContext(ctx); err != nil {
		return fmt.Errorf("open plaintext database: %w", err)
	}
	if err := requireSQLCipherRuntime(ctx, source); err != nil {
		return err
	}
	if err := expectSQLiteIntegrity(ctx, source); err != nil {
		return err
	}
	if _, err := source.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint plaintext database: %w", err)
	}

	var userVersion, applicationID int64
	if err := source.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return err
	}
	if err := source.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return err
	}

	key, err := opener.copyKey()
	if err != nil {
		return err
	}
	defer key.Destroy()
	if err := exportEncrypted(ctx, source, stagePath, &key, userVersion, applicationID); err != nil {
		return err
	}

	target, err := opener.Open(ctx, stagePath, Writable)
	if err != nil {
		return fmt.Errorf("open migrated database: %w", err)
	}
	if err := opener.CheckIntegrity(ctx, target); err != nil {
		_ = target.Close()
		return err
	}
	if err := compareLogicalDatabases(ctx, source, target); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	if err := syncFile(stagePath); err != nil {
		return err
	}
	if err := os.Chmod(stagePath, 0600); err != nil {
		return fmt.Errorf("secure migrated database: %w", err)
	}
	if err := source.Close(); err != nil {
		return err
	}
	sourceClosed = true
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove plaintext SQLite sidecar: %w", err)
		}
	}
	if err := os.Rename(stagePath, path); err != nil {
		return fmt.Errorf("publish encrypted database: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}

	verified, err := opener.Open(ctx, path, Writable)
	if err != nil {
		return fmt.Errorf("reopen migrated database: %w", err)
	}
	defer verified.Close()
	if err := opener.CheckIntegrity(ctx, verified); err != nil {
		return err
	}
	return RequireEncryptedHeader(path)
}

func requireSQLCipherRuntime(ctx context.Context, db databaseQueryer) error {
	var version string
	if err := db.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&version); err != nil {
		return ErrCipherUnavailable
	}
	if strings.TrimSpace(version) != RequiredSQLCipherVersion {
		return fmt.Errorf("%w: required %s", ErrCipherVersion, RequiredSQLCipherVersion)
	}
	return nil
}

func exportEncrypted(ctx context.Context, source *sql.DB, targetPath string, key *RawKey, userVersion, applicationID int64) error {
	conn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return exportEncryptedConnection(ctx, conn, targetPath, key, userVersion, applicationID)
}

func exportEncryptedConnection(ctx context.Context, conn *sql.Conn, targetPath string, key *RawKey, userVersion, applicationID int64) error {
	keySpec := rawKeySpec(key)
	_, attachErr := conn.ExecContext(ctx, "ATTACH DATABASE ? AS encrypted KEY ?", targetPath, string(keySpec))
	clear(keySpec)
	if attachErr != nil {
		return fmt.Errorf("attach encrypted migration target: %w", attachErr)
	}
	attached := true
	defer func() {
		if attached {
			_, _ = conn.ExecContext(context.Background(), "DETACH DATABASE encrypted")
		}
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA encrypted.cipher_memory_security = ON"); err != nil {
		return err
	}
	var cipherStatus string
	if err := conn.QueryRowContext(ctx, "PRAGMA encrypted.cipher_status").Scan(&cipherStatus); err != nil || strings.TrimSpace(cipherStatus) != "1" {
		return ErrCipherInactive
	}
	var exported any
	if err := conn.QueryRowContext(ctx, "SELECT sqlcipher_export('encrypted')").Scan(&exported); err != nil {
		return fmt.Errorf("export plaintext database into SQLCipher: %w", err)
	}
	for _, statement := range []string{
		"PRAGMA encrypted.user_version = " + strconv.FormatInt(userVersion, 10),
		"PRAGMA encrypted.application_id = " + strconv.FormatInt(applicationID, 10),
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA encrypted.cipher_integrity_check")
	if err != nil {
		return fmt.Errorf("verify attached migration target: %w", err)
	}
	if rows.Next() {
		var detail string
		_ = rows.Scan(&detail)
		_ = rows.Close()
		return fmt.Errorf("%w: %s", ErrCipherIntegrity, detail)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "DETACH DATABASE encrypted"); err != nil {
		return err
	}
	attached = false
	return nil
}

func compareLogicalDatabases(ctx context.Context, source, target *sql.DB) error {
	sourceFingerprint, err := logicalDatabaseFingerprint(ctx, source)
	if err != nil {
		return err
	}
	targetFingerprint, err := logicalDatabaseFingerprint(ctx, target)
	if err != nil {
		return err
	}
	if sourceFingerprint != targetFingerprint {
		return errors.New("migrated database logical contents differ")
	}
	return nil
}

func logicalDatabaseFingerprint(ctx context.Context, db databaseQueryer) ([32]byte, error) {
	hash := sha256.New()
	schemaDigest, schemaRows, err := queryDigest(ctx, db, "SELECT type, name, tbl_name, sql FROM sqlite_schema")
	if err != nil {
		return [32]byte{}, err
	}
	writeFramed(hash, []byte("schema"))
	writeFramed(hash, strconv.AppendInt(nil, schemaRows, 10))
	writeFramed(hash, schemaDigest[:])

	for _, pragma := range []string{"application_id", "user_version"} {
		var value int64
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&value); err != nil { // #nosec G202 -- pragma names are fixed constants above.
			return [32]byte{}, err
		}
		writeFramed(hash, []byte(pragma))
		writeFramed(hash, strconv.AppendInt(nil, value, 10))
	}

	tables, err := listTables(ctx, db)
	if err != nil {
		return [32]byte{}, err
	}
	writeUint64(hash, uint64(len(tables)))
	for _, table := range tables {
		digest, count, err := tableDigest(ctx, db, table)
		if err != nil {
			return [32]byte{}, fmt.Errorf("digest table %q: %w", table, err)
		}
		writeFramed(hash, []byte(table))
		writeFramed(hash, strconv.AppendInt(nil, count, 10))
		writeFramed(hash, digest[:])
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func listTables(ctx context.Context, db databaseQueryer) ([]string, error) {
	// Internal logical tables such as sqlite_sequence and sqlite_stat1 are
	// intentionally included. sqlite_schema itself is represented separately.
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func tableDigest(ctx context.Context, db databaseQueryer, table string) ([32]byte, int64, error) {
	query := "SELECT * FROM " + quoteIdentifier(table) // #nosec G202 -- table is read from sqlite_schema and escaped as an identifier.
	return queryDigest(ctx, db, query)
}

func queryDigest(ctx context.Context, db databaseQueryer, query string) ([32]byte, int64, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return [32]byte{}, 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return [32]byte{}, 0, err
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return [32]byte{}, 0, err
	}
	rowDigests := make([][32]byte, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return [32]byte{}, 0, err
		}
		rowHash := sha256.New()
		writeUint64(rowHash, uint64(len(values)))
		for _, value := range values {
			if err := writeDigestValue(rowHash, value); err != nil {
				return [32]byte{}, 0, err
			}
		}
		var rowDigest [32]byte
		copy(rowDigest[:], rowHash.Sum(nil))
		rowDigests = append(rowDigests, rowDigest)
	}
	if err := rows.Err(); err != nil {
		return [32]byte{}, 0, err
	}
	sort.Slice(rowDigests, func(left, right int) bool {
		return bytes.Compare(rowDigests[left][:], rowDigests[right][:]) < 0
	})
	hash := sha256.New()
	writeUint64(hash, uint64(len(columns)))
	for index, column := range columns {
		writeFramed(hash, []byte(column))
		writeFramed(hash, []byte(columnTypes[index].DatabaseTypeName()))
	}
	writeUint64(hash, uint64(len(rowDigests)))
	for _, rowDigest := range rowDigests {
		writeFramed(hash, rowDigest[:])
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, int64(len(rowDigests)), nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func writeDigestValue(writer io.Writer, value any) error {
	var tag byte
	var encoded []byte
	switch typed := value.(type) {
	case nil:
		tag = 0
	case int64:
		tag = 1
		encoded = strconv.AppendInt(nil, typed, 10)
	case float64:
		tag = 2
		encoded = make([]byte, 8)
		binary.BigEndian.PutUint64(encoded, math.Float64bits(typed))
	case bool:
		tag = 3
		if typed {
			encoded = []byte{1}
		} else {
			encoded = []byte{0}
		}
	case []byte:
		tag = 4
		encoded = typed
	case string:
		tag = 5
		encoded = []byte(typed)
	case time.Time:
		tag = 6
		encoded = []byte(typed.UTC().Format(time.RFC3339Nano))
	default:
		return fmt.Errorf("unsupported SQLite value type %T", value)
	}
	_, _ = writer.Write([]byte{tag})
	writeFramed(writer, encoded)
	return nil
}

func writeFramed(writer io.Writer, value []byte) {
	writeUint64(writer, uint64(len(value)))
	_, _ = writer.Write(value)
}

func writeUint64(writer io.Writer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- path is a private migration output.
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- path is the configured database directory.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
