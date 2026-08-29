package control

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openSchemaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "control-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range schemaV1Statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestControlSchemaV2MigrationPreservesExistingUsers(t *testing.T) {
	db := openSchemaTestDB(t)
	if _, err := db.Exec(`INSERT INTO users(
		id, email, display_name, role, state, vault_id, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, 'admin', 'active', ?, 1, 1)`, testAdminID, "admin@example.com", "Admin", "vault_"+testAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO password_credentials(user_id, envelope_json, created_at_ms, updated_at_ms)
		VALUES (?, '{}', 1, 1)`, testAdminID); err != nil {
		t.Fatal(err)
	}

	if err := initializeSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version, users, passkeyTables int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", testAdminID).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'passkey_credentials'`).Scan(&passkeyTables); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || users != 1 || passkeyTables != 1 {
		t.Fatalf("migration result version=%d users=%d passkeyTables=%d", version, users, passkeyTables)
	}
}

func TestControlSchemaV2MigrationFailureIsAtomic(t *testing.T) {
	db := openSchemaTestDB(t)
	if _, err := db.Exec(`CREATE TABLE passkey_credentials (credential_id BLOB PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	if err := initializeSchema(context.Background(), db); err == nil {
		t.Fatal("incompatible passkey table was accepted")
	}
	var version, indexes int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_passkey_credentials_user'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if version != 1 || indexes != 0 {
		t.Fatalf("failed migration changed state: version=%d indexes=%d", version, indexes)
	}
}
