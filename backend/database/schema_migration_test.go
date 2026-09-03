package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/models"
	"omni_money/backend/securedb"
)

func TestLedgerSchemaRecordsCurrentVersion(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	var version int
	if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != ledgerSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, ledgerSchemaVersion)
	}
}

func TestLedgerSchemaV5AddsArchiveSidecarsWithoutChangingCurrentRows(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Build the current shape once, then remove only the additive v4 objects to
	// model a v3 ledger. Existing constrained transaction/image rows must survive
	// the atomic additive migration unchanged.
	if err := createTablesOn(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'kept', 'income', 42, 42, 'memo')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER validate_transactions_amount_insert`,
		`DROP TRIGGER validate_transactions_amount_update`,
		`PRAGMA ignore_check_constraints = ON`,
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
			VALUES ('legacy', '2020-01-01', 'legacy-large', 'income', 1000000001, 1000000001, 'exact')`,
		`PRAGMA ignore_check_constraints = OFF`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare legacy amount %q: %v", statement, err)
		}
	}
	for _, statement := range []string{
		`DROP TRIGGER trg_transaction_image_archive_quota_insert`,
		`DROP TRIGGER trg_transaction_images_quota_insert`,
		`DROP TABLE transaction_image_archive`,
		`DROP TABLE transaction_archive_amounts`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare v3 schema %q: %v", statement, err)
		}
	}
	if err := createTablesOn(db); err != nil {
		t.Fatalf("v3 to v4 migration: %v", err)
	}
	var amount, balance int64
	if err := db.QueryRow(`SELECT amount, balance FROM transactions WHERE item = 'kept'`).Scan(&amount, &balance); err != nil {
		t.Fatal(err)
	}
	if amount != 42 || balance != 42 {
		t.Fatalf("current row changed during migration: %d/%d", amount, balance)
	}
	var stored, archived, legacyBalance int64
	if err := db.QueryRow(`SELECT t.amount, a.amount, t.balance FROM transactions t
		JOIN transaction_archive_amounts a ON a.transaction_id = t.id WHERE t.item = 'legacy-large'`).Scan(&stored, &archived, &legacyBalance); err != nil {
		t.Fatal(err)
	}
	if stored != 1_000_000_000 || archived != 1_000_000_001 || legacyBalance != 1_000_000_001 {
		t.Fatalf("legacy amount migration = stored %d archived %d balance %d", stored, archived, legacyBalance)
	}
	for _, table := range []string{"transaction_archive_amounts", "transaction_image_archive"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("archive table %s = %d/%v", table, count, err)
		}
	}
}

func TestLedgerSchemaV5LosslesslyRebuildsV4ArchiveAndMigratesLargeAmount(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := createTablesOn(db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
			VALUES ('cash', '2026-01-01', 'kept', 'income', 42, 42, '')`,
		`DROP TRIGGER trg_transaction_images_quota_insert`,
		`DROP TRIGGER trg_transaction_image_archive_quota_insert`,
		`DROP INDEX idx_transaction_image_archive_txid`,
		`DROP TABLE transaction_image_archive`,
		`CREATE TABLE transaction_image_archive (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			data BLOB NOT NULL CHECK(length(data) BETWEEN 1 AND 5242880),
			mime_type TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX idx_transaction_image_archive_txid ON transaction_image_archive(transaction_id)`,
		`INSERT INTO transaction_image_archive (id, transaction_id, filename, data, mime_type, created_at)
			VALUES (7, 1, ' exact.PNG ', X'010203', ' IMAGE/PNG ', '2026-01-02 03:04:05')`,
		`DROP TRIGGER validate_transactions_amount_insert`,
		`DROP TRIGGER validate_transactions_amount_update`,
		`PRAGMA ignore_check_constraints = ON`,
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
			VALUES ('legacy', '2020-01-01', 'large', 'income', 1000000001, 1000000001, '')`,
		`PRAGMA ignore_check_constraints = OFF`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare v4 schema %q: %v", statement, err)
		}
	}
	if err := createTablesOn(db); err != nil {
		t.Fatalf("v4 to v5 migration: %v", err)
	}
	var id int64
	var filename, mime, createdAt string
	var data []byte
	if err := db.QueryRow(`SELECT id, filename, data, mime_type, CAST(created_at AS TEXT) FROM transaction_image_archive`).Scan(&id, &filename, &data, &mime, &createdAt); err != nil {
		t.Fatal(err)
	}
	if id != 7 || filename != " exact.PNG " || mime != " IMAGE/PNG " || string(data) != "\x01\x02\x03" || createdAt != "2026-01-02 03:04:05" {
		t.Fatalf("v4 image changed: %d/%q/%x/%q/%q", id, filename, data, mime, createdAt)
	}
	var stored, archived int64
	if err := db.QueryRow(`SELECT t.amount, a.amount FROM transactions t JOIN transaction_archive_amounts a ON a.transaction_id=t.id WHERE t.item='large'`).Scan(&stored, &archived); err != nil {
		t.Fatal(err)
	}
	if stored != 1_000_000_000 || archived != 1_000_000_001 {
		t.Fatalf("large amount migration = %d/%d", stored, archived)
	}
	if _, err := db.Exec(`INSERT INTO transaction_image_archive (transaction_id, filename, data, mime_type) VALUES (1, '', X'', '')`); err != nil {
		t.Fatalf("v5 empty legacy image: %v", err)
	}
}
func TestLedgerSchemaMigrationFailureRollsBackAtomically(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE transactions (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	if err := createTablesOn(db); err == nil {
		t.Fatal("incompatible legacy schema was accepted")
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed migration changed schema version to %d", version)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'transaction_links'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left a partially-created transaction_links table")
	}
}

func TestLedgerSchemaRejectsForgedVersionZeroLayout(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "forged-v0.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The names satisfy the old minimum-column probe, but nullable/wrong-type
	// fields and an extra column make this an impostor rather than the one
	// allowlisted pre-marker family.
	if _, err := db.Exec(`CREATE TABLE transactions (
		id INTEGER PRIMARY KEY, account TEXT, date TEXT, item TEXT,
		type TEXT, amount TEXT, balance TEXT, memo TEXT, forged_extra TEXT
	);
	CREATE TABLE transaction_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT, transaction_id INTEGER NOT NULL,
		filename TEXT NOT NULL, data BLOB NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO transactions(id, amount) VALUES (1, NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(db); err == nil {
		t.Fatal("forged version-0 layout was migrated")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("forged migration changed schema version to %d", version)
	}
}

func TestLegacyImageMigrationRebuildsForeignKeyAndCascade(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-images.db?_foreign_keys=ON"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE transactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account TEXT NOT NULL, date DATETIME NOT NULL, item TEXT NOT NULL,
		type TEXT NOT NULL, amount INTEGER NOT NULL,
		balance INTEGER NOT NULL DEFAULT 0, memo TEXT DEFAULT ''
	);
	CREATE TABLE transaction_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transaction_id INTEGER NOT NULL, filename TEXT NOT NULL,
		data BLOB NOT NULL, mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO transactions(account,date,item,type,amount,balance) VALUES ('cash','2026-01-01','legacy','income',1,1);
	INSERT INTO transaction_images(transaction_id,filename,data,mime_type) VALUES (1,'legacy.png',X'01','image/png');`); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(db); err != nil {
		t.Fatalf("legacy migration failed: %v", err)
	}
	var foreignKeys int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_list('transaction_images')").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("transaction_images foreign key count = %d, want 1", foreignKeys)
	}
	if _, err := db.Exec("DELETE FROM transactions WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	var images int
	if err := db.QueryRow("SELECT COUNT(*) FROM transaction_images").Scan(&images); err != nil {
		t.Fatal(err)
	}
	if images != 0 {
		t.Fatalf("cascade left %d legacy images after deleting transaction", images)
	}
}

func TestVersionTwoLegacyImageRebuildRecreatesDependentObjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-v2-images.db")
	createLegacyImageSnapshot(t, path, true)
	db, err := sql.Open("sqlite3", path+"?_journal_mode=DELETE&_foreign_keys=ON")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// First perform the historical migration. This leaves the old transaction
	// DDL and a current v2 marker, matching databases produced by the previous
	// IF NOT EXISTS migration path.
	if err := createTablesOn(db); err != nil {
		t.Fatalf("initial legacy migration failed: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER trg_transaction_images_quota_insert;
		DROP TRIGGER trg_transaction_images_immutable_update;
		DROP INDEX idx_transaction_images_txid;
		DROP TABLE transaction_images;
		CREATE TABLE transaction_images (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			data BLOB NOT NULL,
			mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_transaction_images_txid ON transaction_images(transaction_id);`); err != nil {
		t.Fatalf("legacy v2 image fixture setup failed: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER trg_transaction_images_quota_insert
		BEFORE INSERT ON transaction_images
		WHEN length(NEW.data) <= 0 OR length(NEW.data) > %d
			OR (SELECT COUNT(*) FROM transaction_images WHERE transaction_id = NEW.transaction_id) >= %d
			OR COALESCE((SELECT SUM(length(data)) FROM transaction_images WHERE transaction_id = NEW.transaction_id), 0) + length(NEW.data) > %d
			OR COALESCE((SELECT SUM(length(ti.data)) FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)), 0) + length(NEW.data) > %d
			OR COALESCE((SELECT SUM(length(data)) FROM transaction_images), 0) + length(NEW.data) > %d
		BEGIN SELECT RAISE(ABORT, 'image storage quota exceeded'); END;
		CREATE TRIGGER trg_transaction_images_immutable_update
		BEFORE UPDATE ON transaction_images
		BEGIN SELECT RAISE(ABORT, 'transaction images are immutable; delete and re-add the image'); END`,
		models.MaxImageBytes, models.MaxImagesPerTransaction, models.MaxImageBytesPerTransaction,
		models.MaxImageBytesPerAccount, models.MaxImageBytesDatabase)); err != nil {
		t.Fatalf("legacy v2 image dependent trigger setup failed: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2; PRAGMA application_id = 0"); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(db); err != nil {
		t.Fatalf("version-2 legacy image rebuild failed: %v", err)
	}
	if err := validateCurrentTransactionImages(db); err != nil {
		t.Fatalf("rebuilt transaction_images is not current: %v", err)
	}
	for _, object := range []string{"idx_transaction_images_txid", "trg_transaction_images_quota_insert", "trg_transaction_images_immutable_update"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", object).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("dependent object %s count=%d, want 1", object, count)
		}
	}
}

func TestLegacyImageMigrationRejectsOrphanAndOversizedRowsAtomically(t *testing.T) {
	cases := []struct {
		name  string
		image string
	}{
		{name: "orphan", image: "INSERT INTO transaction_images(transaction_id,filename,data) VALUES (999,'orphan.png',X'01')"},
		{name: "oversized", image: "INSERT INTO transaction_images(transaction_id,filename,data) VALUES (1,'large.png',?)"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-invalid.db?_foreign_keys=ON"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE transactions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				account TEXT NOT NULL, date DATETIME NOT NULL, item TEXT NOT NULL,
				type TEXT NOT NULL, amount INTEGER NOT NULL,
				balance INTEGER NOT NULL DEFAULT 0, memo TEXT DEFAULT ''
			);
			CREATE TABLE transaction_images (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				transaction_id INTEGER NOT NULL, filename TEXT NOT NULL,
				data BLOB NOT NULL, mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
			INSERT INTO transactions(account,date,item,type,amount,balance) VALUES ('cash','2026-01-01','legacy','income',1,1);`); err != nil {
				t.Fatal(err)
			}
			if test.name == "oversized" {
				if _, err := db.Exec(test.image, make([]byte, models.MaxImageBytes+1)); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec(test.image); err != nil {
				t.Fatal(err)
			}
			if err := createTablesOn(db); err == nil {
				t.Fatal("invalid legacy image rows were migrated")
			}
			var version int
			if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != 0 {
				t.Fatalf("failed image migration changed schema version to %d", version)
			}
			var tableCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='transaction_images'").Scan(&tableCount); err != nil {
				t.Fatal(err)
			}
			if tableCount != 1 {
				t.Fatal("failed image migration removed the original table")
			}
		})
	}
}

func TestLedgerSchemaRejectsFutureVersion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "future.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(db); err == nil {
		t.Fatal("future ledger schema was accepted")
	}
}

func TestLedgerSchemaRejectsCurrentMissingIndexAndForeignKeyOrphan(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("DROP INDEX idx_transaction_images_txid"); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("current ledger missing a critical index was accepted")
	}

	// Re-open a clean current ledger and place an orphan while FK enforcement
	// is disabled only for fixture construction. foreign_key_check must still
	// reject the candidate before it can be restored or listed.
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	instance, err = OpenPlainInstance(filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("PRAGMA foreign_keys = OFF; INSERT INTO transaction_links(parent_id, child_id) VALUES (9999, 9998);"); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("current ledger with an orphan foreign-key row was accepted")
	}
}

func TestCurrentVersionWithoutIdentityStillRequiresFullConstraints(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("PRAGMA application_id = 0; DROP INDEX idx_transaction_images_txid"); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("current version with application_id=0 bypassed full schema validation")
	}
}

func TestLegacyCompatibilityRequiresExactFingerprintAndDoesNotTrustMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	createLegacyImageSnapshot(t, path, true)
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := createTablesOn(db); err != nil {
		t.Fatal(err)
	}
	// The historical transaction/image definitions are allowlisted, but every
	// generated table still has to retain current constraints.
	if _, err := db.Exec(`DROP TABLE tags; CREATE TABLE tags (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		parent_id INTEGER DEFAULT NULL, level INTEGER NOT NULL DEFAULT 1
	); CREATE INDEX idx_tags_parent ON tags(parent_id);
	CREATE UNIQUE INDEX idx_tags_root_name_unique ON tags(name) WHERE parent_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(db, true); err == nil {
		t.Fatal("legacy fingerprint bypassed constraints on a generated table")
	}
	if _, err := db.Exec(`CREATE TABLE omni_legacy_schema_compat (
		legacy_version INTEGER PRIMARY KEY CHECK(legacy_version >= 0 AND legacy_version < 2)
	); INSERT INTO omni_legacy_schema_compat(legacy_version) VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(db, true); err == nil {
		t.Fatal("forgeable legacy marker was trusted")
	}
}

func TestCurrentSchemaRejectsIneffectiveSameNamedTrigger(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "trigger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec(`DROP TRIGGER trg_transaction_images_immutable_update;
		CREATE TRIGGER trg_transaction_images_immutable_update
		BEFORE UPDATE ON transaction_images BEGIN SELECT NULL; END`); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("ineffective same-name trigger was accepted")
	}
}

func TestCurrentSchemaRejectsForgeableTriggerTokens(t *testing.T) {
	tests := []struct {
		name  string
		setup string
	}{
		{
			name: "quota when zero",
			setup: `DROP TRIGGER trg_transaction_images_quota_insert;
				CREATE TRIGGER trg_transaction_images_quota_insert
				BEFORE INSERT ON transaction_images WHEN 0
				BEGIN SELECT RAISE(ABORT, 'image storage quota exceeded'); END`,
		},
		{
			name: "amount or one",
			setup: `DROP TRIGGER validate_transactions_amount_insert;
				CREATE TRIGGER validate_transactions_amount_insert
				BEFORE INSERT ON transactions
				WHEN NEW.amount < 1 OR 1
				BEGIN SELECT RAISE(ABORT, 'transaction amount out of range'); END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "trigger.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			if _, err := instance.DB().Exec(test.setup); err != nil {
				t.Fatal(err)
			}
			if err := validateLedgerSchema(instance.DB(), true); err == nil {
				t.Fatal("token-stuffed trigger was accepted")
			}
		})
	}
}

func TestCurrentSchemaRejectsExtraPersistentObjects(t *testing.T) {
	cases := []struct {
		name  string
		setup string
	}{
		{name: "index", setup: "CREATE INDEX extra_ledger_index ON transactions(account)"},
		{name: "trigger", setup: "CREATE TRIGGER extra_ledger_trigger AFTER INSERT ON transactions BEGIN SELECT 1; END"},
		{name: "view", setup: "CREATE VIEW extra_ledger_view AS SELECT id FROM transactions"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "extra-object.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			if _, err := instance.DB().Exec(test.setup); err != nil {
				t.Fatal(err)
			}
			if err := validateLedgerSchema(instance.DB(), true); err == nil {
				t.Fatalf("extra persistent %s was accepted", test.name)
			}
		})
	}
}

func TestCurrentSchemaRejectsWritableSchemaInjectedSQLiteObject(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "writable-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("PRAGMA writable_schema = ON"); err != nil {
		t.Fatal(err)
	}
	_, insertErr := instance.DB().Exec(`INSERT INTO sqlite_master(type, name, tbl_name, rootpage, sql)
		VALUES ('trigger', 'sqlite_evil_injected', 'transactions', 0,
		'CREATE TRIGGER sqlite_evil_injected AFTER INSERT ON transactions BEGIN SELECT 1; END')`)
	_, disableErr := instance.DB().Exec("PRAGMA writable_schema = OFF")
	if insertErr != nil || disableErr != nil {
		t.Fatalf("writable_schema fixture setup failed: insert=%v disable=%v", insertErr, disableErr)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("SQL-bearing sqlite-prefixed persistent object was accepted")
	}
}

func TestCurrentSchemaRejectsWritableSchemaInjectedSQLiteTable(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "writable-table.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("PRAGMA writable_schema = ON"); err != nil {
		t.Fatal(err)
	}
	_, insertErr := instance.DB().Exec(`INSERT INTO sqlite_master(type, name, tbl_name, rootpage, sql)
		VALUES ('table', 'sqlite_evil_injected', 'sqlite_evil_injected', 0,
		'CREATE TABLE sqlite_evil_injected(secret TEXT)')`)
	_, disableErr := instance.DB().Exec("PRAGMA writable_schema = OFF")
	if insertErr != nil || disableErr != nil {
		t.Fatalf("writable_schema table fixture setup failed: insert=%v disable=%v", insertErr, disableErr)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("SQL-bearing sqlite-prefixed table was accepted")
	}
}

func TestCurrentSchemaAllowsExactSQLiteStatisticTable(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "sqlite-stat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("ANALYZE"); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err != nil {
		t.Fatalf("exact SQLite statistic table was rejected: %v", err)
	}
}

func TestCurrentSchemaRejectsPartialVariantOfNonPartialIndex(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "partial-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec(`DROP INDEX idx_transactions_account;
		CREATE INDEX idx_transactions_account ON transactions(account) WHERE 0`); err != nil {
		t.Fatal(err)
	}
	if err := validateLedgerSchema(instance.DB(), true); err == nil {
		t.Fatal("same-name partial index variant was accepted")
	}
}

func TestSchemaCanonicalizerPreservesQuotedLiteralContent(t *testing.T) {
	upper := canonicalDDL("CREATE TABLE example (value TEXT CHECK(value = 'MiXeD, (literal)'))")
	lower := canonicalDDL("CREATE TABLE example (value TEXT CHECK(value = 'mixed, (literal)'))")
	if upper == lower {
		t.Fatalf("quoted literal content was folded: %q", upper)
	}
	if got := canonicalDDL("CREATE TABLE example (value TEXT CHECK(value = 'MiXeD, (literal)'))"); got != upper {
		t.Fatalf("canonicalization is not deterministic: got %q want %q", got, upper)
	}
}

func TestBlankSQLiteFileIsNotAListableSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	probe := &Instance{opener: securedb.NewPlainOpener()}
	readonly, err := probe.opener.Open(t.Context(), path, securedb.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err := probe.validateSnapshotDatabase(readonly, path); err == nil {
		t.Fatal("blank SQLite file was accepted as a snapshot")
	}
	probe.opener.Destroy()
}

func TestBlankSameKeySnapshotIsNotRestored(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	blankPath := filepath.Join(snapshotDir, "blank-same-key.db")
	db, err := sql.Open("sqlite3", blankPath)
	if err != nil {
		t.Fatal(err)
	}
	// database/sql opens lazily; force SQLite to materialize the empty file so
	// this test exercises a real blank same-key snapshot instead of a missing
	// pathname.
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blankPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(blankPath)); err == nil {
		t.Fatal("blank same-key SQLite snapshot was accepted")
	}
	if instance.DB() == nil {
		t.Fatal("blank snapshot rejection unpublished the live database")
	}
}
