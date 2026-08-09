package database

import (
	"os"
	"path/filepath"
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
