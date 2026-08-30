// Package database はSQLite接続、初期化、スナップショット機能を提供する
package database

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/validation"
)

const defaultSnapshotMaxTotalBytes int64 = 2 * 1024 * 1024 * 1024
const ledgerSchemaVersion = 5

const writableSQLiteQuery = "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=FULL"
const snapshotSQLiteQuery = "mode=rw&_busy_timeout=5000&_foreign_keys=ON&_synchronous=FULL"

var umask sync.Once

// Instance owns one database connection, its SQLCipher opener, and the entire
// snapshot lifecycle. Multiple instances can therefore be used independently
// (for example, one encrypted vault per server user) without sharing mutable
// package state. An Instance must not be copied after first use.
type Instance struct {
	db     *sql.DB
	path   string
	opener *securedb.Opener
	mu     sync.RWMutex

	// snapshotLifecycle serializes database lifecycle changes with snapshot
	// operations. A snapshot keeps a database handle and path for the whole
	// copy, so closing the database must wait for it to finish.
	snapshotLifecycle sync.RWMutex
	dbLifecycleMu     sync.Mutex
	snapshotMu        sync.Mutex
	snapshotInit      sync.Once
	snapshotCond      *sync.Cond
	snapshotRunning   bool
	snapshotPending   bool
	snapshotClosing   bool
}

func newInstance() *Instance {
	instance := &Instance{}
	instance.ensureSnapshotCond()
	return instance
}

func (i *Instance) ensureSnapshotCond() {
	i.snapshotInit.Do(func() {
		i.snapshotCond = sync.NewCond(&i.snapshotMu)
	})
}

// defaultInstance backs the historical package-level API used by Desktop and
// existing services. New server code should hold an explicit Instance.
var defaultInstance = newInstance()

// OpenPlainInstance opens a standalone plaintext SQLite instance. This is
// retained for compatibility and tests; server and Desktop vaults should use
// OpenEncryptedInstance.
func OpenPlainInstance(path string) (*Instance, error) {
	return openInstance(path, securedb.NewPlainOpener(), false)
}

// OpenEncryptedInstance opens a standalone SQLCipher instance, atomically
// migrating an existing plaintext database before publishing the connection.
func OpenEncryptedInstance(path string, key securedb.RawKey) (*Instance, error) {
	defer key.Destroy()
	return openInstance(path, securedb.NewEncryptedOpener(key), true)
}

// OpenExistingEncryptedInstance opens only an existing SQLCipher database.
// Unlike OpenEncryptedInstance it never creates a database or migrates a
// plaintext replacement. Desktop unlock uses this strict boundary after a
// manifest has been activated so a downgrade or path substitution fails
// closed instead of being silently adopted.
func OpenExistingEncryptedInstance(path string, key securedb.RawKey) (*Instance, error) {
	defer key.Destroy()
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("encrypted database path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect existing encrypted database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("existing encrypted database must be a regular non-symlink file")
	}
	if err := securedb.RequireEncryptedHeader(path); err != nil {
		return nil, fmt.Errorf("reject non-encrypted database: %w", err)
	}
	return openInstance(path, securedb.NewEncryptedOpener(key), false)
}

func openInstance(path string, opener *securedb.Opener, migratePlaintext bool) (*Instance, error) {
	instance := newInstance()
	if err := instance.initialize(path, opener, migratePlaintext); err != nil {
		return nil, err
	}
	return instance, nil
}

// InitDB はSQLiteデータベースを初期化する。
// wails build でバインディング生成時にも呼ばれるため、sync.Once は使わない。
// 既に接続がある場合はまず閉じてから再接続する。
func InitDB(path string) error {
	return defaultInstance.initialize(path, securedb.NewPlainOpener(), false)
}

// InitEncryptedDB はSQLCipher鍵を接続ごとに適用し、平文DBが存在する場合は
// 検証済みの暗号化コピーへ原子的に移行してから初期化する。
func InitEncryptedDB(path string, key securedb.RawKey) error {
	defer key.Destroy()
	return defaultInstance.initialize(path, securedb.NewEncryptedOpener(key), true)
}

func (i *Instance) initialize(path string, opener *securedb.Opener, migratePlaintext bool) error {
	if i == nil {
		opener.Destroy()
		return fmt.Errorf("データベースinstanceが初期化されていません")
	}
	i.ensureSnapshotCond()
	i.beginDBLifecycle()
	defer i.endDBLifecycle()

	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.initDBLocked(path, opener, migratePlaintext); err != nil {
		if i.db != nil {
			_ = i.db.Close()
			i.db = nil
		}
		if i.opener == opener {
			i.opener = nil
		}
		opener.Destroy()
		return err
	}
	return nil
}

// initDBLocked は mu.Lock() を保持した状態で呼び出す前提の初期化本体。
// RestoreSnapshot のようにロックを保持したまま再初期化する経路と共有する。
func (i *Instance) initDBLocked(path string, opener *securedb.Opener, migratePlaintext bool) error {
	// SQLiteが後から作成するWAL/SHM/rollback journalも所有者だけが読めるよう、
	// プロセスのファイル作成マスクを一度だけ制限する。
	umask.Do(setRestrictiveUmask)

	// 既存の接続があればまず閉じる
	if i.db != nil {
		i.db.Close()
		i.db = nil
	}
	if opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	if i.opener != nil && i.opener != opener {
		i.opener.Destroy()
	}
	i.opener = opener

	if path == "" {
		path = "omni_money.db"
	}
	i.path = path

	// データベースディレクトリが存在しない場合はprivate権限で作成する。
	// 既存ディレクトリのACLや共有設定はアプリから無条件に変更しない。
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := ensurePrivateDir(dir); err != nil {
			return fmt.Errorf("データベースディレクトリ作成エラー: %w", err)
		}
	}
	if migratePlaintext {
		if err := securedb.EnsureEncrypted(context.Background(), path, opener); err != nil {
			return fmt.Errorf("SQLCipher移行エラー: %w", err)
		}
	}
	if err := preparePrivateDatabaseFile(path); err != nil {
		return fmt.Errorf("データベースファイル準備エラー: %w", err)
	}

	var err error
	i.db, err = opener.Open(context.Background(), path, securedb.Writable)
	if err != nil {
		return fmt.Errorf("データベース接続エラー: %w", err)
	}

	// 接続テスト
	if err := i.db.Ping(); err != nil {
		i.db.Close()
		i.db = nil
		return fmt.Errorf("データベースping失敗: %w", err)
	}
	if err := requireFullSynchronous(i.db); err != nil {
		i.db.Close()
		i.db = nil
		return fmt.Errorf("データベース耐久性設定エラー: %w", err)
	}
	// SQLite creates the file on first open.  Restrict an existing database as
	// well: it may contain the user's complete financial history.
	if err := os.Chmod(path, 0600); err != nil {
		i.db.Close()
		i.db = nil
		return fmt.Errorf("データベースファイル権限設定エラー: %w", err)
	}

	// テーブル作成
	if err := createTablesOn(i.db); err != nil {
		return fmt.Errorf("テーブル作成エラー: %w", err)
	}
	if err := hardenSQLiteFiles(path); err != nil {
		return fmt.Errorf("データベース権限設定エラー: %w", err)
	}
	if opener.Encrypted() {
		if err := securedb.RequireEncryptedHeader(path); err != nil {
			return fmt.Errorf("SQLCipher暗号化検証エラー: %w", err)
		}
	}

	log.Printf("データベース初期化完了: %s", path)
	return nil
}

// GetDB はデータベース接続を返す
func GetDB() *sql.DB {
	return defaultInstance.DB()
}

// DB returns this instance's current database handle, or nil after Close.
func (i *Instance) DB() *sql.DB {
	if i == nil {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.db
}

// CloseDB はデータベース接続を閉じる
func CloseDB() {
	_ = defaultInstance.Close()
}

// Close waits for snapshots, closes the connection, and destroys the opener's
// in-memory key. It is safe to call more than once.
func (i *Instance) Close() error {
	if i == nil {
		return nil
	}
	i.ensureSnapshotCond()
	i.beginDBLifecycle()
	defer i.endDBLifecycle()

	i.mu.Lock()
	defer i.mu.Unlock()
	var closeErr error
	if i.db != nil {
		closeErr = i.db.Close()
		i.db = nil
		log.Println("データベース接続を閉じました")
	}
	if i.opener != nil {
		i.opener.Destroy()
		i.opener = nil
	}
	return closeErr
}

// createTablesOn は指定した接続へ現行スキーマ、index、triggerを適用する。
// 復元候補をグローバル接続へ公開する前に移行できるよう、接続を引数で受け取る。
func createTablesOn(target *sql.DB) error {
	if target == nil {
		return fmt.Errorf("データベース接続が初期化されていません")
	}
	var version int
	if err := target.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("スキーマversion取得エラー: %w", err)
	}
	if version > ledgerSchemaVersion {
		return fmt.Errorf("データベースschema version %dは対応version %dより新しいため開けません", version, ledgerSchemaVersion)
	}

	tx, err := target.Begin()
	if err != nil {
		return fmt.Errorf("スキーマtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	archiveImageTableSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS transaction_image_archive (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transaction_id INTEGER NOT NULL,
		filename TEXT NOT NULL CHECK(length(CAST(filename AS BLOB)) <= %d),
		data BLOB NOT NULL CHECK(length(data) BETWEEN 0 AND %d),
		mime_type TEXT NOT NULL CHECK(length(CAST(mime_type AS BLOB)) <= %d),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
	)`, models.MaxArchivedImageMetadataBytes, models.MaxArchivedImageBytes, models.MaxArchivedImageMetadataBytes)
	if version < 5 {
		// v5 tightens and extends archive accounting. Recreate both named triggers
		// in this same schema transaction so v4 definitions cannot survive their
		// CREATE TRIGGER IF NOT EXISTS declarations.
		for _, name := range []string{"trg_transaction_images_quota_insert", "trg_transaction_image_archive_quota_insert"} {
			if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
				return fmt.Errorf("旧画像quota trigger削除エラー: %w", err)
			}
		}
		if version == 4 {
			// SQLite cannot add CHECK constraints in place. Rebuild the v4 archive
			// table atomically and copy every ID and byte exactly. Any row outside the
			// new explicit bounds aborts and rolls back rather than being truncated.
			if _, err := tx.Exec(`ALTER TABLE transaction_image_archive RENAME TO transaction_image_archive_v4`); err != nil {
				return fmt.Errorf("v4 legacy画像table退避エラー: %w", err)
			}
			if _, err := tx.Exec(archiveImageTableSQL); err != nil {
				return fmt.Errorf("v5 legacy画像table作成エラー: %w", err)
			}
			if _, err := tx.Exec(`INSERT INTO transaction_image_archive (id, transaction_id, filename, data, mime_type, created_at)
				SELECT id, transaction_id, filename, data, mime_type, created_at FROM transaction_image_archive_v4`); err != nil {
				return fmt.Errorf("v4 legacy画像lossless移行エラー: %w", err)
			}
			if _, err := tx.Exec(`DROP TABLE transaction_image_archive_v4`); err != nil {
				return fmt.Errorf("v4 legacy画像table削除エラー: %w", err)
			}
		}
	}

	statements := []string{
		// 取引テーブル
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account TEXT NOT NULL,
			date DATETIME NOT NULL,
			item TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('income', 'expense')),
			amount INTEGER NOT NULL CHECK(amount BETWEEN 1 AND %d),
			balance INTEGER NOT NULL DEFAULT 0,
			memo TEXT DEFAULT ''
		)`, validation.MaxTransactionAmount),
		// 取引紐付けテーブル
		`CREATE TABLE IF NOT EXISTS transaction_links (
			parent_id INTEGER NOT NULL,
			child_id INTEGER NOT NULL,
			PRIMARY KEY (parent_id, child_id),
			FOREIGN KEY (parent_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (child_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`,
		// 取引画像テーブル（Agent.md §6.5）
		`CREATE TABLE IF NOT EXISTS transaction_images (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			data BLOB NOT NULL,
			mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`,
		// Archive sidecars preserve historical values that cannot be represented by
		// current write constraints. They are not writable by normal APIs: CSV
		// restore is the sole producer and validates int64/binary/global bounds.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS transaction_archive_amounts (
			transaction_id INTEGER PRIMARY KEY,
			amount INTEGER NOT NULL CHECK(amount > %d),
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`, validation.MaxTransactionAmount),
		archiveImageTableSQL,
		// タグテーブル（Agent.md §6.6: 3階層タグシステム）
		`CREATE TABLE IF NOT EXISTS tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER DEFAULT NULL,
			level INTEGER NOT NULL DEFAULT 1 CHECK(level IN (1, 2, 3)),
			legacy_duplicate INTEGER NOT NULL DEFAULT 0 CHECK(legacy_duplicate IN (0, 1)),
			FOREIGN KEY (parent_id) REFERENCES tags(id) ON DELETE CASCADE,
			UNIQUE(name, parent_id)
		)`,
		// 取引タグ紐付けテーブル
		`CREATE TABLE IF NOT EXISTS transaction_tags (
			transaction_id INTEGER NOT NULL,
			tag_id INTEGER NOT NULL,
			PRIMARY KEY (transaction_id, tag_id),
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
		)`,
		// AI transaction idempotency metadata intentionally contains only
		// digests and the minimal write-only response. Raw idempotency keys and
		// request bodies must never be persisted.
		`CREATE TABLE IF NOT EXISTS ai_transaction_idempotency (
			credential_id TEXT NOT NULL,
			idempotency_key_sha256 BLOB NOT NULL CHECK(length(idempotency_key_sha256) = 32),
			request_sha256 BLOB NOT NULL CHECK(length(request_sha256) = 32),
			transaction_id INTEGER,
			response_account TEXT,
			response_date TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (credential_id, idempotency_key_sha256),
			UNIQUE (transaction_id),
			CHECK (
				(transaction_id IS NULL AND response_account IS NULL AND response_date IS NULL)
				OR (transaction_id IS NOT NULL AND response_account IS NOT NULL AND response_date IS NOT NULL)
			)
		)`,
		// Quotas are persisted in the ledger database and updated in the same
		// SQL transaction as the corresponding successful AI write.
		`CREATE TABLE IF NOT EXISTS ai_daily_transaction_usage (
			credential_id TEXT NOT NULL,
			utc_date TEXT NOT NULL CHECK(length(utc_date) = 10),
			successful_creates INTEGER NOT NULL CHECK(successful_creates >= 0),
			PRIMARY KEY (credential_id, utc_date)
		)`,
		// 設定テーブル
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		// インデックス
		`CREATE INDEX IF NOT EXISTS idx_transactions_account ON transactions(account)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_account_date_id ON transactions(account, date, id)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_date ON transactions(date)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_item ON transactions(item)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_memo ON transactions(memo)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_links_child_id ON transaction_links(child_id)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_images_txid ON transaction_images(transaction_id)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_image_archive_txid ON transaction_image_archive(transaction_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_idempotency_credential_key
			ON ai_transaction_idempotency(credential_id, idempotency_key_sha256)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_idempotency_transaction
			ON ai_transaction_idempotency(transaction_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_daily_usage_credential_date
			ON ai_daily_transaction_usage(credential_id, utc_date)`,
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_transaction_images_quota_insert
			BEFORE INSERT ON transaction_images
			WHEN length(NEW.data) <= 0
				OR length(NEW.data) > %d
				OR ((SELECT COUNT(*) FROM transaction_images WHERE transaction_id = NEW.transaction_id)
					+ (SELECT COUNT(*) FROM transaction_image_archive WHERE transaction_id = NEW.transaction_id)) >= %d
				OR (COALESCE((SELECT SUM(length(data)) FROM transaction_images WHERE transaction_id = NEW.transaction_id), 0)
					+ COALESCE((SELECT SUM(length(data)) FROM transaction_image_archive WHERE transaction_id = NEW.transaction_id), 0)
					+ length(NEW.data)) > %d
				OR COALESCE((
					SELECT SUM(bytes) FROM (
						SELECT length(ti.data) AS bytes FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id
						WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)
						UNION ALL
						SELECT length(ai.data) AS bytes FROM transaction_image_archive ai JOIN transactions t ON t.id = ai.transaction_id
						WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)
					)
				), 0) + length(NEW.data) > %d
				OR COALESCE((SELECT SUM(bytes) FROM (
					SELECT length(data) AS bytes FROM transaction_images
					UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive
				)), 0) + length(NEW.data) > %d
			BEGIN
				SELECT RAISE(ABORT, 'image storage quota exceeded');
			END`,
			models.MaxImageBytes,
			models.MaxImagesPerTransaction,
			models.MaxImageBytesPerTransaction,
			models.MaxImageBytesPerAccount,
			models.MaxImageBytesDatabase,
		),
		`CREATE TRIGGER IF NOT EXISTS trg_transaction_images_immutable_update
			BEFORE UPDATE ON transaction_images
			BEGIN
				SELECT RAISE(ABORT, 'transaction images are immutable; delete and re-add the image');
			END`,
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_transaction_image_archive_quota_insert
			BEFORE INSERT ON transaction_image_archive
			WHEN length(NEW.data) > %d
				OR length(CAST(NEW.filename AS BLOB)) > %d
				OR length(CAST(NEW.mime_type AS BLOB)) > %d
				OR ((SELECT COUNT(*) FROM transaction_images WHERE transaction_id = NEW.transaction_id)
					+ (SELECT COUNT(*) FROM transaction_image_archive WHERE transaction_id = NEW.transaction_id)) >= %d
				OR (SELECT COUNT(*) FROM transaction_image_archive) >= %d
				OR COALESCE((
					SELECT SUM(bytes) FROM (
						SELECT length(ti.data) AS bytes FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id
						WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)
						UNION ALL
						SELECT length(ai.data) AS bytes FROM transaction_image_archive ai JOIN transactions t ON t.id = ai.transaction_id
						WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)
					)
				), 0) + length(NEW.data) > %d
				OR COALESCE((SELECT SUM(bytes) FROM (
					SELECT length(data) AS bytes FROM transaction_images
					UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive
				)), 0) + length(NEW.data) > %d
			BEGIN
				SELECT RAISE(ABORT, 'archive image storage quota exceeded');
			END`, models.MaxArchivedImageBytes, models.MaxArchivedImageMetadataBytes, models.MaxArchivedImageMetadataBytes,
			models.MaxArchivedImagesPerTransaction, models.MaxArchivedImagesDatabase,
			models.MaxImageBytesPerAccount, models.MaxImageBytesDatabase),
		`CREATE INDEX IF NOT EXISTS idx_tags_parent ON tags(parent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_tags_txid ON transaction_tags(transaction_id)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_tags_tagid ON transaction_tags(tag_id)`,
		// 既存DBにも共通金額上限を適用する防御的トリガー。
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS validate_transactions_amount_insert
			BEFORE INSERT ON transactions
			WHEN NEW.amount < 1 OR NEW.amount > %d
			BEGIN
				SELECT RAISE(ABORT, 'transaction amount out of range');
			END`, validation.MaxTransactionAmount),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS validate_transactions_amount_update
			BEFORE UPDATE OF amount ON transactions
			WHEN NEW.amount < 1 OR NEW.amount > %d
			BEGIN
				SELECT RAISE(ABORT, 'transaction amount out of range');
			END`, validation.MaxTransactionAmount),
	}

	// Version 0 includes both a brand-new database and databases created by
	// older releases before schema versions were recorded. Reapplying the
	// idempotent declarations upgrades either case atomically.
	if version < ledgerSchemaVersion {
		for _, stmt := range statements {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("SQL実行エラー (%s): %w", stmt[:50], err)
			}
		}
		if version < 5 {
			// Some pre-v5 databases were created before the shared amount trigger
			// and may contain positive int64 amounts above the current write limit.
			// Capture the exact value before replacing it with a constrained
			// surrogate; all service reads use the sidecar as the effective amount.
			if _, err := tx.Exec(`INSERT OR REPLACE INTO transaction_archive_amounts (transaction_id, amount)
				SELECT id, amount FROM transactions WHERE amount > ?`, validation.MaxTransactionAmount); err != nil {
				return fmt.Errorf("legacy取引金額archive移行エラー: %w", err)
			}
			if _, err := tx.Exec(`UPDATE transactions SET amount = ? WHERE amount > ?`, validation.MaxTransactionAmount, validation.MaxTransactionAmount); err != nil {
				return fmt.Errorf("legacy取引金額surrogate移行エラー: %w", err)
			}
		}
		// v2 and earlier databases have no marker column. Add it atomically,
		// preserve every pre-existing duplicate root, and mark all but the
		// lowest-id row as archive-only. Normal writes remain unique through the
		// partial index; CSV v3 tag_legacy rows can restore the historical rows
		// without merging, renaming, or dropping them.
		var hasLegacyDuplicate int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tags') WHERE name = 'legacy_duplicate'`).Scan(&hasLegacyDuplicate); err != nil {
			return fmt.Errorf("rootタグ互換列検査エラー: %w", err)
		}
		if hasLegacyDuplicate == 0 {
			if _, err := tx.Exec(`ALTER TABLE tags ADD COLUMN legacy_duplicate INTEGER NOT NULL DEFAULT 0 CHECK(legacy_duplicate IN (0, 1))`); err != nil {
				return fmt.Errorf("rootタグ互換列追加エラー: %w", err)
			}
		}
		if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_tags_root_name_unique`); err != nil {
			return fmt.Errorf("rootタグ一意index更新エラー: %w", err)
		}
		if _, err := tx.Exec(`UPDATE tags SET legacy_duplicate = CASE WHEN id IN (
			SELECT MIN(id) FROM tags WHERE parent_id IS NULL GROUP BY name
		) THEN 0 ELSE CASE WHEN parent_id IS NULL THEN 1 ELSE legacy_duplicate END END`); err != nil {
			return fmt.Errorf("rootタグ互換marker更新エラー: %w", err)
		}
		if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tags_root_name_unique ON tags(name) WHERE parent_id IS NULL AND legacy_duplicate = 0`); err != nil {
			return fmt.Errorf("rootタグ一意index作成エラー: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", ledgerSchemaVersion)); err != nil {
			return fmt.Errorf("スキーマversion更新エラー: %w", err)
		}
	}
	if err := validateCriticalSchema(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("スキーマtransaction確定エラー: %w", err)
	}

	return nil
}

type schemaQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func validateCriticalSchema(target schemaQueryer) error {
	if target == nil {
		return fmt.Errorf("データベース接続が初期化されていません")
	}
	requiredColumns := map[string][]string{
		"transactions":                {"id", "account", "date", "item", "type", "amount", "balance", "memo"},
		"transaction_images":          {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"transaction_archive_amounts": {"transaction_id", "amount"},
		"transaction_image_archive":   {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"tags":                        {"id", "name", "parent_id", "level", "legacy_duplicate"},
		"ai_transaction_idempotency":  {"credential_id", "idempotency_key_sha256", "request_sha256", "transaction_id", "response_account", "response_date", "created_at"},
		"ai_daily_transaction_usage":  {"credential_id", "utc_date", "successful_creates"},
	}
	for table, required := range requiredColumns {
		rows, err := target.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return fmt.Errorf("必須列検査エラー (%s): %w", table, err)
		}
		found := make(map[string]struct{}, len(required))
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue interface{}
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("必須列検査スキャンエラー (%s): %w", table, err)
			}
			found[name] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("必須列検査クローズエラー (%s): %w", table, err)
		}
		for _, name := range required {
			if _, ok := found[name]; !ok {
				return fmt.Errorf("必須列が不足しています: %s.%s", table, name)
			}
		}
	}

	requiredObjects := []struct {
		objectType string
		name       string
	}{
		{objectType: "index", name: "idx_transaction_images_txid"},
		{objectType: "index", name: "idx_transaction_image_archive_txid"},
		{objectType: "index", name: "idx_tags_root_name_unique"},
		{objectType: "index", name: "idx_ai_idempotency_credential_key"},
		{objectType: "index", name: "idx_ai_idempotency_transaction"},
		{objectType: "index", name: "idx_ai_daily_usage_credential_date"},
		{objectType: "trigger", name: "trg_transaction_images_quota_insert"},
		{objectType: "trigger", name: "trg_transaction_images_immutable_update"},
		{objectType: "trigger", name: "trg_transaction_image_archive_quota_insert"},
	}
	for _, object := range requiredObjects {
		var count int
		if err := target.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?",
			object.objectType,
			object.name,
		).Scan(&count); err != nil {
			return fmt.Errorf("必須DBオブジェクト検査エラー (%s): %w", object.name, err)
		}
		if count != 1 {
			return fmt.Errorf("必須DBオブジェクトが不足しています: %s", object.name)
		}
	}
	if err := validateRootTagIndex(target); err != nil {
		return err
	}
	return nil
}

// validateRootTagIndex verifies the definition, not just the object name.
// CREATE UNIQUE INDEX IF NOT EXISTS is intentionally not sufficient here:
// SQLite silently leaves an existing non-unique index untouched when the
// names collide.
func validateRootTagIndex(target schemaQueryer) error {
	var objectType, tableName string
	var definition sql.NullString
	if err := target.QueryRow(`
		SELECT type, tbl_name, sql FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_tags_root_name_unique'`).Scan(&objectType, &tableName, &definition); err != nil {
		return fmt.Errorf("rootタグ一意index定義の検査に失敗しました: %w", err)
	}
	if objectType != "index" || tableName != "tags" || !definition.Valid {
		return fmt.Errorf("rootタグ一意indexの対象が不正です")
	}
	canonicalSQL := strings.Join(strings.Fields(strings.ToLower(definition.String)), " ")
	if canonicalSQL != "create unique index idx_tags_root_name_unique on tags(name) where parent_id is null and legacy_duplicate = 0" {
		return fmt.Errorf("rootタグ一意indexの定義が不正です: %q", definition.String)
	}

	rows, err := target.Query("PRAGMA index_list(tags)")
	if err != nil {
		return fmt.Errorf("rootタグ一意index属性の検査に失敗しました: %w", err)
	}
	found := false
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rootタグ一意index属性の読み取りに失敗しました: %w", err)
		}
		if name == "idx_tags_root_name_unique" {
			if unique != 1 || partial != 1 {
				_ = rows.Close()
				return fmt.Errorf("rootタグ一意indexはunique partialでなければなりません")
			}
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("rootタグ一意index属性の終了に失敗しました: %w", err)
	}
	if !found {
		return fmt.Errorf("rootタグ一意indexがindex_listにありません")
	}

	rows, err = target.Query(`PRAGMA index_info("idx_tags_root_name_unique")`)
	if err != nil {
		return fmt.Errorf("rootタグ一意index列の検査に失敗しました: %w", err)
	}
	columns := 0
	for rows.Next() {
		var seq, cid int
		var column string
		if err := rows.Scan(&seq, &cid, &column); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rootタグ一意index列の読み取りに失敗しました: %w", err)
		}
		columns++
		if columns != 1 || column != "name" {
			_ = rows.Close()
			return fmt.Errorf("rootタグ一意indexの列がnameではありません")
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("rootタグ一意index列の終了に失敗しました: %w", err)
	}
	if columns != 1 {
		return fmt.Errorf("rootタグ一意indexの列数が不正です: %d", columns)
	}
	return nil
}

// --- スナップショット機能 (Agent.md §6.2) ---

// getSnapshotDir はDBパスと同じディレクトリ配下の snapshots/ を返す。
// ユーザーが保存場所を意識しなくて済むようにアプリデータ内に格納する。
func (i *Instance) getSnapshotDir() string {
	i.mu.RLock()
	p := i.path
	i.mu.RUnlock()
	if p == "" {
		return "snapshots"
	}
	return filepath.Join(filepath.Dir(p), "snapshots")
}

// CreateSnapshot は現在のDBファイルのスナップショットを作成する。
// snapshotDir にタイムスタンプ付きのコピーを保存する。
func CreateSnapshot(snapshotDir string) (string, error) {
	return defaultInstance.CreateSnapshot(snapshotDir)
}

// CreateSnapshot creates a consistent snapshot of this instance.
func (i *Instance) CreateSnapshot(snapshotDir string) (string, error) {
	if i == nil {
		return "", fmt.Errorf("データベースinstanceが初期化されていません")
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	return i.createSnapshot(snapshotDir)
}

// createSnapshot performs the copy while holding the database lock.  It is
// called by CreateSnapshot and the auto-snapshot worker, both of which hold a
// read lock on snapshotLifecycle for the duration of the operation.
func (i *Instance) createSnapshot(snapshotDir string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	currentPath := i.path
	currentDB := i.db
	currentOpener := i.opener

	if currentPath == "" {
		return "", fmt.Errorf("データベースが初期化されていません")
	}

	if currentDB == nil || currentOpener == nil {
		return "", fmt.Errorf("データベースが初期化されていません")
	}

	defaultSnapshotDir := snapshotDir == ""
	if defaultSnapshotDir {
		snapshotDir = filepath.Join(filepath.Dir(currentPath), "snapshots")
	}

	if err := ensurePrivateDir(snapshotDir); err != nil {
		return "", fmt.Errorf("スナップショットディレクトリ作成エラー: %w", err)
	}
	if defaultSnapshotDir {
		if err := os.Chmod(snapshotDir, 0700); err != nil { // #nosec G302 -- the default snapshot directory is intentionally private.
			return "", fmt.Errorf("スナップショットディレクトリ権限設定エラー: %w", err)
		}
	}

	budget, err := snapshotMaxTotalBytes()
	if err != nil {
		return "", err
	}
	requiredBytes, err := sqliteDatabaseSize(currentDB)
	if err != nil {
		return "", fmt.Errorf("スナップショット必要容量取得エラー: %w", err)
	}
	if requiredBytes > budget {
		return "", fmt.Errorf("スナップショット必要容量 %d bytes が総容量上限 %d bytes を超えます", requiredBytes, budget)
	}
	// Reserve both one generation slot and the estimated destination bytes
	// before creating a new file. Only existing snapshot files are candidates;
	// the live DB and any in-progress output are outside this deletion set.
	if err := pruneSnapshots(snapshotDir, 29, budget-requiredBytes, ""); err != nil {
		return "", fmt.Errorf("スナップショット容量確保エラー: %w", err)
	}

	// Nanosecond precision avoids same-process collisions.  copyFile also uses
	// O_EXCL, so a collision or pre-existing symlink fails closed instead of
	// overwriting an existing snapshot.
	timestamp := time.Now().UTC().Format("20060102_150405.000000000")
	// ドットをアンダースコアに置換してファイル名に安全な形式にする
	timestamp = strings.ReplaceAll(timestamp, ".", "_")
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("omni_money_%s.db", timestamp))

	// sqlite3_backup APIはWALを含む一貫した状態をオンラインで複製する。
	// TRUNCATE checkpointを実行しないため、直後の取引更新と競合しない。
	if err := backupSQLiteDatabase(currentOpener, currentDB, snapshotPath); err != nil {
		return "", fmt.Errorf("スナップショット作成エラー: %w", err)
	}
	if err := pruneSnapshots(snapshotDir, 30, budget, snapshotPath); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("スナップショット容量検査エラー: %w", err)
	}
	if err := syncDirectory(snapshotDir); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("スナップショットdirectory fsyncエラー: %w", err)
	}

	// Audit records deliberately contain only the operation and result.  The
	// vault directory and snapshot basename are sensitive metadata.
	log.Printf("security_event=snapshot_create result=success")
	return snapshotPath, nil
}

// ListSnapshots は利用可能なスナップショットのリストを返す
func ListSnapshots(snapshotDir string) ([]string, error) {
	return defaultInstance.ListSnapshots(snapshotDir)
}

// ListSnapshots returns snapshots belonging to this instance's default
// snapshot directory, or to snapshotDir when explicitly provided.
func (i *Instance) ListSnapshots(snapshotDir string) ([]string, error) {
	if i == nil {
		return nil, fmt.Errorf("データベースinstanceが初期化されていません")
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	if snapshotDir == "" {
		snapshotDir = i.getSnapshotDir()
	}

	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("スナップショット一覧取得エラー: %w", err)
	}

	var snapshots []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if err := validateSnapshotName(entry.Name()); err != nil {
			continue
		}
		info, err := os.Lstat(filepath.Join(snapshotDir, entry.Name()))
		if err != nil || !validSnapshotFile(info) || !validSnapshotMode(info, i.opener != nil && i.opener.Encrypted()) {
			// Listing is intentionally fail-closed per entry: a stray symlink,
			// hard link, or non-regular file must never become a restore target.
			continue
		}
		snapshots = append(snapshots, entry.Name())
	}
	sort.Strings(snapshots)
	return snapshots, nil
}

// RestoreSnapshot はスナップショットからDBを復元する。
//
// 手順:
//  1. DB接続を完全に遮断（Close + nil化）
//  2. 現在のDBファイルを .bak に退避
//  3. SQLite WAL/SHM 一時ファイルを消去
//  4. スナップショットファイルを元のDBパスにコピー
//  5. 復元候補へ現行スキーマ・index・triggerを再適用
//  6. PRAGMA integrity_check で整合性を検証
//  7. 成功なら退避ファイルを削除、失敗なら退避から復旧
func RestoreSnapshot(snapshotDir, snapshotName string) error {
	return defaultInstance.RestoreSnapshot(snapshotDir, snapshotName)
}

// RestoreSnapshot replaces this instance's database with a validated
// snapshot and reopens it with the same opener (and therefore the same key).
//
// The snapshot is first copied to a private, randomly named candidate in the
// live database directory.  The candidate is opened with the same SQLCipher
// opener, migrated, integrity checked, and fsynced before the live file is
// touched.  Only after that validation does the lifecycle lock permit an
// atomic rename swap.  A failed reopen rolls the old file back into place.
func (i *Instance) RestoreSnapshot(snapshotDir, snapshotName string) error {
	if i == nil {
		return fmt.Errorf("データベースinstanceが初期化されていません")
	}
	// スナップショット名の検証（パストラバーサル防止）。
	// APIから任意の名前が渡り得るため、ディレクトリ区切りや ".." を含む名前、
	// snapshots/ 直下の .db ファイル以外は拒否する。
	if err := validateSnapshotName(snapshotName); err != nil {
		return err
	}

	i.beginDBLifecycle()
	defer i.endDBLifecycle()

	// 復元中に他のリクエストが nil の DB 接続へアクセスして panic しないよう、
	// ファイル差し替えと再接続が終わるまでロックを保持し続ける。
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.path == "" || i.db == nil || i.opener == nil {
		return fmt.Errorf("データベースが初期化されていません")
	}
	currentPath := i.path
	if snapshotDir == "" {
		snapshotDir = filepath.Join(filepath.Dir(currentPath), "snapshots")
	}
	if err := validateSnapshotDirectory(snapshotDir); err != nil {
		return fmt.Errorf("スナップショットディレクトリが安全ではありません: %w", err)
	}
	snapshotPath := filepath.Join(snapshotDir, snapshotName)
	snapshotInfo, err := validateSnapshotSource(snapshotPath, snapshotDir, snapshotName, i.opener.Encrypted())
	if err != nil {
		return err
	}

	// Validate an isolated candidate while the live database remains open.
	dir := filepath.Dir(currentPath)
	candidatePath, candidateFile, err := temporaryDatabaseFile(dir, ".omni-money-restore-candidate-")
	if err != nil {
		return fmt.Errorf("復元候補を作成できません: %w", err)
	}
	removeCandidate := true
	defer func() {
		if candidateFile != nil {
			_ = candidateFile.Close()
		}
		if removeCandidate {
			_ = removeSQLiteFiles(candidatePath)
		}
	}()
	snapshotFile, err := openSnapshotFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("スナップショットを開けません: %w", err)
	}
	defer snapshotFile.Close()
	info, err := snapshotFile.Stat()
	if err != nil || !validSnapshotFile(info) || !validSnapshotMode(info, i.opener.Encrypted()) ||
		!snapshotSourceMatches(snapshotInfo, info) {
		if err == nil {
			err = errors.New("スナップショットが検査後に置き換えられました")
		}
		return fmt.Errorf("スナップショットfd検証エラー: %w", err)
	}
	if err := copyFileToOpen(snapshotFile, candidateFile); err != nil {
		return fmt.Errorf("スナップショット候補コピーエラー: %w", err)
	}
	if err := candidateFile.Sync(); err != nil {
		return fmt.Errorf("復元候補のsyncエラー: %w", err)
	}
	if err := candidateFile.Close(); err != nil {
		return fmt.Errorf("復元候補のクローズエラー: %w", err)
	}
	candidateFile = nil
	candidateDB, err := i.opener.Open(context.Background(), candidatePath, securedb.Writable)
	if err != nil {
		return fmt.Errorf("復元候補のDB接続エラー: %w", err)
	}
	if err := i.validateRestoreDatabase(candidateDB, candidatePath); err != nil {
		_ = candidateDB.Close()
		return err
	}
	if err := checkpointAndClose(candidateDB, candidatePath); err != nil {
		return fmt.Errorf("復元候補の耐久化エラー: %w", err)
	}
	if err := syncFileAndDirectory(candidatePath, dir); err != nil {
		return fmt.Errorf("復元候補のfsyncエラー: %w", err)
	}

	// Close the live handle only after candidate validation.  Its WAL is
	// checkpointed first so the renamed live file is a complete database.
	if _, err := i.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("現行DBのcheckpointエラー: %w", err)
	}
	if err := i.db.Close(); err != nil {
		return fmt.Errorf("現行DBのクローズエラー: %w", err)
	}
	i.db = nil
	if err := removeSQLiteSidecars(currentPath); err != nil {
		// The live handle is intentionally not republished after a post-close
		// filesystem failure. The vault manager still owns the drained entry and
		// will close it fail-closed; a later fresh acquire can reopen the intact
		// live file after the failed operation has been fully released.
		return fmt.Errorf("現行DB一時ファイル削除エラー: %w", err)
	}

	backupPath, err := randomDatabasePath(dir, ".omni-money-restore-backup-")
	if err != nil {
		return fmt.Errorf("現行DB退避先を作成できません: %w", err)
	}
	if err := os.Rename(currentPath, backupPath); err != nil {
		return fmt.Errorf("現行DB退避エラー: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return errors.Join(fmt.Errorf("現行DB退避のfsyncエラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}

	if err := os.Rename(candidatePath, currentPath); err != nil {
		return errors.Join(fmt.Errorf("復元候補の配置エラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}
	if err := syncDirectory(dir); err != nil {
		return errors.Join(fmt.Errorf("復元配置のfsyncエラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}

	newDB, err := i.opener.Open(context.Background(), currentPath, securedb.Writable)
	if err == nil {
		err = i.validateRestoreDatabase(newDB, currentPath)
	}
	if err != nil {
		if newDB != nil {
			_ = newDB.Close()
		}
		return errors.Join(fmt.Errorf("復元後DB検証エラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}
	if err := checkpointAndClose(newDB, currentPath); err != nil {
		_ = newDB.Close()
		return errors.Join(fmt.Errorf("復元後DB耐久化エラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}
	// Reopen once more after checkpointing so i.db never references a handle
	// whose pager state predates the final durable candidate.
	newDB, err = i.opener.Open(context.Background(), currentPath, securedb.Writable)
	if err == nil {
		err = i.validateRestoreDatabase(newDB, currentPath)
	}
	if err != nil {
		if newDB != nil {
			_ = newDB.Close()
		}
		return errors.Join(fmt.Errorf("復元後DB再検証エラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}
	i.db = newDB
	removeCandidate = false
	_ = os.Remove(backupPath)
	_ = syncDirectory(dir)
	log.Printf("snapshot_restore result=success")
	return nil
}

func (i *Instance) validateRestoreDatabase(target *sql.DB, path string) error {
	if target == nil {
		return fmt.Errorf("復元候補DBが初期化されていません")
	}
	if err := requireFullSynchronous(target); err != nil {
		return fmt.Errorf("復元DB耐久性設定エラー: %w", err)
	}
	if err := i.checkIntegrity(target); err != nil {
		return err
	}
	if err := createTablesOn(target); err != nil {
		return fmt.Errorf("復元DBスキーマ更新エラー: %w", err)
	}
	if err := i.checkIntegrity(target); err != nil {
		return err
	}
	if i.opener != nil && i.opener.Encrypted() {
		if err := securedb.RequireEncryptedHeader(path); err != nil {
			return fmt.Errorf("復元DB暗号化検証エラー: %w", err)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("復元DB権限設定エラー: %w", err)
	}
	return nil
}

func checkpointAndClose(target *sql.DB, path string) error {
	if target == nil {
		return fmt.Errorf("DBが初期化されていません")
	}
	if _, err := target.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}

func (i *Instance) reopenAfterRestoreFailure(path string) error {
	if i.opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	info, err := os.Lstat(path)
	if err != nil {
		i.db = nil
		return fmt.Errorf("復旧対象DBを検査できません: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		i.db = nil
		return errors.New("復旧対象DBが安全な通常ファイルではありません")
	}
	db, err := i.opener.Open(context.Background(), path, securedb.Writable)
	if err != nil {
		i.db = nil
		return err
	}
	if err := i.validateRestoreDatabase(db, path); err != nil {
		_ = db.Close()
		i.db = nil
		return err
	}
	i.db = db
	return nil
}

func writableSQLiteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, RawQuery: writableSQLiteQuery}
	return u.String()
}

func snapshotSQLiteDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path, RawQuery: snapshotSQLiteQuery}
	return u.String()
}

func requireFullSynchronous(target *sql.DB) error {
	var synchronous int
	if err := target.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("PRAGMA synchronous検査エラー: %w", err)
	}
	if synchronous != 2 {
		return fmt.Errorf("PRAGMA synchronous=%d; FULL (2) が必要です", synchronous)
	}
	return nil
}

func (i *Instance) checkIntegrity(target *sql.DB) error {
	if i.opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	if err := i.opener.CheckIntegrity(context.Background(), target); err != nil {
		return fmt.Errorf("整合性チェック失敗: %w", err)
	}
	return nil
}

// checkIntegrity preserves the package-internal compatibility path used by
// the legacy default-instance tests.
func checkIntegrity(target *sql.DB) error {
	return defaultInstance.checkIntegrity(target)
}

// validateSnapshotName はスナップショット名として安全な形式かを検証する
func validateSnapshotName(name string) error {
	if name == "" {
		return fmt.Errorf("スナップショット名が必要です")
	}
	if name != filepath.Base(name) ||
		strings.ContainsAny(name, `/\`) ||
		strings.Contains(name, "..") ||
		!strings.HasSuffix(name, ".db") {
		return fmt.Errorf("スナップショット名が不正です")
	}
	return nil
}

// ValidateSnapshotName exposes the same strict basename validation used by
// restore without exposing a database path or instance to HTTP callers.
func ValidateSnapshotName(name string) error { return validateSnapshotName(name) }

// CleanOldSnapshots は古いスナップショットを削除する（世代管理: 最新N件を残す）
func CleanOldSnapshots(snapshotDir string, maxKeep int) error {
	return defaultInstance.CleanOldSnapshots(snapshotDir, maxKeep)
}

// CleanOldSnapshots applies generation and total-size retention to this
// instance's snapshots.
func (i *Instance) CleanOldSnapshots(snapshotDir string, maxKeep int) error {
	if i == nil {
		return fmt.Errorf("データベースinstanceが初期化されていません")
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	return i.cleanOldSnapshots(snapshotDir, maxKeep)
}

func (i *Instance) cleanOldSnapshots(snapshotDir string, maxKeep int) error {
	if snapshotDir == "" {
		i.mu.RLock()
		path := i.path
		i.mu.RUnlock()
		snapshotDir = filepath.Join(filepath.Dir(path), "snapshots")
	}
	if maxKeep <= 0 {
		maxKeep = 30
	}
	budget, err := snapshotMaxTotalBytes()
	if err != nil {
		return err
	}
	return pruneSnapshots(snapshotDir, maxKeep, budget, "")
}

func snapshotMaxTotalBytes() (int64, error) {
	raw := strings.TrimSpace(os.Getenv("SNAPSHOT_MAX_TOTAL_BYTES"))
	if raw == "" {
		return defaultSnapshotMaxTotalBytes, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("SNAPSHOT_MAX_TOTAL_BYTES は正の整数で指定してください")
	}
	return value, nil
}

func sqliteDatabaseSize(target *sql.DB) (int64, error) {
	var pageCount, pageSize int64
	if err := target.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := target.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, err
	}
	if pageCount <= 0 || pageSize <= 0 || pageCount > (1<<63-1)/pageSize {
		return 0, fmt.Errorf("SQLite page size is invalid")
	}
	return pageCount * pageSize, nil
}

type snapshotUsageEntry struct {
	name string
	path string
	size int64
}

func pruneSnapshots(snapshotDir string, maxKeep int, maxBytes int64, protectedPath string) error {
	if maxKeep <= 0 || maxBytes <= 0 {
		return fmt.Errorf("スナップショット保持境界が無効です")
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	usage := make([]snapshotUsageEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !validSnapshotFile(info) {
			return fmt.Errorf("スナップショットが通常ファイルではありません: %s", entry.Name())
		}
		if info.Size() < 0 || total > (1<<63-1)-info.Size() {
			return fmt.Errorf("スナップショット容量が整数上限を超えました")
		}
		total += info.Size()
		usage = append(usage, snapshotUsageEntry{name: entry.Name(), path: path, size: info.Size()})
	}
	sort.Slice(usage, func(i, j int) bool { return usage[i].name < usage[j].name })
	for len(usage) > maxKeep || total > maxBytes {
		candidate := -1
		for index := range usage {
			if usage[index].path != protectedPath {
				candidate = index
				break
			}
		}
		if candidate < 0 {
			return fmt.Errorf("保護対象を残したまま容量上限を満たせません")
		}
		victim := usage[candidate]
		if err := os.Remove(victim.path); err != nil {
			return fmt.Errorf("古いスナップショット削除エラー (%s): %w", victim.name, err)
		}
		total -= victim.size
		usage = append(usage[:candidate], usage[candidate+1:]...)
		log.Printf("security_event=snapshot_prune result=success remaining_bytes=%d", total)
	}
	return nil
}

// AutoSnapshot は操作ごとに自動スナップショットを作成し、30世代を維持する
func AutoSnapshot() {
	defaultInstance.StartAutoSnapshot()
}

// StartAutoSnapshot schedules an asynchronous snapshot for this instance.
// Bursts are coalesced into at most one follow-up run.
func (i *Instance) StartAutoSnapshot() {
	if i == nil {
		return
	}
	i.ensureSnapshotCond()
	i.snapshotMu.Lock()
	defer i.snapshotMu.Unlock()
	if i.snapshotClosing {
		return
	}
	i.mu.RLock()
	ready := i.db != nil && i.opener != nil
	i.mu.RUnlock()
	if !ready {
		return
	}
	if i.snapshotRunning {
		// Coalesce bursts of writes into one follow-up snapshot.  This avoids
		// spawning an unbounded number of goroutines while preserving the
		// asynchronous API expected by callers.
		i.snapshotPending = true
		return
	}
	i.snapshotRunning = true
	go i.runAutoSnapshots()
}

func (i *Instance) runAutoSnapshots() {
	for {
		i.snapshotLifecycle.RLock()
		_, err := i.createSnapshot("")
		if err != nil {
			log.Printf("security_event=snapshot_create result=error")
		} else if err := i.cleanOldSnapshots("", 30); err != nil {
			log.Printf("security_event=snapshot_prune result=error")
		}
		i.snapshotLifecycle.RUnlock()

		i.snapshotMu.Lock()
		if i.snapshotPending && !i.snapshotClosing {
			i.snapshotPending = false
			i.snapshotMu.Unlock()
			continue
		}
		i.snapshotPending = false
		i.snapshotRunning = false
		i.snapshotCond.Broadcast()
		i.snapshotMu.Unlock()
		return
	}
}

// beginDBLifecycle prevents new automatic snapshots and waits for an already
// scheduled worker to finish before a database is closed or replaced.  The
// lifecycle lock also waits for direct CreateSnapshot calls that are already
// in progress.
func (i *Instance) beginDBLifecycle() {
	i.ensureSnapshotCond()
	// Serialize lifecycle transitions themselves.  Without this outer mutex,
	// two concurrent CloseDB/InitDB calls could both observe the closing state,
	// and the first one to finish could reopen the window while the second still
	// owns the database lifecycle lock.
	i.dbLifecycleMu.Lock()

	i.snapshotMu.Lock()
	i.snapshotClosing = true
	for i.snapshotRunning {
		i.snapshotCond.Wait()
	}
	i.snapshotMu.Unlock()
	i.snapshotLifecycle.Lock()
}

func (i *Instance) endDBLifecycle() {
	i.snapshotLifecycle.Unlock()
	i.snapshotMu.Lock()
	i.snapshotClosing = false
	i.snapshotCond.Broadcast()
	i.snapshotMu.Unlock()
	i.dbLifecycleMu.Unlock()
}

func ensurePrivateDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("ディレクトリではありません")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700) // #nosec G302 -- newly created financial-data directories are owner-only.
}

func preparePrivateDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		file, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- path is the configured database path and O_EXCL rejects existing symlinks.
		if createErr != nil {
			return createErr
		}
		return file.Close()
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("データベースパスが通常ファイルではありません")
	}
	return os.Chmod(path, 0600)
}

func hardenSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		info, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("SQLite関連パスが通常ファイルではありません: %s", candidate)
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return err
		}
	}
	return nil
}

// backupSQLiteDatabase はSQLiteのオンラインBackup APIで一貫した複製を作る。
func backupSQLiteDatabase(opener *securedb.Opener, source *sql.DB, snapshotPath string) error {
	if opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return opener.Backup(ctx, source, snapshotPath)
}

// copyFile はファイルをコピーする
func copyFile(src, dst string) error {
	sourceInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("コピー元が通常ファイルではありません")
	}
	in, err := os.Open(src) // #nosec G304 -- src is the configured DB or validated snapshot path.
	if err != nil {
		return err
	}
	defer in.Close()

	// Snapshot and restored database files contain sensitive financial data.
	// O_EXCL prevents an existing file or symlink from being followed.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- dst is generated under the configured DB/snapshot directory.
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		_ = out.Close()
		if !completed {
			_ = os.Remove(dst)
		}
	}()
	if err := out.Chmod(0600); err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	completed = true
	return nil
}

func validateSnapshotDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("スナップショットディレクトリは通常のdirectoryである必要があります")
	}
	if info.Mode().Perm() != 0700 {
		return fmt.Errorf("スナップショットディレクトリ権限は0700が必要です")
	}
	return nil
}

func validateSnapshotSource(path, dir, name string, encrypted ...bool) (os.FileInfo, error) {
	if err := validateSnapshotName(name); err != nil {
		return nil, err
	}
	if filepath.Dir(filepath.Clean(path)) != filepath.Clean(dir) {
		return nil, fmt.Errorf("スナップショットパスがdirectoryから逸脱しています")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("スナップショットが見つかりません: %s", name)
		}
		return nil, fmt.Errorf("スナップショット検査エラー: %w", err)
	}
	if !validSnapshotFile(info) || (len(encrypted) > 0 && !validSnapshotMode(info, encrypted[0])) {
		return nil, fmt.Errorf("スナップショットが安全な通常ファイルではありません")
	}
	return info, nil
}

func validSnapshotMode(info os.FileInfo, encrypted bool) bool {
	if info == nil {
		return false
	}
	if encrypted {
		return info.Mode().Perm() == 0600
	}
	// Desktop migration may encounter read-only legacy snapshots. They cannot
	// be modified by another user, while group/other write access is rejected.
	return info.Mode().Perm()&0022 == 0
}

// snapshotSourceMatches binds the descriptor used for the copy to the
// metadata inspected before opening it.  A same-name replacement between
// Lstat and open is therefore rejected instead of silently copying a
// different file.  Size is checked as well because a writer can mutate a
// regular file without changing its inode.
func snapshotSourceMatches(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) && expected.Size() == actual.Size()
}

func temporaryDatabaseFile(dir, prefix string) (string, *os.File, error) {
	file, err := os.CreateTemp(dir, prefix+"*.db")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", nil, err
	}
	// Keep this descriptor open until the caller has populated or atomically
	// replaced the reserved pathname. Closing and unlinking here would create a
	// TOCTOU window in which another process could plant a symlink.
	return path, file, nil
}

func randomDatabasePath(dir, prefix string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 16)
		if _, err := cryptorand.Read(random); err != nil {
			return "", err
		}
		path := filepath.Join(dir, prefix+hex.EncodeToString(random)+".db")
		clear(random)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			// The name is unguessable and the directory is private. Unlike a
			// create-close-remove reservation this also works with Windows rename,
			// which rejects replacing an existing destination.
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("一意な復元退避先を生成できません")
}

func copyFileToOpen(src, dst *os.File) error {
	if src == nil || dst == nil {
		return errors.New("コピー元またはコピー先が開かれていません")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := dst.Truncate(0); err != nil {
		return err
	}
	_, err := io.Copy(dst, src)
	return err
}

func removeSQLiteFiles(path string) error {
	var errs []error
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removeSQLiteSidecars(path string) error {
	var errs []error
	for _, candidate := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func syncFileAndDirectory(path, dir string) error {
	file, err := os.Open(path) // #nosec G304 -- path is a generated candidate in the private DB directory.
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func rollbackRestoreFiles(currentPath, backupPath, candidatePath string, instance *Instance) error {
	dir := filepath.Dir(currentPath)
	var errs []error
	if err := removeSQLiteFiles(currentPath); err != nil {
		errs = append(errs, fmt.Errorf("remove failed restore candidate: %w", err))
	}
	if err := removeSQLiteFiles(candidatePath); err != nil {
		errs = append(errs, fmt.Errorf("remove restore candidate: %w", err))
	}
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Rename(backupPath, currentPath); err != nil {
			errs = append(errs, fmt.Errorf("restore backup rename: %w", err))
		}
	} else if os.IsNotExist(err) {
		// Never turn a missing rollback image into a newly-created empty
		// plaintext/SQLCipher database during recovery.
		errs = append(errs, fmt.Errorf("restore backup is missing: %w", os.ErrNotExist))
	} else {
		errs = append(errs, fmt.Errorf("inspect restore backup: %w", err))
	}
	if err := syncDirectory(dir); err != nil {
		errs = append(errs, fmt.Errorf("restore rollback directory sync: %w", err))
	}
	if len(errs) != 0 {
		// Do not reopen a potentially mixed or missing file.  The manager keeps
		// the vault drained and the caller can surface a fail-closed error.
		return errors.Join(errs...)
	}
	if instance != nil {
		if err := instance.reopenAfterRestoreFailure(currentPath); err != nil {
			errs = append(errs, fmt.Errorf("rollback reopen: %w", err))
		}
	}
	return errors.Join(errs...)
}
