package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestCreateSnapshotStopsWhenWALCheckpointFails(t *testing.T) {
	tmpDir := t.TempDir()
	if err := InitDB(filepath.Join(tmpDir, "test.db")); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(CloseDB)

	if err := GetDB().Close(); err != nil {
		t.Fatalf("database close failed: %v", err)
	}
	snapshotDir := filepath.Join(tmpDir, "snapshots")
	if _, err := CreateSnapshot(snapshotDir); err == nil {
		t.Fatal("CreateSnapshot succeeded, want checkpoint error")
	} else if !strings.Contains(err.Error(), "WALチェックポイントエラー") {
		t.Fatalf("error = %q, want WAL checkpoint error", err)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory was created after checkpoint failure: %v", err)
	}
}

func TestDatabaseAndSnapshotPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "private-db")
	if err := os.Mkdir(dbDir, 0755); err != nil {
		t.Fatalf("database directory create failed: %v", err)
	}
	dbPath := filepath.Join(dbDir, "test.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(CloseDB)

	assertFileMode(t, dbDir, 0700)
	assertFileMode(t, dbPath, 0600)

	snapshotDir := filepath.Join(tmpDir, "snapshots")
	if err := os.Mkdir(snapshotDir, 0755); err != nil {
		t.Fatalf("snapshot directory create failed: %v", err)
	}
	snapshotPath, err := CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	assertFileMode(t, snapshotDir, 0700)
	assertFileMode(t, snapshotPath, 0600)
}

func TestRootTagUniqueIndexAndLegacyDuplicateStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(CloseDB)

	if _, err := GetDB().Exec("INSERT INTO tags (name, level) VALUES ('重複', 1)"); err != nil {
		t.Fatalf("first root tag insert failed: %v", err)
	}
	if _, err := GetDB().Exec("INSERT INTO tags (name, level) VALUES ('重複', 1)"); err == nil {
		t.Fatal("duplicate root tag insert succeeded, want unique constraint error")
	}

	if _, err := GetDB().Exec("DROP INDEX idx_tags_root_name"); err != nil {
		t.Fatalf("drop root tag index failed: %v", err)
	}
	if _, err := GetDB().Exec("INSERT INTO tags (name, level) VALUES ('重複', 1)"); err != nil {
		t.Fatalf("legacy duplicate root tag insert failed: %v", err)
	}
	CloseDB()

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB with legacy root tag duplicates failed: %v", err)
	}
	var count int
	if err := GetDB().QueryRow("SELECT COUNT(*) FROM tags WHERE name = '重複' AND parent_id IS NULL").Scan(&count); err != nil {
		t.Fatalf("legacy duplicate count query failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("legacy duplicate count = %d, want 2", count)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s failed: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}
