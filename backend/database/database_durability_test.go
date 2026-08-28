package database

import (
	"strings"
	"testing"
)

func TestWritableSQLiteConnectionsForceFullSynchronous(t *testing.T) {
	path := t.TempDir() + "/ledger.db"
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()

	var synchronous int
	if err := GetDB().QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("PRAGMA synchronous=%d, want FULL (2)", synchronous)
	}

	for name, dsn := range map[string]string{
		"live": writableSQLiteDSN(path),
		"snapshot": snapshotSQLiteDSN(path),
	} {
		if !strings.Contains(dsn, "_synchronous=FULL") {
			t.Fatalf("%s DSN does not force FULL synchronous: %s", name, dsn)
		}
	}
}

func TestCommittedDataSurvivesCloseAndReopenWithFullSynchronous(t *testing.T) {
	path := t.TempDir() + "/ledger.db"
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	if _, err := GetDB().Exec("INSERT INTO settings(key, value) VALUES(?, ?)", "durability-test", "committed"); err != nil {
		t.Fatal(err)
	}
	CloseDB()
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	var value string
	if err := GetDB().QueryRow("SELECT value FROM settings WHERE key = ?", "durability-test").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("value=%q", value)
	}
	if err := requireFullSynchronous(GetDB()); err != nil {
		t.Fatal(err)
	}
}
