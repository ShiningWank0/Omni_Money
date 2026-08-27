package database

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAITransactionSecurityTablesEnforceDigestAndQuotaConstraints(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-security.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)

	if _, err := GetDB().Exec(`INSERT INTO ai_transaction_idempotency (
		credential_id, idempotency_key_sha256, request_sha256, created_at
	) VALUES ('agent', ?, ?, '2026-08-09T00:00:00Z')`, []byte("raw-key"), bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("idempotency table accepted a non-SHA-256 key")
	}
	if _, err := GetDB().Exec(`INSERT INTO ai_daily_transaction_usage (
		credential_id, utc_date, successful_creates
	) VALUES ('agent', '2026-08-09', -1)`); err == nil {
		t.Fatal("daily quota table accepted a negative count")
	}
}

func TestEmbeddedSQLiteIncludesMemorySafetyFixes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sqlite-version.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)

	var version string
	if err := GetDB().QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("embedded SQLite: %s", version)
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("parse sqlite version %q: %v", version, err)
	}
	if major < 3 || (major == 3 && minor < 50) || (major == 3 && minor == 50 && patch < 2) {
		t.Fatalf("embedded SQLite %s predates required memory-safety fixes in 3.50.2", version)
	}
}

// TestRestoreSnapshotRejectsUnsafeNames はスナップショット名による
// パストラバーサルが拒否されることを検証する回帰テスト。
func TestRestoreSnapshotRejectsUnsafeNames(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")
	snapDir := filepath.Join(tmpDir, "snapshots")

	if err := InitDB(testDBPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer CloseDB()

	// snapshots/ の外に「復元されてはいけない」.dbファイルを用意する
	outsidePath := filepath.Join(tmpDir, "outside.db")
	if err := os.WriteFile(outsidePath, []byte("evil"), 0644); err != nil {
		t.Fatalf("failed to create outside file: %v", err)
	}

	unsafeNames := []string{
		"",
		"../outside.db",
		"..\\outside.db",
		"sub/outside.db",
		filepath.Join(tmpDir, "outside.db"), // 絶対パス
		"omni_money_20250101.txt",           // .db以外
		"..",
	}

	for _, name := range unsafeNames {
		if err := RestoreSnapshot(snapDir, name); err == nil {
			t.Errorf("RestoreSnapshot(%q) should be rejected, but succeeded", name)
		}
	}

	// 拒否された場合でもDB接続が生きていること
	d := GetDB()
	if d == nil {
		t.Fatal("GetDB returned nil after rejected restore attempts")
	}
	var one int
	if err := d.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("DB should remain usable: %v", err)
	}
}
