// Package database はSQLite接続、初期化、スナップショット機能を提供する
package database

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

	"omni_money/backend/fileprivacy"
	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/validation"
)

const defaultSnapshotMaxTotalBytes int64 = 2 * 1024 * 1024 * 1024
const ledgerSchemaVersion = 5

// application_id is the SQLite file identity for Omni Money ledgers. Zero is
// accepted only for legacy files that predate the identity marker; another
// non-zero identity is never treated as a ledger merely because it has a
// familiar table name.
const ledgerSchemaIdentity = 0x4f4d4e59

const writableSQLiteQuery = "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=FULL"
const snapshotSQLiteQuery = "mode=rw&_busy_timeout=5000&_foreign_keys=ON&_synchronous=FULL"

// Validation copies are bounded before they are opened.  The retention
// budget is the upper bound for an individual snapshot in normal operation;
// keeping the same bound here prevents a hostile off-host file from turning a
// GET /snapshots into an unbounded disk/CPU operation.
const maxSnapshotValidationBytes int64 = defaultSnapshotMaxTotalBytes

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
	// snapshotValidationSem bounds expensive snapshot authentication per
	// instance. A server user cannot turn one GET into unbounded concurrent
	// SQLCipher opens by issuing parallel requests.
	snapshotValidationSem chan struct{}
}

const maxSnapshotValidationEntries = 256

// A single list request is capped at one maximum-size snapshot. This keeps
// validation work bounded even when a vault contains the maximum number of
// large off-host files, while still allowing the largest snapshot that
// CreateSnapshot can retain to be listed and restored.
const maxSnapshotValidationWorkBytes int64 = maxSnapshotValidationBytes
const restoreManifestVersion = 1

type restoreManifest struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	Current   string `json:"current"`
	Backup    string `json:"backup"`
	Candidate string `json:"candidate"`
	OldDigest string `json:"old_digest"`
	NewDigest string `json:"new_digest"`
}

func newInstance() *Instance {
	instance := &Instance{}
	instance.ensureSnapshotCond()
	return instance
}

func (i *Instance) ensureSnapshotCond() {
	i.snapshotInit.Do(func() {
		i.snapshotCond = sync.NewCond(&i.snapshotMu)
		i.snapshotValidationSem = make(chan struct{}, 1)
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
	// Restore swaps are journaled before the live pathname is touched. Recover
	// that journal before EnsureEncrypted/preparePrivateDatabaseFile: those
	// open paths are allowed to create a fresh file, which would otherwise hide
	// a crash that left the live pathname absent and cause the only valid old
	// copy to be deleted as an "artifact".
	if err := i.recoverRestoreState(path); err != nil {
		return fmt.Errorf("restore crash recovery failed: %w", err)
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
	if err := validateLedgerSchema(target, false); err != nil {
		return fmt.Errorf("schema identity/minimum validation failed: %w", err)
	}
	var identity int64
	if err := target.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		return fmt.Errorf("スキーマidentity取得エラー: %w", err)
	}
	var userTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return fmt.Errorf("schema table count取得エラー: %w", err)
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
	// A blank new database receives the identity marker. Legacy databases keep
	// application_id=0 so their explicitly supported pre-marker layout can be
	// reopened after migration without pretending that old table definitions
	// had constraints that SQLite cannot add with CREATE IF NOT EXISTS.
	if identity == ledgerSchemaIdentity || userTables == 0 {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id = %d", ledgerSchemaIdentity)); err != nil {
			return fmt.Errorf("スキーマidentity更新エラー: %w", err)
		}
	}
	if err := validateCriticalSchema(tx); err != nil {
		return err
	}
	// Validate the complete post-migration schema while the transaction is
	// still open. A crafted extra table/index/constraint must not leave a
	// partially upgraded database committed after validation reports failure.
	if err := validateLedgerSchemaAfterMigration(tx, version == ledgerSchemaVersion || identity == ledgerSchemaIdentity); err != nil {
		return fmt.Errorf("完全なschema validation failed: %w", err)
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

// validateLedgerSchema validates both the pre-migration trust boundary and
// the complete current schema. requireCurrent is false for read-only listing:
// an explicitly supported legacy version may be shown, but it still needs a
// recognizable ledger identity/minimum shape and is never migrated there.
func validateLedgerSchema(target schemaQueryer, requireCurrent bool) error {
	return validateLedgerSchemaInternal(target, requireCurrent, true)
}

func validateLedgerSchemaInternal(target schemaQueryer, requireCurrent, strictCurrent bool) error {
	if target == nil {
		return errors.New("database schema target is nil")
	}
	var version int
	if err := target.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > ledgerSchemaVersion {
		return fmt.Errorf("unsupported ledger schema version %d", version)
	}
	var identity int64
	if err := target.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		return fmt.Errorf("read ledger identity: %w", err)
	}
	if identity != 0 && identity != ledgerSchemaIdentity {
		return fmt.Errorf("database identity %08x is not an Omni Money ledger", identity)
	}
	var userTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return err
	}
	if userTables == 0 {
		if requireCurrent && version != 0 {
			return fmt.Errorf("empty database has schema version %d", version)
		}
		return nil
	}
	// Before migration, require the historical transaction shape. This keeps a
	// random SQLite file from being upgraded by CREATE TABLE IF NOT EXISTS.
	if err := requireColumns(target, "transactions", []string{"id", "account", "date", "item", "type", "amount", "balance", "memo"}); err != nil {
		return fmt.Errorf("ledger minimum schema: %w", err)
	}
	if version == ledgerSchemaVersion {
		strict := strictCurrent
		if strictCurrent && identity == 0 {
			// application_id=0 is never a compatibility marker. A historical
			// image is accepted only when every legacy table definition matches
			// the exact allowlisted pre-identity DDL and all current objects have
			// their expected shape. Same-name forged tables/markers do not pass.
			legacy, err := isSupportedLegacyCurrentLayout(target)
			if err != nil {
				return err
			}
			strict = !legacy
		}
		return validateFullLedgerSchema(target, strict)
	}
	if !requireCurrent {
		return nil
	}
	if version != ledgerSchemaVersion {
		return fmt.Errorf("schema version %d was not migrated", version)
	}
	return validateFullLedgerSchema(target, strictCurrent)
}

func validateLedgerSchemaAfterMigration(target schemaQueryer, strict bool) error {
	return validateLedgerSchemaInternal(target, true, strict)
}

func canonicalSQL(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

// canonicalDDL removes only formatting which SQLite itself is free to add
// around punctuation when it stores sqlite_master.sql. Unlike substring
// checks, equality against these fingerprints cannot be bypassed with a
// harmless-looking token such as "WHEN 0" or "OR 1".
func canonicalDDL(value string) string {
	value = canonicalSQL(value)
	value = strings.ReplaceAll(value, "( ", "(")
	value = strings.ReplaceAll(value, " )", ")")
	value = strings.ReplaceAll(value, " ,", ",")
	value = strings.ReplaceAll(value, ", ", ",")
	return value
}

func expectedLedgerTableDefinitions() map[string]string {
	return map[string]string{
		"transactions": canonicalDDL(`CREATE TABLE transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account TEXT NOT NULL,
			date DATETIME NOT NULL,
			item TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('income', 'expense')),
			amount INTEGER NOT NULL CHECK(amount BETWEEN 1 AND 1000000000),
			balance INTEGER NOT NULL DEFAULT 0,
			memo TEXT DEFAULT ''
		)`),
		"transaction_links": canonicalDDL(`CREATE TABLE transaction_links (
			parent_id INTEGER NOT NULL,
			child_id INTEGER NOT NULL,
			PRIMARY KEY (parent_id, child_id),
			FOREIGN KEY (parent_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (child_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`),
		"transaction_images": canonicalDDL(`CREATE TABLE transaction_images (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			data BLOB NOT NULL,
			mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`),
		"tags": canonicalDDL(`CREATE TABLE tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER DEFAULT NULL,
			level INTEGER NOT NULL DEFAULT 1 CHECK(level IN (1, 2, 3)),
			FOREIGN KEY (parent_id) REFERENCES tags(id) ON DELETE CASCADE,
			UNIQUE(name, parent_id)
		)`),
		"transaction_tags": canonicalDDL(`CREATE TABLE transaction_tags (
			transaction_id INTEGER NOT NULL,
			tag_id INTEGER NOT NULL,
			PRIMARY KEY (transaction_id, tag_id),
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
		)`),
		"ai_transaction_idempotency": canonicalDDL(`CREATE TABLE ai_transaction_idempotency (
			credential_id TEXT NOT NULL,
			idempotency_key_sha256 BLOB NOT NULL CHECK(length(idempotency_key_sha256) = 32),
			request_sha256 BLOB NOT NULL CHECK(length(request_sha256) = 32),
			transaction_id INTEGER,
			response_account TEXT,
			response_date TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (credential_id, idempotency_key_sha256),
			UNIQUE (transaction_id),
			CHECK ((transaction_id IS NULL AND response_account IS NULL AND response_date IS NULL) OR (transaction_id IS NOT NULL AND response_account IS NOT NULL AND response_date IS NOT NULL))
		)`),
		"ai_daily_transaction_usage": canonicalDDL(`CREATE TABLE ai_daily_transaction_usage (
			credential_id TEXT NOT NULL,
			utc_date TEXT NOT NULL CHECK(length(utc_date) = 10),
			successful_creates INTEGER NOT NULL CHECK(successful_creates >= 0),
			PRIMARY KEY (credential_id, utc_date)
		)`),
		"settings": canonicalDDL(`CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`),
	}
}

// isSupportedLegacyCurrentLayout recognizes the one historical layout that
// can be migrated without pretending its old tables had today's constraints.
// Recognition is based on immutable DDL fingerprints, not a mutable marker
// row/table inside the database. The migrated current objects are validated
// separately by validateFullLedgerSchema.
func isSupportedLegacyCurrentLayout(target schemaQueryer) (bool, error) {
	var markerCount int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='omni_legacy_schema_compat'").Scan(&markerCount); err != nil {
		return false, err
	}
	if markerCount != 0 {
		// The former marker is deliberately not trusted. A forged same-name
		// object must force strict current validation, never grant a bypass.
		return false, nil
	}
	legacyDefinitions := map[string]string{
		"transactions":       "create table transactions ( id integer primary key autoincrement, account text not null, date datetime not null, item text not null, type text not null, amount integer not null, balance integer not null default 0, memo text default '' )",
		"transaction_images": "create table transaction_images ( id integer primary key autoincrement, transaction_id integer not null, filename text not null, data blob not null, mime_type text not null default 'image/jpeg', created_at datetime default current_timestamp )",
	}
	for table, expected := range legacyDefinitions {
		var definition sql.NullString
		if err := target.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil || !definition.Valid {
			return false, nil
		}
		if canonicalSQL(definition.String) != expected {
			return false, nil
		}
	}
	// The old image table must not have foreign keys or hidden extra columns;
	// those would be a different migration family.
	for _, table := range []string{"transactions", "transaction_images"} {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			return false, err
		}
	}
	return true, nil
}

func requireColumns(target schemaQueryer, table string, required []string) error {
	rows, err := target.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(required))
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range required {
		if !found[column] {
			return fmt.Errorf("missing %s.%s", table, column)
		}
	}
	return nil
}

func validateFullLedgerSchema(target schemaQueryer, strictConstraints bool) error {
	allowedTables := map[string]bool{
		"transactions": true, "transaction_links": true, "transaction_images": true,
		"tags": true, "transaction_tags": true, "ai_transaction_idempotency": true,
		"ai_daily_transaction_usage": true, "settings": true,
	}
	rows, err := target.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		if !allowedTables[name] {
			_ = rows.Close()
			return fmt.Errorf("unexpected ledger table %s", name)
		}
		delete(allowedTables, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(allowedTables) != 0 {
		return errors.New("ledger is missing one or more required tables")
	}
	requiredColumns := map[string][]string{
		"transactions":               {"id", "account", "date", "item", "type", "amount", "balance", "memo"},
		"transaction_links":          {"parent_id", "child_id"},
		"transaction_images":         {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"tags":                       {"id", "name", "parent_id", "level"},
		"transaction_tags":           {"transaction_id", "tag_id"},
		"ai_transaction_idempotency": {"credential_id", "idempotency_key_sha256", "request_sha256", "transaction_id", "response_account", "response_date", "created_at"},
		"ai_daily_transaction_usage": {"credential_id", "utc_date", "successful_creates"},
		"settings":                   {"key", "value"},
	}
	for table, columns := range requiredColumns {
		if err := requireColumns(target, table, columns); err != nil {
			return fmt.Errorf("full schema: %w", err)
		}
	}
	objects := []struct{ typ, name string }{
		{"index", "idx_transactions_account"}, {"index", "idx_transactions_account_date_id"},
		{"index", "idx_transactions_date"}, {"index", "idx_transactions_item"}, {"index", "idx_transactions_memo"},
		{"index", "idx_transaction_links_child_id"}, {"index", "idx_transaction_images_txid"},
		{"index", "idx_tags_parent"}, {"index", "idx_tags_root_name_unique"},
		{"index", "idx_transaction_tags_txid"}, {"index", "idx_transaction_tags_tagid"},
		{"index", "idx_ai_idempotency_credential_key"}, {"index", "idx_ai_idempotency_transaction"},
		{"index", "idx_ai_daily_usage_credential_date"},
		{"trigger", "trg_transaction_images_quota_insert"}, {"trigger", "trg_transaction_images_immutable_update"},
		{"trigger", "validate_transactions_amount_insert"}, {"trigger", "validate_transactions_amount_update"},
	}
	for _, object := range objects {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?", object.typ, object.name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("missing required %s %s", object.typ, object.name)
		}
	}
	for table, indexes := range map[string][]string{
		"transactions":               {"idx_transactions_account", "idx_transactions_account_date_id", "idx_transactions_date", "idx_transactions_item", "idx_transactions_memo"},
		"transaction_links":          {"idx_transaction_links_child_id"},
		"transaction_images":         {"idx_transaction_images_txid"},
		"tags":                       {"idx_tags_parent", "idx_tags_root_name_unique"},
		"transaction_tags":           {"idx_transaction_tags_txid", "idx_transaction_tags_tagid"},
		"ai_transaction_idempotency": {"idx_ai_idempotency_credential_key", "idx_ai_idempotency_transaction"},
		"ai_daily_transaction_usage": {"idx_ai_daily_usage_credential_date"},
	} {
		for _, index := range indexes {
			var tableName string
			if err := target.QueryRow("SELECT tbl_name FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&tableName); err != nil || tableName != table {
				return fmt.Errorf("index %s is not attached to %s", index, table)
			}
		}
	}
	if err := validateRootTagIndex(target); err != nil {
		return err
	}
	if err := validateIndexShapes(target); err != nil {
		return err
	}
	if err := validateTriggerDefinitions(target); err != nil {
		return err
	}
	if err := validateCurrentColumnDefinitions(target, !strictConstraints); err != nil {
		return err
	}
	// Check the complete canonical table DDL. Substring checks are not a
	// security boundary: an attacker can retain a token while replacing a
	// CHECK/FOREIGN KEY with a weaker expression. Legacy transaction tables
	// are the only intentionally relaxed objects during the supported
	// pre-identity migration; every other table remains exact.
	for table, expected := range expectedLedgerTableDefinitions() {
		if !strictConstraints && (table == "transactions" || table == "transaction_images") {
			continue
		}
		var definition sql.NullString
		if err := target.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil || !definition.Valid {
			return fmt.Errorf("missing definition for %s: %w", table, err)
		}
		if got := canonicalDDL(definition.String); got != expected {
			return fmt.Errorf("%s definition is not the current allowlisted DDL", table)
		}
	}
	type expectedForeignKey struct {
		referenced, from, to, onUpdate, onDelete string
	}
	expectedForeignKeys := map[string][]expectedForeignKey{
		"transaction_links": {
			{referenced: "transactions", from: "parent_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
			{referenced: "transactions", from: "child_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		},
		"transaction_images": {
			{referenced: "transactions", from: "transaction_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		},
		"tags": {
			{referenced: "tags", from: "parent_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		},
		"transaction_tags": {
			{referenced: "transactions", from: "transaction_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
			{referenced: "tags", from: "tag_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		},
	}
	for table, expected := range expectedForeignKeys {
		if !strictConstraints && table == "transaction_images" {
			continue
		}
		rows, err := target.Query("PRAGMA foreign_key_list(" + table + ")")
		if err != nil {
			return err
		}
		actual := make([]expectedForeignKey, 0, len(expected))
		for rows.Next() {
			var id, seq int
			var referenced, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &referenced, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				_ = rows.Close()
				return err
			}
			actual = append(actual, expectedForeignKey{referenced: referenced, from: from, to: to, onUpdate: strings.ToUpper(onUpdate), onDelete: strings.ToUpper(onDelete)})
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return fmt.Errorf("%s has %d expected foreign keys, found %d", table, len(expected), len(actual))
		}
		for _, want := range expected {
			found := false
			for _, got := range actual {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s has an unexpected foreign key", table)
			}
		}
	}
	rows, err = target.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign_key_check reported an orphan row")
	}
	return rows.Err()
}

type expectedColumnDefinition struct {
	name, columnType, defaultValue string
	notNull, primaryKey            int
}

func validateCurrentColumnDefinitions(target schemaQueryer, allowLegacyTables bool) error {
	expected := map[string][]expectedColumnDefinition{
		"transactions": {
			{name: "id", columnType: "INTEGER", primaryKey: 1},
			{name: "account", columnType: "TEXT", notNull: 1},
			{name: "date", columnType: "DATETIME", notNull: 1},
			{name: "item", columnType: "TEXT", notNull: 1},
			{name: "type", columnType: "TEXT", notNull: 1},
			{name: "amount", columnType: "INTEGER", notNull: 1},
			{name: "balance", columnType: "INTEGER", notNull: 1, defaultValue: "0"},
			{name: "memo", columnType: "TEXT", defaultValue: "''"},
		},
		"transaction_links": {
			{name: "parent_id", columnType: "INTEGER", notNull: 1, primaryKey: 1},
			{name: "child_id", columnType: "INTEGER", notNull: 1, primaryKey: 2},
		},
		"transaction_images": {
			{name: "id", columnType: "INTEGER", primaryKey: 1},
			{name: "transaction_id", columnType: "INTEGER", notNull: 1},
			{name: "filename", columnType: "TEXT", notNull: 1},
			{name: "data", columnType: "BLOB", notNull: 1},
			{name: "mime_type", columnType: "TEXT", notNull: 1, defaultValue: "'image/jpeg'"},
			{name: "created_at", columnType: "DATETIME", defaultValue: "current_timestamp"},
		},
		"tags": {
			{name: "id", columnType: "INTEGER", primaryKey: 1},
			{name: "name", columnType: "TEXT", notNull: 1},
			{name: "parent_id", columnType: "INTEGER", defaultValue: "null"},
			{name: "level", columnType: "INTEGER", notNull: 1, defaultValue: "1"},
		},
		"transaction_tags": {
			{name: "transaction_id", columnType: "INTEGER", notNull: 1, primaryKey: 1},
			{name: "tag_id", columnType: "INTEGER", notNull: 1, primaryKey: 2},
		},
		"settings": {
			{name: "key", columnType: "TEXT", primaryKey: 1},
			{name: "value", columnType: "TEXT", notNull: 1, defaultValue: "''"},
		},
		"ai_transaction_idempotency": {
			{name: "credential_id", columnType: "TEXT", notNull: 1, primaryKey: 1},
			{name: "idempotency_key_sha256", columnType: "BLOB", notNull: 1, primaryKey: 2},
			{name: "request_sha256", columnType: "BLOB", notNull: 1},
			{name: "transaction_id", columnType: "INTEGER"},
			{name: "response_account", columnType: "TEXT"},
			{name: "response_date", columnType: "TEXT"},
			{name: "created_at", columnType: "TEXT", notNull: 1},
		},
		"ai_daily_transaction_usage": {
			{name: "credential_id", columnType: "TEXT", notNull: 1, primaryKey: 1},
			{name: "utc_date", columnType: "TEXT", notNull: 1, primaryKey: 2},
			{name: "successful_creates", columnType: "INTEGER", notNull: 1},
		},
	}
	for table, want := range expected {
		if allowLegacyTables && (table == "transactions" || table == "transaction_images") {
			continue
		}
		rows, err := target.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		var index int
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return err
			}
			if index >= len(want) {
				_ = rows.Close()
				return fmt.Errorf("%s has unexpected extra columns", table)
			}
			definition := want[index]
			if cid != index || name != definition.name || strings.ToUpper(columnType) != definition.columnType || notNull != definition.notNull || primaryKey != definition.primaryKey || normalizeColumnDefault(defaultValue) != definition.defaultValue {
				_ = rows.Close()
				return fmt.Errorf("%s.%s column definition is not current", table, name)
			}
			index++
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if index != len(want) {
			return fmt.Errorf("%s is missing current columns", table)
		}
	}
	return nil
}

func validateTriggerDefinitions(target schemaQueryer) error {
	expected := map[string]string{
		"trg_transaction_images_quota_insert": canonicalDDL(fmt.Sprintf(`CREATE TRIGGER trg_transaction_images_quota_insert
			BEFORE INSERT ON transaction_images
			WHEN length(NEW.data) <= 0
				OR length(NEW.data) > %d
				OR (SELECT COUNT(*) FROM transaction_images WHERE transaction_id = NEW.transaction_id) >= %d
				OR COALESCE((SELECT SUM(length(data)) FROM transaction_images WHERE transaction_id = NEW.transaction_id), 0) + length(NEW.data) > %d
				OR COALESCE(( SELECT SUM(length(ti.data)) FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id) ), 0) + length(NEW.data) > %d
				OR COALESCE((SELECT SUM(length(data)) FROM transaction_images), 0) + length(NEW.data) > %d
			BEGIN
				SELECT RAISE(ABORT, 'image storage quota exceeded');
			END`, models.MaxImageBytes, models.MaxImagesPerTransaction, models.MaxImageBytesPerTransaction, models.MaxImageBytesPerAccount, models.MaxImageBytesDatabase)),
		"trg_transaction_images_immutable_update": canonicalDDL(`CREATE TRIGGER trg_transaction_images_immutable_update
			BEFORE UPDATE ON transaction_images
			BEGIN
				SELECT RAISE(ABORT, 'transaction images are immutable; delete and re-add the image');
			END`),
		"validate_transactions_amount_insert": canonicalDDL(fmt.Sprintf(`CREATE TRIGGER validate_transactions_amount_insert
			BEFORE INSERT ON transactions
			WHEN NEW.amount < 1 OR NEW.amount > %d
			BEGIN
				SELECT RAISE(ABORT, 'transaction amount out of range');
			END`, validation.MaxTransactionAmount)),
		"validate_transactions_amount_update": canonicalDDL(fmt.Sprintf(`CREATE TRIGGER validate_transactions_amount_update
			BEFORE UPDATE OF amount ON transactions
			WHEN NEW.amount < 1 OR NEW.amount > %d
			BEGIN
				SELECT RAISE(ABORT, 'transaction amount out of range');
			END`, validation.MaxTransactionAmount)),
	}
	for name, expectedDDL := range expected {
		var definition sql.NullString
		if err := target.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&definition); err != nil || !definition.Valid {
			return fmt.Errorf("trigger %s definition is unavailable: %w", name, err)
		}
		if got := canonicalDDL(definition.String); got != expectedDDL {
			return fmt.Errorf("trigger %s definition is not the current allowlisted DDL", name)
		}
	}
	return nil
}

func normalizeColumnDefault(value any) string {
	if value == nil {
		return ""
	}
	return canonicalSQL(fmt.Sprint(value))
}

func validateIndexShapes(target schemaQueryer) error {
	expected := map[string]struct {
		table  string
		unique bool
		cols   []string
	}{
		"idx_transactions_account":           {"transactions", false, []string{"account"}},
		"idx_transactions_account_date_id":   {"transactions", false, []string{"account", "date", "id"}},
		"idx_transactions_date":              {"transactions", false, []string{"date"}},
		"idx_transactions_item":              {"transactions", false, []string{"item"}},
		"idx_transactions_memo":              {"transactions", false, []string{"memo"}},
		"idx_transaction_links_child_id":     {"transaction_links", false, []string{"child_id"}},
		"idx_transaction_images_txid":        {"transaction_images", false, []string{"transaction_id"}},
		"idx_tags_parent":                    {"tags", false, []string{"parent_id"}},
		"idx_tags_root_name_unique":          {"tags", true, []string{"name"}},
		"idx_transaction_tags_txid":          {"transaction_tags", false, []string{"transaction_id"}},
		"idx_transaction_tags_tagid":         {"transaction_tags", false, []string{"tag_id"}},
		"idx_ai_idempotency_credential_key":  {"ai_transaction_idempotency", true, []string{"credential_id", "idempotency_key_sha256"}},
		"idx_ai_idempotency_transaction":     {"ai_transaction_idempotency", true, []string{"transaction_id"}},
		"idx_ai_daily_usage_credential_date": {"ai_daily_transaction_usage", true, []string{"credential_id", "utc_date"}},
	}
	for name, want := range expected {
		var tableName string
		if err := target.QueryRow("SELECT tbl_name FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&tableName); err != nil {
			return fmt.Errorf("index %s definition is unavailable: %w", name, err)
		}
		if tableName != want.table {
			return fmt.Errorf("index %s is attached to %s, want %s", name, tableName, want.table)
		}
		rows, err := target.Query("PRAGMA index_list(" + want.table + ")")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var seq, unique, partial int
			var indexName, origin string
			if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err != nil {
				_ = rows.Close()
				return err
			}
			if indexName == name {
				found = unique == boolToInt(want.unique)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("index %s has unexpected uniqueness", name)
		}
		rows, err = target.Query("PRAGMA index_info(" + strconv.Quote(name) + ")")
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var seq, cid int
			var column string
			if err := rows.Scan(&seq, &cid, &column); err != nil {
				_ = rows.Close()
				return err
			}
			columns = append(columns, column)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(columns) != len(want.cols) {
			return fmt.Errorf("index %s has unexpected column count", name)
		}
		for index := range want.cols {
			if columns[index] != want.cols[index] {
				return fmt.Errorf("index %s column order is invalid", name)
			}
		}
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
		// The implicit per-vault directory is owned by this application and can
		// safely be hardened in place. Explicit caller-selected directories are
		// validated below but are never silently chmod'ed/ACL-replaced.
		if err := fileprivacy.HardenDirectory(snapshotDir); err != nil {
			return "", fmt.Errorf("スナップショットディレクトリ権限設定エラー: %w", err)
		}
	}
	if defaultSnapshotDir {
		if err := os.Chmod(snapshotDir, 0700); err != nil { // #nosec G302 -- the default snapshot directory is intentionally private.
			return "", fmt.Errorf("スナップショットディレクトリ権限設定エラー: %w", err)
		}
	}
	if err := validateSnapshotDirectory(snapshotDir); err != nil {
		return "", fmt.Errorf("スナップショットディレクトリが安全ではありません: %w", err)
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
	cleanupSnapshot := func() {
		_ = removePrivateSQLiteFile(snapshotPath)
		_ = syncDirectory(snapshotDir)
	}
	// Backup is not a successful snapshot until the resulting bytes have been
	// hardened and opened with the same SQLCipher opener. This catches a
	// plaintext/wrong-key/corrupt output before it is exposed to ListSnapshots.
	if err := os.Chmod(snapshotPath, 0600); err != nil {
		cleanupSnapshot()
		return "", fmt.Errorf("スナップショット権限設定エラー: %w", err)
	}
	if err := hardenPrivateFile(snapshotPath); err != nil {
		cleanupSnapshot()
		return "", fmt.Errorf("スナップショットACL設定エラー: %w", err)
	}
	created, err := currentOpener.Open(context.Background(), snapshotPath, securedb.ReadOnly)
	if err != nil {
		cleanupSnapshot()
		return "", fmt.Errorf("作成済みスナップショットを開けません: %w", err)
	}
	validationErr := i.validateSnapshotDatabase(created, snapshotPath)
	closeErr := created.Close()
	if validationErr != nil || closeErr != nil {
		cleanupSnapshot()
		return "", errors.Join(errors.New("作成済みスナップショットの検証に失敗しました"), validationErr, closeErr)
	}
	if err := pruneSnapshots(snapshotDir, 30, budget, snapshotPath); err != nil {
		cleanupSnapshot()
		return "", fmt.Errorf("スナップショット容量検査エラー: %w", err)
	}
	if err := syncDirectory(snapshotDir); err != nil {
		cleanupSnapshot()
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
	return i.ListSnapshotsContext(context.Background(), snapshotDir)
}

// ListSnapshotsContext is the request-bound form of ListSnapshots. Every
// potentially expensive copy/open/integrity operation observes ctx, and one
// instance admits only one validation scan at a time.
func (i *Instance) ListSnapshotsContext(ctx context.Context, snapshotDir string) ([]string, error) {
	if i == nil {
		return nil, fmt.Errorf("データベースinstanceが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	i.ensureSnapshotCond()
	select {
	case i.snapshotValidationSem <- struct{}{}:
		defer func() { <-i.snapshotValidationSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshotDir == "" {
		snapshotDir = i.getSnapshotDir()
	}
	if err := validateSnapshotDirectory(snapshotDir); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("スナップショットディレクトリが安全ではありません: %w", err)
	}

	i.mu.RLock()
	opener := i.opener
	encrypted := opener != nil && opener.Encrypted()
	i.mu.RUnlock()
	if opener == nil {
		return nil, fmt.Errorf("データベースopenerが初期化されていません")
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("スナップショット一覧取得エラー: %w", err)
	}

	var snapshots []string
	checked := 0
	var validationBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if checked >= maxSnapshotValidationEntries {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if err := validateSnapshotName(entry.Name()); err != nil {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !validSnapshotFile(info) || !validSnapshotMode(info, encrypted) {
			// Listing is intentionally fail-closed per entry: a stray symlink,
			// hard link, or non-regular file must never become a restore target.
			continue
		}
		checked++
		valid, used, err := i.validateSnapshotEntry(ctx, path, snapshotDir, info, encrypted, maxSnapshotValidationWorkBytes-validationBytes)
		validationBytes += used
		if err != nil {
			return nil, err
		}
		if valid {
			snapshots = append(snapshots, entry.Name())
		}
	}
	sort.Strings(snapshots)
	return snapshots, nil
}

// validateSnapshotEntry validates the bytes copied from one no-follow source
// descriptor. Keeping all cleanup in this bounded helper is important: a
// defer in the ListSnapshots loop would retain up to 256 full candidates
// until the whole directory scan returned.
func (i *Instance) validateSnapshotEntry(ctx context.Context, path, snapshotDir string, inspected os.FileInfo, encrypted bool, remainingWork int64) (bool, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if remainingWork <= 0 {
		return false, 0, errors.New("snapshot validation work budget exhausted")
	}
	file, err := openSnapshotFile(path)
	if err != nil {
		return false, 0, nil
	}
	defer file.Close()
	fdInfo, err := file.Stat()
	if err != nil || fdInfo.Size() < 0 || fdInfo.Size() > maxSnapshotValidationBytes || fdInfo.Size() > remainingWork ||
		!validSnapshotFile(fdInfo) || !validSnapshotMode(fdInfo, encrypted) ||
		!snapshotSourceMatches(inspected, fdInfo) {
		return false, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	used := fdInfo.Size()
	// The descriptor-level checks above are required even for a positive
	// cache hit. In particular, Windows does not expose hard-link count in
	// os.FileInfo, while openSnapshotFile verifies it with the handle.
	candidatePath, candidateFile, err := temporaryDatabaseFile(snapshotDir, ".omni-money-list-validation-")
	if err != nil {
		return false, used, nil
	}
	defer func() {
		_ = candidateFile.Close()
		_ = removeSQLiteFiles(candidatePath)
	}()
	if err := copyFileToOpenBoundedContext(ctx, file, candidateFile, maxSnapshotValidationBytes); err != nil {
		return false, used, errIfContextDone(ctx, err)
	}
	if err := candidateFile.Sync(); err != nil {
		return false, used, nil
	}
	if err := candidateFile.Close(); err != nil {
		return false, used, nil
	}
	// Re-check the source descriptor after copying. A same-inode writer or
	// replacement must not cause a partially copied object to be treated as a
	// validated snapshot.
	postFDInfo, err := file.Stat()
	if err != nil || !snapshotSourceMatches(fdInfo, postFDInfo) {
		return false, used, nil
	}
	if err := ctx.Err(); err != nil {
		return false, used, err
	}
	db, err := i.opener.Open(ctx, candidatePath, securedb.ReadOnly)
	if err != nil {
		return false, used, errIfContextDone(ctx, err)
	}
	validErr := i.validateSnapshotDatabaseContext(ctx, db, candidatePath)
	closeErr := db.Close()
	if validErr != nil || closeErr != nil {
		return false, used, errIfContextDone(ctx, errors.Join(validErr, closeErr))
	}
	// The path may not have been replaced while its descriptor was being
	// copied/validated. The candidate remains the validated bytes even if the
	// path was briefly swapped away, but do not cache or expose that name.
	postInfo, postErr := os.Lstat(path)
	if postErr != nil || !sameSnapshotInfo(fdInfo, postInfo) {
		return false, used, nil
	}
	return true, used, nil
}

func errIfContextDone(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return nil
}

func sameSnapshotInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode().Perm() == b.Mode().Perm()
}

// validateSnapshotDatabase is deliberately read-only. Legacy snapshots may
// be migrated only during restore; listing must not mutate an off-host file.
func (i *Instance) validateSnapshotDatabase(target *sql.DB, path string) error {
	return i.validateSnapshotDatabaseContext(context.Background(), target, path)
}

func (i *Instance) validateSnapshotDatabaseContext(ctx context.Context, target *sql.DB, path string) error {
	if target == nil {
		return errors.New("snapshot database is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.opener != nil && i.opener.Encrypted() {
		if err := securedb.RequireEncryptedHeader(path); err != nil {
			return err
		}
	}
	if err := i.checkIntegrityContext(ctx, target); err != nil {
		return err
	}
	var userTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return err
	}
	if userTables == 0 {
		return errors.New("empty database is not a ledger snapshot")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return validateLedgerSchema(target, false)
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
	sourceDigest, err := digestOpenFile(snapshotFile)
	if err != nil {
		return fmt.Errorf("スナップショットdigest検証エラー: %w", err)
	}
	if err := copyFileToOpen(snapshotFile, candidateFile); err != nil {
		return fmt.Errorf("スナップショット候補コピーエラー: %w", err)
	}
	if err := candidateFile.Sync(); err != nil {
		return fmt.Errorf("復元候補のsyncエラー: %w", err)
	}
	postSourceDigest, err := digestOpenFile(snapshotFile)
	if err != nil || !strings.EqualFold(sourceDigest, postSourceDigest) {
		if err == nil {
			err = errors.New("スナップショットがコピー中に変更されました")
		}
		return fmt.Errorf("スナップショットdigest再検証エラー: %w", err)
	}
	candidateDigest, err := digestDatabaseFile(candidatePath)
	if err != nil || !strings.EqualFold(sourceDigest, candidateDigest) {
		if err == nil {
			err = errors.New("復元候補digestがスナップショットと一致しません")
		}
		return fmt.Errorf("復元候補digest検証エラー: %w", err)
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

	// Close the live handle only after candidate validation. Its WAL is
	// checkpointed first so the live file is a complete database. The original
	// pathname is deliberately kept in place until the replacement is ready:
	// a rename of live -> backup followed by a second rename would expose a
	// missing database if the process were killed at the boundary.
	if _, err := i.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("現行DBのcheckpointエラー: %w", err)
	}
	closeErr := i.db.Close()
	i.db = nil
	if closeErr != nil {
		return fmt.Errorf("現行DBのクローズエラー: %w", closeErr)
	}
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
	if err := copyDatabaseFile(currentPath, backupPath); err != nil {
		return fmt.Errorf("現行DB退避コピーエラー: %w", err)
	}
	if err := syncFileAndDirectory(backupPath, dir); err != nil {
		return fmt.Errorf("現行DB退避のfsyncエラー: %w", err)
	}
	oldDigest, err := digestDatabaseFile(backupPath)
	if err != nil {
		return fmt.Errorf("現行DB退避digest検証エラー: %w", err)
	}
	newDigest, err := digestDatabaseFile(candidatePath)
	if err != nil {
		return fmt.Errorf("復元候補digest検証エラー: %w", err)
	}
	manifest := restoreManifest{
		Version:   restoreManifestVersion,
		Phase:     "prepared",
		Current:   filepath.Base(currentPath),
		Backup:    filepath.Base(backupPath),
		Candidate: filepath.Base(candidatePath),
		OldDigest: oldDigest,
		NewDigest: newDigest,
	}
	if err := writeRestoreManifest(currentPath, manifest); err != nil {
		return fmt.Errorf("restore intent journal作成エラー: %w", err)
	}

	// Replace the live pathname in one filesystem operation. POSIX rename is
	// atomic and replaces the old file; Windows uses ReplaceFileW and retains
	// its backup argument. At every crash boundary either the old live file or
	// the complete candidate remains addressable as currentPath.
	if err := replaceDatabaseFile(candidatePath, currentPath, backupPath); err != nil {
		// The single replace failed, therefore currentPath still names the old
		// live database. Do not delete it in a rollback path designed for the
		// post-replace case.
		return fmt.Errorf("復元候補の配置エラー: %w", err)
	}
	removeCandidate = false
	if err := syncDirectory(dir); err != nil {
		return errors.Join(fmt.Errorf("復元配置のfsyncエラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
	}
	manifest.Phase = "swapped"
	if err := writeRestoreManifest(currentPath, manifest); err != nil {
		return errors.Join(fmt.Errorf("restore intent journal更新エラー: %w", err), rollbackRestoreFiles(currentPath, backupPath, candidatePath, i))
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
	if err := removeRestoreBackup(backupPath, dir); err != nil {
		// The new live database is valid and published, but report cleanup
		// failure so the manager closes the drained instance. The backup is
		// retained for startup cleanup/recovery rather than silently losing the
		// only rollback image.
		return errors.Join(errors.New("復元後の旧DB退避ファイル削除に失敗しました"), err)
	}
	if err := removeRestoreManifest(currentPath); err != nil {
		// The current database is already valid and published. Keep the durable
		// intent journal so the next startup can verify that state and retry its
		// cleanup rather than silently forgetting the restore boundary.
		return errors.Join(errors.New("復元後のrestore intent journal削除に失敗しました"), err)
	}
	log.Printf("snapshot_restore result=success")
	return nil
}

func (i *Instance) validateRestoreDatabase(target *sql.DB, path string) error {
	if target == nil {
		return fmt.Errorf("復元候補DBが初期化されていません")
	}
	// Restore validation is not fresh-database initialization. A blank
	// user_version=0 SQLite file must never be accepted and then populated by
	// createTablesOn, otherwise a same-key but unrelated file becomes a valid
	// ledger. Supported legacy snapshots must at least contain the historical
	// transaction shape before migration.
	var userTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return fmt.Errorf("復元DB table count検査エラー: %w", err)
	}
	if userTables == 0 {
		return errors.New("空のSQLiteファイルは復元候補ではありません")
	}
	if err := validateLedgerSchema(target, false); err != nil {
		return fmt.Errorf("復元DB schema最低要件エラー: %w", err)
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
	return i.checkIntegrityContext(context.Background(), target)
}

func (i *Instance) checkIntegrityContext(ctx context.Context, target *sql.DB) error {
	if i.opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := i.opener.CheckIntegrity(ctx, target); err != nil {
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
	if value > maxSnapshotValidationBytes {
		return 0, fmt.Errorf("SNAPSHOT_MAX_TOTAL_BYTES は %d bytes 以下で指定してください", maxSnapshotValidationBytes)
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
		snapshotBytes := info.Size()
		for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
			sidecarInfo, sidecarErr := os.Lstat(sidecar)
			if os.IsNotExist(sidecarErr) {
				continue
			}
			if sidecarErr != nil {
				return sidecarErr
			}
			if !validSnapshotFile(sidecarInfo) {
				return fmt.Errorf("スナップショットsidecarが通常ファイルではありません: %s", filepath.Base(sidecar))
			}
			if sidecarInfo.Size() < 0 || snapshotBytes > (1<<63-1)-sidecarInfo.Size() {
				return fmt.Errorf("スナップショットsidecar容量が整数上限を超えました")
			}
			snapshotBytes += sidecarInfo.Size()
		}
		if total > (1<<63-1)-snapshotBytes {
			return fmt.Errorf("スナップショット容量が整数上限を超えました")
		}
		total += snapshotBytes
		usage = append(usage, snapshotUsageEntry{name: entry.Name(), path: path, size: snapshotBytes})
	}
	sort.Slice(usage, func(i, j int) bool { return usage[i].name < usage[j].name })
	removed := false
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
		if err := removePrivateSQLiteFile(victim.path); err != nil {
			return fmt.Errorf("古いスナップショット削除エラー (%s): %w", victim.name, err)
		}
		for _, sidecar := range []string{victim.path + "-wal", victim.path + "-shm", victim.path + "-journal"} {
			if err := removePrivateSQLiteFile(sidecar); err != nil {
				return fmt.Errorf("古いスナップショットsidecar削除エラー (%s): %w", victim.name, err)
			}
		}
		total -= victim.size
		usage = append(usage[:candidate], usage[candidate+1:]...)
		removed = true
		log.Printf("security_event=snapshot_prune result=success remaining_bytes=%d", total)
	}
	if removed {
		if err := syncDirectory(snapshotDir); err != nil {
			return fmt.Errorf("スナップショットprune directory fsyncエラー: %w", err)
		}
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
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
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
	if err := fileprivacy.HardenDirectory(path); err != nil {
		return err
	}
	return fileprivacy.ValidateDirectory(path)
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
		if err := hardenPrivateFile(candidate); err != nil {
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
	if err := fileprivacy.Harden(out); err != nil {
		return err
	}
	if err := copyFileToOpenBounded(in, out, maxSnapshotValidationBytes); err != nil {
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
	if err := fileprivacy.ValidateDirectory(path); err != nil {
		return err
	}
	if !snapshotDirectoryModeAllowed(info) {
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
	return snapshotModeAllowed(info, encrypted)
}

// snapshotSourceMatches binds the descriptor used for the copy to the
// metadata inspected before opening it.  A same-name replacement between
// Lstat and open is therefore rejected instead of silently copying a
// different file.  Size is checked as well because a writer can mutate a
// regular file without changing its inode.
func snapshotSourceMatches(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime()) &&
		expected.Mode().Perm() == actual.Mode().Perm()
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
	if err := fileprivacy.Harden(file); err != nil {
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

// copyDatabaseFile creates a new private destination and copies the complete
// database bytes without ever unlinking or replacing the source. The O_EXCL
// destination is important even though the name is random: it preserves the
// no-overwrite boundary if a hostile process can influence the directory.
func copyDatabaseFile(source, destination string) error {
	in, err := openSnapshotFile(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- destination is generated in the private DB directory.
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		_ = out.Close()
		if !completed {
			_ = os.Remove(destination)
		}
	}()
	if err := out.Chmod(0600); err != nil {
		return err
	}
	if err := fileprivacy.Harden(out); err != nil {
		return err
	}
	if err := copyFileToOpenBounded(in, out, maxSnapshotValidationBytes); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	completed = true
	return nil
}

func removeRestoreBackup(path, dir string) error {
	if err := removePrivateSQLiteFile(path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// restoreManifestPath is kept beside the live database, rather than in the
// snapshot directory.  It is the durable boundary for a restore swap: startup
// must inspect it before any opener which is allowed to create a new database.
func restoreManifestPath(databasePath string) string {
	return filepath.Join(filepath.Dir(databasePath), "."+filepath.Base(databasePath)+".restore-intent.json")
}

func writeRestoreManifest(databasePath string, manifest restoreManifest) error {
	if err := validateRestoreManifest(databasePath, manifest); err != nil {
		return err
	}
	dir := filepath.Dir(databasePath)
	tmp, err := os.CreateTemp(dir, ".omni-money-restore-manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	complete := false
	defer func() {
		_ = tmp.Close()
		if !complete {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if err := fileprivacy.Harden(tmp); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceManifestFile(tmpPath, restoreManifestPath(databasePath)); err != nil {
		return err
	}
	complete = true
	return syncDirectory(dir)
}

func readRestoreManifest(databasePath string) (restoreManifest, bool, error) {
	file, err := openSnapshotFile(restoreManifestPath(databasePath))
	if os.IsNotExist(err) {
		return restoreManifest{}, false, nil
	}
	if err != nil {
		return restoreManifest{}, true, err
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil || !validSnapshotFile(info) || info.Size() > 16*1024 {
		if statErr != nil {
			return restoreManifest{}, true, statErr
		}
		return restoreManifest{}, true, errors.New("restore intent is not a private regular file")
	}
	var manifest restoreManifest
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024))
	if err := decoder.Decode(&manifest); err != nil {
		return restoreManifest{}, true, fmt.Errorf("decode restore intent: %w", err)
	}
	if err := validateRestoreManifest(databasePath, manifest); err != nil {
		return restoreManifest{}, true, err
	}
	return manifest, true, nil
}

func validateRestoreManifest(databasePath string, manifest restoreManifest) error {
	if manifest.Version != restoreManifestVersion || (manifest.Phase != "prepared" && manifest.Phase != "swapped") {
		return errors.New("unsupported restore intent manifest")
	}
	if manifest.Current != filepath.Base(databasePath) || !safeRestoreArtifactName(manifest.Backup, ".omni-money-restore-backup-") || !safeRestoreArtifactName(manifest.Candidate, ".omni-money-restore-candidate-") {
		return errors.New("restore intent manifest path is invalid")
	}
	for _, digest := range []string{manifest.OldDigest, manifest.NewDigest} {
		if len(digest) != sha256.Size*2 {
			return errors.New("restore intent manifest digest is invalid")
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return errors.New("restore intent manifest digest is not hexadecimal")
		}
	}
	return nil
}

func safeRestoreArtifactName(name, prefix string) bool {
	return name != "" && name == filepath.Base(name) && !strings.ContainsAny(name, `/\\`) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".db")
}

func removeRestoreManifest(databasePath string) error {
	if err := os.Remove(restoreManifestPath(databasePath)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(databasePath))
}

func digestDatabaseFile(path string) (string, error) {
	file, err := openSnapshotFile(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return digestOpenFile(file)
}

func digestOpenFile(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("nil file for digest")
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() < 0 || info.Size() > maxSnapshotValidationBytes {
		return "", errors.New("file exceeds validation size limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, info.Size()); err != nil {
		return "", err
	}
	post, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !snapshotSourceMatches(info, post) {
		return "", errors.New("file changed while digesting")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (i *Instance) validateRecoveryDatabase(path string) error {
	if i == nil || i.opener == nil {
		return errors.New("database opener is unavailable for recovery")
	}
	db, err := i.opener.Open(context.Background(), path, securedb.Writable)
	if err != nil {
		return err
	}
	if err := i.validateRestoreDatabase(db, path); err != nil {
		_ = db.Close()
		return err
	}
	if err := checkpointAndClose(db, path); err != nil {
		return err
	}
	return syncFileAndDirectory(path, filepath.Dir(path))
}

func (i *Instance) recoveryFile(path, expectedDigest string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !validSnapshotFile(info) || (i.opener != nil && !validSnapshotMode(info, i.opener.Encrypted())) {
		return false, errors.New("restore artifact is not a private regular file")
	}
	digest, err := digestDatabaseFile(path)
	if err != nil {
		return false, err
	}
	if expectedDigest != "" && !strings.EqualFold(digest, expectedDigest) {
		return false, fmt.Errorf("restore artifact digest mismatch")
	}
	return true, nil
}

func removeRecoveryArtifact(path, dir string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !validSnapshotFile(info) {
		return errors.New("refusing to remove unsafe restore artifact")
	}
	var errs []error
	if err := removePrivateSQLiteFile(path); err != nil {
		errs = append(errs, err)
	}
	// SQLite can leave sidecars beside a copied image. Treat each as an
	// independent artifact and never blindly unlink a symlink/hard link.
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if sidecarInfo, statErr := os.Lstat(sidecar); statErr == nil {
			if !validSnapshotFile(sidecarInfo) {
				errs = append(errs, fmt.Errorf("unsafe restore sidecar: %s", filepath.Base(sidecar)))
				continue
			}
			if removeErr := removePrivateSQLiteFile(sidecar); removeErr != nil {
				errs = append(errs, removeErr)
			}
		} else if !os.IsNotExist(statErr) {
			errs = append(errs, statErr)
		}
	}
	if err := syncDirectory(dir); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// recoverRestoreState runs before any create-capable database open.  It
// treats a journal with an absent/invalid live pathname as a recovery problem,
// never as permission to create a fresh ledger.  Ambiguous artifacts fail
// closed and are left for an operator rather than being guessed away.
func (i *Instance) recoverRestoreState(databasePath string) error {
	dir := filepath.Dir(databasePath)
	manifest, hasManifest, err := readRestoreManifest(databasePath)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var backups, candidates []string
	var orphanSidecars []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, ".omni-money-restore-backup-") && strings.HasSuffix(name, ".db"):
			backups = append(backups, filepath.Join(dir, name))
		case strings.HasPrefix(name, ".omni-money-restore-candidate-") && strings.HasSuffix(name, ".db"):
			candidates = append(candidates, filepath.Join(dir, name))
		case (strings.HasPrefix(name, ".omni-money-restore-backup-") || strings.HasPrefix(name, ".omni-money-restore-candidate-") || strings.HasPrefix(name, ".omni-money-list-validation-")) && (strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-journal")):
			orphanSidecars = append(orphanSidecars, filepath.Join(dir, name))
		}
	}
	if !hasManifest && len(backups) == 0 && len(candidates) == 0 && len(orphanSidecars) == 0 {
		return nil
	}
	if hasManifest {
		backups = []string{filepath.Join(dir, manifest.Backup)}
		candidates = []string{filepath.Join(dir, manifest.Candidate)}
	}
	liveDigest, liveDigestErr := digestDatabaseFile(databasePath)
	liveExists := liveDigestErr == nil
	if liveExists && hasManifest && !strings.EqualFold(liveDigest, manifest.OldDigest) && !strings.EqualFold(liveDigest, manifest.NewDigest) {
		// The pathname exists but is not either journaled image. Treat it as
		// corrupt and recover only from exactly one validated journal image.
		liveExists = false
	}
	if liveExists {
		if err := i.validateRecoveryDatabase(databasePath); err != nil {
			liveExists = false
		}
	}
	if liveExists {
		// A durable, valid live image wins regardless of which side of the
		// rename boundary was recorded. Validate every named artifact before
		// deleting it so an unexpected valid copy is never discarded.
		for _, path := range append(backups, candidates...) {
			digest := ""
			if hasManifest {
				switch filepath.Base(path) {
				case manifest.Backup:
					digest = manifest.OldDigest
				case manifest.Candidate:
					digest = manifest.NewDigest
				default:
					return errors.New("restore artifact is not named by its journal")
				}
			}
			item := struct {
				path   string
				digest string
			}{path: path, digest: digest}
			if ok, err := i.recoveryFile(item.path, item.digest); err != nil {
				return err
			} else if ok {
				if err := removeRecoveryArtifact(item.path, dir); err != nil {
					return err
				}
			}
		}
		for _, path := range orphanSidecars {
			if err := removePrivateSQLiteFile(path); err != nil {
				return err
			}
		}
		if len(orphanSidecars) > 0 {
			if err := syncDirectory(dir); err != nil {
				return err
			}
		}
		if hasManifest {
			return removeRestoreManifest(databasePath)
		}
		return nil
	}
	if !hasManifest {
		return errors.New("live database is unavailable while restore artifacts exist")
	}
	if len(orphanSidecars) != 0 {
		return errors.New("orphan restore sidecar prevents deterministic recovery")
	}
	// The live pathname is missing or corrupt. Exactly one durable image may
	// be used; choosing between two valid images would make crash recovery
	// nondeterministic.
	type image struct{ path, digest string }
	var valid []image
	for _, candidate := range []struct {
		path   string
		digest string
	}{{backups[0], manifest.OldDigest}, {candidates[0], manifest.NewDigest}} {
		ok, err := i.recoveryFile(candidate.path, candidate.digest)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if ok {
			valid = append(valid, image{candidate.path, candidate.digest})
		}
	}
	if len(valid) != 1 {
		return fmt.Errorf("restore recovery is ambiguous: %d valid images", len(valid))
	}
	if err := installRecoveryFile(valid[0].path, databasePath, candidates[0]); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	if err := i.validateRecoveryDatabase(databasePath); err != nil {
		return fmt.Errorf("recovered database validation failed: %w", err)
	}
	for _, path := range append(backups, candidates...) {
		if filepath.Clean(path) == filepath.Clean(databasePath) {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			if err := removeRecoveryArtifact(path, dir); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return removeRestoreManifest(databasePath)
}

func cleanupRestoreArtifacts(databasePath string) error {
	dir := filepath.Dir(databasePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	base := filepath.Base(databasePath)
	prefixes := []string{
		".omni-money-restore-backup-",
		".omni-money-restore-candidate-",
		".omni-money-list-validation-",
	}
	var cleanupErrs []error
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".db") {
				matched = true
				break
			}
		}
		if !matched || name == base {
			continue
		}
		path := filepath.Join(dir, name)
		_, statErr := os.Lstat(path)
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				cleanupErrs = append(cleanupErrs, statErr)
			}
			continue
		}
		if removeErr := removeRecoveryArtifact(path, dir); removeErr != nil {
			cleanupErrs = append(cleanupErrs, removeErr)
		} else {
			removed = true
		}
	}
	if removed {
		if err := syncDirectory(dir); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	return errors.Join(cleanupErrs...)
}

func copyFileToOpen(src, dst *os.File) error {
	return copyFileToOpenBounded(src, dst, maxSnapshotValidationBytes)
}

func copyFileToOpenBounded(src, dst *os.File, maxBytes int64) error {
	return copyFileToOpenBoundedContext(context.Background(), src, dst, maxBytes)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

func copyFileToOpenBoundedContext(ctx context.Context, src, dst *os.File, maxBytes int64) error {
	if src == nil || dst == nil {
		return errors.New("コピー元またはコピー先が開かれていません")
	}
	if maxBytes <= 0 {
		return errors.New("コピー上限が無効です")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return fmt.Errorf("コピー元がサイズ上限を超えています")
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
	written, err := io.Copy(dst, io.LimitReader(contextReader{ctx: ctx, r: src}, maxBytes+1))
	if err != nil {
		return err
	}
	if written != info.Size() {
		return fmt.Errorf("コピー元のサイズがコピー中に変化しました")
	}
	postInfo, err := src.Stat()
	if err != nil {
		return err
	}
	if !snapshotSourceMatches(info, postInfo) {
		return errors.New("コピー元がコピー後に置き換えられました")
	}
	return nil
}

func removeSQLiteFiles(path string) error {
	var errs []error
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := removePrivateSQLiteFile(candidate); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removeSQLiteSidecars(path string) error {
	var errs []error
	for _, candidate := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := removePrivateSQLiteFile(candidate); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removePrivateSQLiteFile(path string) error {
	file, err := openSnapshotFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !validSnapshotFile(info) || !snapshotSourceMatches(pathInfo, info) {
		return fmt.Errorf("refusing to remove unsafe SQLite artifact: %s", filepath.Base(path))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if postInfo, postErr := os.Lstat(path); postErr == nil {
		if !snapshotSourceMatches(info, postInfo) {
			return fmt.Errorf("SQLite artifact was replaced while removing: %s", filepath.Base(path))
		}
		return fmt.Errorf("SQLite artifact remained after removal: %s", filepath.Base(path))
	} else if !os.IsNotExist(postErr) {
		return postErr
	}
	return nil
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
	// Never unlink currentPath: this rollback runs after the candidate has
	// already replaced it, and unlink-then-rename would recreate the same
	// crash window as the original restore implementation. Sidecars can be
	// removed first; the main file is replaced in one OS atomic operation.
	if err := removeSQLiteSidecars(currentPath); err != nil {
		errs = append(errs, fmt.Errorf("remove failed restore sidecars: %w", err))
	}
	if _, err := os.Lstat(backupPath); err == nil {
		if err := replaceDatabaseFile(backupPath, currentPath, candidatePath); err != nil {
			errs = append(errs, fmt.Errorf("restore backup atomic replace: %w", err))
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
	// On Windows ReplaceFileW stores the failed new live file at
	// candidatePath. POSIX rename-overwrite consumes backupPath and leaves no
	// such artifact. Cleanup happens only after the old live file is durable.
	if err := removeSQLiteFiles(candidatePath); err != nil {
		errs = append(errs, fmt.Errorf("remove failed restore image: %w", err))
	}
	if err := syncDirectory(dir); err != nil {
		errs = append(errs, fmt.Errorf("restore cleanup directory sync: %w", err))
	}
	if len(errs) == 0 {
		if err := removeRestoreManifest(currentPath); err != nil {
			errs = append(errs, fmt.Errorf("remove restore intent after rollback: %w", err))
		}
	}
	return errors.Join(errs...)
}
