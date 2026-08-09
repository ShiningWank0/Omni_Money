package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteConnectionsUseFullSynchronous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)

	database := GetDB()
	database.SetMaxOpenConns(4)
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("Conn(%d): %v", i, err)
		}
		connections = append(connections, connection)
		var synchronous int
		if err := connection.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("PRAGMA synchronous on connection %d: %v", i, err)
		}
		if synchronous != 2 {
			t.Fatalf("connection %d synchronous=%d, want FULL (2)", i, synchronous)
		}
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatalf("close pooled connection: %v", err)
		}
	}
}

func TestCommittedDataAndFullSyncSurviveReopenAndRestore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "durable.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(CloseDB)

	if _, err := GetDB().Exec("CREATE TABLE durability_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if _, err := GetDB().Exec("INSERT INTO durability_probe (value) VALUES ('committed')"); err != nil {
		t.Fatalf("insert probe: %v", err)
	}
	snapshotPath, err := CreateSnapshot(filepath.Join(root, "snapshots"))
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	CloseDB()
	if err := InitDB(path); err != nil {
		t.Fatalf("reopen InitDB: %v", err)
	}
	assertDurabilityProbeAndSync(t)

	if _, err := GetDB().Exec("DELETE FROM durability_probe"); err != nil {
		t.Fatalf("delete probe: %v", err)
	}
	if err := RestoreSnapshot(filepath.Dir(snapshotPath), filepath.Base(snapshotPath)); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	assertDurabilityProbeAndSync(t)
}

func assertDurabilityProbeAndSync(t *testing.T) {
	t.Helper()
	var value string
	if err := GetDB().QueryRow("SELECT value FROM durability_probe").Scan(&value); err != nil {
		t.Fatalf("read committed probe: %v", err)
	}
	if value != "committed" {
		t.Fatalf("probe value=%q, want committed", value)
	}
	var synchronous int
	if err := GetDB().QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous=%d, want FULL (2)", synchronous)
	}
}

func BenchmarkSQLiteCommitDurability(b *testing.B) {
	for _, mode := range []string{"FULL", "NORMAL"} {
		b.Run(mode, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "benchmark.db")
			database, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous="+mode+"&_busy_timeout=5000")
			if err != nil {
				b.Fatal(err)
			}
			defer database.Close()
			if _, err := database.Exec("CREATE TABLE entries (value INTEGER NOT NULL)"); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := database.Exec("INSERT INTO entries (value) VALUES (?)", i); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
