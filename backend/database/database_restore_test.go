package database

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRestoreSnapshotE2E は6ステップ復元ロジックのE2Eテスト。
// 1. DB初期化 → データ挿入 → スナップショット作成
// 2. さらにデータ挿入
// 3. スナップショットから復元
// 4. 復元後のデータが手順1時点に戻っていることを検証
func TestRestoreSnapshotE2E(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")
	snapDir := filepath.Join(tmpDir, "snapshots")

	// --- Phase 1: DB初期化 + データ挿入 ---
	if err := InitDB(testDBPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	d := GetDB()
	if d == nil {
		t.Fatal("GetDB returned nil after InitDB")
	}

	// 2件挿入
	_, err := d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-01','Item1','income',1000,1000)`)
	if err != nil {
		t.Fatalf("Insert 1 failed: %v", err)
	}
	_, err = d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-02','Item2','expense',500,500)`)
	if err != nil {
		t.Fatalf("Insert 2 failed: %v", err)
	}

	var count1 int
	d.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count1)
	if count1 != 2 {
		t.Fatalf("Expected 2 rows before snapshot, got %d", count1)
	}

	// --- Phase 2: スナップショット作成 ---
	snapPath, err := CreateSnapshot(snapDir)
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	snapName := filepath.Base(snapPath)
	t.Logf("Snapshot created: %s", snapName)

	// --- Phase 3: さらにデータ挿入（3件目、4件目） ---
	d = GetDB()
	_, err = d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-03','Item3','income',2000,2500)`)
	if err != nil {
		t.Fatalf("Insert 3 failed: %v", err)
	}
	_, err = d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-04','Item4','expense',100,2400)`)
	if err != nil {
		t.Fatalf("Insert 4 failed: %v", err)
	}

	var count2 int
	d.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count2)
	if count2 != 4 {
		t.Fatalf("Expected 4 rows after extra inserts, got %d", count2)
	}
	t.Logf("Before restore: %d rows", count2)

	// --- Phase 4: スナップショットから復元 ---
	if err := RestoreSnapshot(snapDir, snapName); err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	// --- Phase 5: 復元後の検証 ---
	d = GetDB()
	if d == nil {
		t.Fatal("GetDB returned nil after restore")
	}

	var count3 int
	if err := d.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count3); err != nil {
		t.Fatalf("Count query after restore failed: %v", err)
	}

	if count3 != 2 {
		t.Fatalf("RESTORE FAILED: Expected 2 rows after restore, got %d", count3)
	}
	t.Logf("After restore: %d rows ✓ (restored to snapshot state)", count3)

	// 具体的なデータも検証
	var item1, item2 string
	d.QueryRow("SELECT item FROM transactions WHERE id=1").Scan(&item1)
	d.QueryRow("SELECT item FROM transactions WHERE id=2").Scan(&item2)
	if item1 != "Item1" || item2 != "Item2" {
		t.Fatalf("Data mismatch after restore: got %q, %q", item1, item2)
	}
	t.Log("Data verification passed ✓")

	// .bakファイルが残っていないことを確認
	bakPath := testDBPath + ".bak"
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Errorf("Backup file should have been cleaned up: %s", bakPath)
	}

	CloseDB()
}

// TestRestoreSnapshotOldVersion は古いスナップショットへの復元をテスト。
// snap1(2件) → snap2(4件) → snap1に復元 → 2件に戻ること
func TestRestoreSnapshotOldVersion(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")
	snapDir := filepath.Join(tmpDir, "snapshots")

	if err := InitDB(testDBPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	// 2件挿入 → snap1
	d := GetDB()
	d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-01','A','income',1000,1000)`)
	d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-02','B','expense',500,500)`)

	snap1Path, _ := CreateSnapshot(snapDir)
	snap1Name := filepath.Base(snap1Path)

	// さらに2件挿入 → snap2
	d = GetDB()
	d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-03','C','income',2000,2500)`)
	d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-04','D','expense',100,2400)`)

	// snap2を作成するために少し待つ（タイムスタンプが異なるように）
	snap2Path, _ := CreateSnapshot(snapDir)
	snap2Name := filepath.Base(snap2Path)
	t.Logf("snap1: %s, snap2: %s", snap1Name, snap2Name)

	// さらに1件追加（5件の状態）
	d = GetDB()
	d.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2025-01-05','E','income',3000,5400)`)

	var currentCount int
	d.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&currentCount)
	t.Logf("Current state: %d rows", currentCount)

	// snap1（2件の状態）に復元
	if err := RestoreSnapshot(snapDir, snap1Name); err != nil {
		t.Fatalf("RestoreSnapshot to snap1 failed: %v", err)
	}

	d = GetDB()
	var afterRestore int
	d.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&afterRestore)
	if afterRestore != 2 {
		t.Fatalf("Expected 2 rows after restoring snap1, got %d", afterRestore)
	}
	t.Logf("Restored to snap1: %d rows ✓", afterRestore)

	// snap2はまだ存在していること（削除されていない）
	if _, err := os.Stat(filepath.Join(snapDir, snap2Name)); os.IsNotExist(err) {
		t.Error("snap2 should still exist after restoring snap1")
	}
	t.Log("snap2 still exists after restore ✓ (no auto-deletion)")

	CloseDB()
}
