//go:build !server

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"omni_money/backend/securedb"
)

const desktopSQLCipherSelfTestCanary = "omni-money-sqlcipher-release-canary"

type desktopSQLCipherSelfTestReport struct {
	OK                    bool   `json:"ok"`
	CipherVersion         string `json:"cipher_version"`
	CipherProvider        string `json:"cipher_provider"`
	CipherStatus          string `json:"cipher_status"`
	CipherMemorySecurity  string `json:"cipher_memory_security"`
	EncryptedHeader       bool   `json:"encrypted_header"`
	WrongKeyRejected      bool   `json:"wrong_key_rejected"`
	PlaintextOpenRejected bool   `json:"plaintext_open_rejected"`
	LoadExtensionOmitted  bool   `json:"load_extension_omitted"`
	ForeignKeyCheckPassed bool   `json:"foreign_key_check_passed"`
	EncryptedCanaryAtRest bool   `json:"encrypted_canary_at_rest"`
}

// runDesktopSQLCipherSelfTest proves that the currently executing shipped
// binary is linked to the required SQLCipher runtime. It runs before Wails and
// never reads or creates the user's Omni Money data directory.
func runDesktopSQLCipherSelfTest() (desktopSQLCipherSelfTestReport, error) {
	var report desktopSQLCipherSelfTestReport
	ctx := context.Background()
	directory, err := os.MkdirTemp("", "omni-money-sqlcipher-self-test-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(directory)
	databasePath := filepath.Join(directory, "probe.db")
	if err := prepareDesktopSQLCipherSelfTestStorage(directory, databasePath); err != nil {
		return report, fmt.Errorf("secure self-test storage: %w", err)
	}

	keyBytes := make([]byte, securedb.RawKeySize)
	if _, err := rand.Read(keyBytes); err != nil {
		return report, fmt.Errorf("generate self-test key: %w", err)
	}
	defer clearBytes(keyBytes)

	opener, database, err := openSelfTestDatabase(ctx, databasePath, keyBytes, securedb.Writable)
	if err != nil {
		return report, err
	}
	closePrimary := func() error {
		databaseErr := database.Close()
		opener.Destroy()
		return databaseErr
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE self_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("create encrypted self-test table: %w", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO self_test(id, value) VALUES (1, ?)`, desktopSQLCipherSelfTestCanary); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("write encrypted self-test canary: %w", err)
	}
	var canary string
	if err := database.QueryRowContext(ctx, `SELECT value FROM self_test WHERE id = 1`).Scan(&canary); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("read encrypted self-test canary: %w", err)
	}
	if canary != desktopSQLCipherSelfTestCanary {
		_ = closePrimary()
		return report, errors.New("encrypted self-test canary changed")
	}
	if err := database.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&report.CipherVersion); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("read cipher_version: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA cipher_provider").Scan(&report.CipherProvider); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("read cipher_provider: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA cipher_status").Scan(&report.CipherStatus); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("read cipher_status: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA cipher_memory_security").Scan(&report.CipherMemorySecurity); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("read cipher_memory_security: %w", err)
	}
	if strings.TrimSpace(report.CipherVersion) != securedb.RequiredSQLCipherVersion {
		_ = closePrimary()
		return report, fmt.Errorf("unexpected SQLCipher version %q", report.CipherVersion)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(report.CipherProvider)), "openssl") {
		_ = closePrimary()
		return report, fmt.Errorf("unexpected SQLCipher provider %q", report.CipherProvider)
	}
	if strings.TrimSpace(report.CipherStatus) != "1" || strings.TrimSpace(report.CipherMemorySecurity) != "1" {
		_ = closePrimary()
		return report, errors.New("SQLCipher encryption or memory security is inactive")
	}
	if err := opener.CheckIntegrity(ctx, database); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("verify encrypted self-test database: %w", err)
	}
	foreignKeys, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("run foreign_key_check: %w", err)
	}
	if foreignKeys.Next() {
		_ = foreignKeys.Close()
		_ = closePrimary()
		return report, errors.New("self-test database has a foreign key violation")
	}
	if err := foreignKeys.Close(); err != nil {
		_ = closePrimary()
		return report, err
	}
	report.ForeignKeyCheckPassed = true
	var loadExtensionOmitted int
	if err := database.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('OMIT_LOAD_EXTENSION')`).Scan(&loadExtensionOmitted); err != nil {
		_ = closePrimary()
		return report, fmt.Errorf("verify OMIT_LOAD_EXTENSION compile option: %w", err)
	}
	if loadExtensionOmitted != 1 {
		_ = closePrimary()
		return report, errors.New("SQLite release library was compiled with load_extension support")
	}
	if _, err := database.ExecContext(ctx, `SELECT load_extension('omni-money-self-test-must-not-load')`); err == nil {
		_ = closePrimary()
		return report, errors.New("SQLite load_extension is available in the release binary")
	}
	report.LoadExtensionOmitted = true
	if err := closePrimary(); err != nil {
		return report, fmt.Errorf("close encrypted self-test database: %w", err)
	}
	if err := os.Chmod(databasePath, 0600); err != nil { // #nosec G302 -- encrypted self-test DB remains owner-only.
		return report, fmt.Errorf("secure encrypted self-test database: %w", err)
	}
	if err := securedb.RequireEncryptedHeader(databasePath); err != nil {
		return report, err
	}
	report.EncryptedHeader = true
	if err := rejectVisibleSelfTestCanary(databasePath); err != nil {
		return report, err
	}
	report.EncryptedCanaryAtRest = true

	plain := securedb.NewPlainOpener()
	plainDB, err := plain.Open(ctx, databasePath, securedb.ReadOnly)
	if err == nil {
		err = plainDB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(new(int64))
		_ = plainDB.Close()
	}
	plain.Destroy()
	if err == nil {
		return report, errors.New("encrypted database was readable without a key")
	}
	report.PlaintextOpenRejected = true

	wrongKey := make([]byte, securedb.RawKeySize)
	if _, err := rand.Read(wrongKey); err != nil {
		return report, fmt.Errorf("generate wrong self-test key: %w", err)
	}
	wrongOpener, wrongDB, wrongErr := openSelfTestDatabase(ctx, databasePath, wrongKey, securedb.ReadOnly)
	clearBytes(wrongKey)
	if wrongDB != nil {
		_ = wrongDB.Close()
	}
	if wrongOpener != nil {
		wrongOpener.Destroy()
	}
	if wrongErr == nil {
		return report, errors.New("encrypted database opened with an incorrect key")
	}
	report.WrongKeyRejected = true

	reopen, reopenedDB, err := openSelfTestDatabase(ctx, databasePath, keyBytes, securedb.ReadOnly)
	if err != nil {
		return report, fmt.Errorf("reopen encrypted self-test database: %w", err)
	}
	defer reopen.Destroy()
	defer reopenedDB.Close()
	if err := reopenedDB.QueryRowContext(ctx, `SELECT value FROM self_test WHERE id = 1`).Scan(&canary); err != nil {
		return report, fmt.Errorf("re-read encrypted self-test canary: %w", err)
	}
	if canary != desktopSQLCipherSelfTestCanary {
		return report, errors.New("reopened encrypted self-test canary changed")
	}

	report.OK = true
	return report, nil
}

func openSelfTestDatabase(ctx context.Context, path string, keyBytes []byte, purpose securedb.Purpose) (*securedb.Opener, *sql.DB, error) {
	key, err := securedb.NewRawKey(keyBytes)
	if err != nil {
		return nil, nil, err
	}
	opener := securedb.NewEncryptedOpener(key)
	key.Destroy()
	database, err := opener.Open(ctx, path, purpose)
	if err != nil {
		opener.Destroy()
		return nil, nil, err
	}
	return opener, database, nil
}

func rejectVisibleSelfTestCanary(databasePath string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		content, err := os.ReadFile(databasePath + suffix) // #nosec G304 -- exact private self-test paths only.
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte(desktopSQLCipherSelfTestCanary)) {
			clearBytes(content)
			return fmt.Errorf("self-test canary is visible in encrypted database member %q", suffix)
		}
		clearBytes(content)
	}
	return nil
}
