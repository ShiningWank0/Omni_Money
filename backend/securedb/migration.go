package securedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	if err := os.Chmod(stageDir, 0700); err != nil {
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
	if err := exportEncrypted(ctx, source, stagePath, key, userVersion, applicationID); err != nil {
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

func requireSQLCipherRuntime(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&version); err != nil {
		return ErrCipherUnavailable
	}
	if strings.TrimSpace(version) != RequiredSQLCipherVersion {
		return fmt.Errorf("%w: required %s", ErrCipherVersion, RequiredSQLCipherVersion)
	}
	return nil
}

func exportEncrypted(ctx context.Context, source *sql.DB, targetPath string, key RawKey, userVersion, applicationID int64) error {
	conn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	hexKey := make([]byte, hex.EncodedLen(len(key)))
	hex.Encode(hexKey, key[:])
	attach := `ATTACH DATABASE '` + sqliteQuote(targetPath) + `' AS encrypted KEY "x'` + string(hexKey) + `'"`
	clear(hexKey)
	if _, err := conn.ExecContext(ctx, attach); err != nil {
		return fmt.Errorf("attach encrypted migration target: %w", err)
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
	if _, err := conn.ExecContext(ctx, "DETACH DATABASE encrypted"); err != nil {
		return err
	}
	attached = false
	return nil
}

func sqliteQuote(value string) string { return strings.ReplaceAll(value, "'", "''") }

func compareLogicalDatabases(ctx context.Context, source, target *sql.DB) error {
	tables, err := listTables(ctx, source)
	if err != nil {
		return err
	}
	targetTables, err := listTables(ctx, target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(tables, targetTables) {
		return errors.New("migrated database schema table list differs")
	}
	for _, table := range tables {
		sourceDigest, sourceRows, err := tableDigest(ctx, source, table)
		if err != nil {
			return err
		}
		targetDigest, targetRows, err := tableDigest(ctx, target, table)
		if err != nil {
			return err
		}
		if sourceRows != targetRows || sourceDigest != targetDigest {
			return fmt.Errorf("migrated table %s differs", table)
		}
	}
	return nil
}

func listTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
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

func tableDigest(ctx context.Context, db *sql.DB, table string) ([32]byte, int64, error) {
	hash := sha256.New()
	query := "SELECT * FROM " + quoteIdentifier(table) + " ORDER BY rowid"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return [32]byte{}, 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return [32]byte{}, 0, err
	}
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return [32]byte{}, 0, err
		}
		for _, value := range values {
			writeDigestValue(hash, value)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return [32]byte{}, 0, err
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, count, nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func writeDigestValue(writer io.Writer, value any) {
	var encoded []byte
	switch typed := value.(type) {
	case nil:
		encoded = []byte{0}
	case int64:
		encoded = []byte(strconv.FormatInt(typed, 10))
	case float64:
		encoded = []byte(strconv.FormatFloat(typed, 'g', -1, 64))
	case bool:
		encoded = []byte(strconv.FormatBool(typed))
	case []byte:
		encoded = typed
	case string:
		encoded = []byte(typed)
	case time.Time:
		encoded = []byte(typed.UTC().Format(time.RFC3339Nano))
	default:
		encoded = []byte(fmt.Sprintf("%T:%v", value, value))
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(encoded)))
	_, _ = writer.Write([]byte(fmt.Sprintf("%T", value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(encoded)
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
