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
	"unicode"
	"unicode/utf8"

	sqlite3 "github.com/mattn/go-sqlite3"
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

// SQLite creates these internal statistic/sequence tables itself. Keep the
// allowlist explicit: a name such as sqlite_evil must be treated as a user
// table and rejected, rather than disappearing behind a LIKE filter.
const sqliteUserTablePredicate = "name NOT IN ('sqlite_sequence', 'sqlite_stat1', 'sqlite_stat4')"

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
}

const maxSnapshotValidationEntries = 256
const maxSnapshotDirectoryEntries = 256
const snapshotDirectoryReadBatchSize = 32

// A validation may materialize one file up to maxSnapshotValidationBytes in a
// private temporary copy. Keep this admission process-wide so independent
// server tenants cannot multiply that disk/I/O budget by opening one semaphore
// per Instance.
const maxConcurrentSnapshotValidations = 1

var snapshotValidationAdmission = make(chan struct{}, maxConcurrentSnapshotValidations)

// A single list request is capped at one maximum-size snapshot. This keeps
// validation work bounded even when a vault contains the maximum number of
// large off-host files, while still allowing the largest snapshot that
// CreateSnapshot can retain to be listed and restored.
const maxSnapshotValidationWorkBytes int64 = maxSnapshotValidationBytes
const restoreManifestVersion = 1
const snapshotPruneArtifactPrefix = ".omni-money-snapshot-prune-"
const snapshotPruneManifestVersion = 2
const snapshotPruneManifestName = ".omni-money-snapshot-prune-intent.json"
const maxSnapshotPruneManifestBytes int64 = 64 * 1024
const maxSnapshotPruneTemporaryEntries = 1024
const snapshotPruneCreateTempPrefix = ".omni-money-snapshot-prune-create-"
const snapshotPruneUpdateTempPrefix = ".omni-money-snapshot-prune-manifest-"

// SnapshotTransactionLockFileName is exported only so the strict Desktop
// migration tree can recognize this exact database-owned coordination file in
// a destination snapshot directory. Callers must still validate the artifact
// with ValidateSnapshotTransactionLock.
const SnapshotTransactionLockFileName = snapshotTransactionLockName

type restoreManifest struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	Current   string `json:"current"`
	Backup    string `json:"backup"`
	Candidate string `json:"candidate"`
	OldDigest string `json:"old_digest"`
	NewDigest string `json:"new_digest"`
}

// snapshotPruneManifest is the durable transaction record for retention
// quarantine. A process may die after publishing the new generation but
// before deleting quarantined victims; without this phase marker startup
// cannot distinguish that state from a pre-publication rollback and could
// resurrect old generations alongside the new one.
type snapshotPruneManifest struct {
	Version   int                           `json:"version"`
	Phase     string                        `json:"phase"`
	Snapshot  string                        `json:"snapshot"`
	NewDigest string                        `json:"new_digest"`
	Victims   []snapshotPruneManifestVictim `json:"victims"`
}

type snapshotPruneManifestVictim struct {
	Original    string                         `json:"original"`
	Quarantined string                         `json:"quarantined"`
	Digest      string                         `json:"digest"`
	Sidecars    []snapshotPruneManifestSidecar `json:"sidecars,omitempty"`
}

type snapshotPruneManifestSidecar struct {
	Original    string `json:"original"`
	Quarantined string `json:"quarantined"`
	Digest      string `json:"digest"`
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
	return createTablesOnContext(context.Background(), target)
}

// createTablesOnContext performs schema inspection and migration with the
// caller's cancellation boundary. Restore validation uses this variant so a
// canceled request cannot continue holding the process-wide admission slot
// through an unbounded migration query.
func createTablesOnContext(ctx context.Context, target *sql.DB) error {
	if target == nil {
		return fmt.Errorf("データベース接続が初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var version int
	if err := target.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("スキーマversion取得エラー: %w", err)
	}
	if version > ledgerSchemaVersion {
		return fmt.Errorf("データベースschema version %dは対応version %dより新しいため開けません", version, ledgerSchemaVersion)
	}
	if err := validateLedgerSchemaContext(ctx, target, false); err != nil {
		return fmt.Errorf("schema identity/minimum validation failed: %w", err)
	}
	var identity int64
	if err := target.QueryRowContext(ctx, "PRAGMA application_id").Scan(&identity); err != nil {
		return fmt.Errorf("スキーマidentity取得エラー: %w", err)
	}
	var userTables int
	if err := validateInternalSQLiteTablesContext(ctx, target); err != nil {
		return fmt.Errorf("SQLite internal table validation failed: %w", err)
	}
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate).Scan(&userTables); err != nil {
		return fmt.Errorf("schema table count取得エラー: %w", err)
	}
	legacyMigration := version == 0 && identity == 0 && userTables > 0
	legacyLayout, err := isSupportedLegacyCurrentLayoutContext(ctx, target)
	if err != nil {
		return fmt.Errorf("legacy schema fingerprint検査エラー: %w", err)
	}
	legacyImageLayout, err := isLegacyTransactionImagesLayoutContext(ctx, target)
	if err != nil {
		return fmt.Errorf("legacy transaction_images fingerprint検査エラー: %w", err)
	}
	if legacyMigration {
		// Version 0 is ambiguous: it is either a fresh SQLite file or a
		// historical ledger. Once user tables exist, never let CREATE TABLE IF
		// NOT EXISTS upgrade a same-name impostor. The only supported pre-marker
		// family is checked against its complete immutable DDL before anything
		// is changed.
		legacy, err := isSupportedLegacyPreMigrationLayoutContext(ctx, target)
		if err != nil {
			return fmt.Errorf("legacy schema fingerprint検査エラー: %w", err)
		}
		if !legacy {
			return errors.New("unsupported version-0 ledger layout")
		}
	}

	tx, err := target.BeginTx(ctx, nil)
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
	if legacyImageLayout {
		// The historical image table has no foreign key and therefore cannot be
		// repaired by CREATE TABLE IF NOT EXISTS. Validate all existing rows and
		// rebuild it under the current DDL before any identity/version marker is
		// written. The transaction makes malformed, orphaned, or over-quota data
		// fail closed without leaving a half-migrated table behind.
		if err := migrateLegacyTransactionImagesContext(ctx, tx); err != nil {
			return fmt.Errorf("legacy transaction_images migration failed: %w", err)
		}
		if err := validateCurrentTransactionImagesContext(ctx, tx); err != nil {
			return fmt.Errorf("migrated transaction_images validation failed: %w", err)
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
	// A historical v2 image table can coexist with the current version marker:
	// an older migration used IF NOT EXISTS and consequently left the legacy
	// table (and its dependent objects) in place. Re-run the idempotent DDL
	// after rebuilding that table so indexes/triggers dropped by the rebuild
	// are recreated even though user_version already equals the current value.
	if version < ledgerSchemaVersion || legacyImageLayout {
		for _, stmt := range statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
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
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", ledgerSchemaVersion)); err != nil {
			return fmt.Errorf("スキーマversion更新エラー: %w", err)
		}
	}
	// A blank new database receives the identity marker. Legacy databases keep
	// application_id=0 so their explicitly supported pre-marker layout can be
	// reopened after migration without pretending that old table definitions
	// had constraints that SQLite cannot add with CREATE IF NOT EXISTS.
	if identity == ledgerSchemaIdentity || userTables == 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", ledgerSchemaIdentity)); err != nil {
			return fmt.Errorf("スキーマidentity更新エラー: %w", err)
		}
	}
	if err := validateCriticalSchemaContext(ctx, tx); err != nil {
		return err
	}
	// Validate the complete post-migration schema while the transaction is
	// still open. A crafted extra table/index/constraint must not leave a
	// partially upgraded database committed after validation reports failure.
	if err := validateLedgerSchemaAfterMigrationContext(ctx, tx, !legacyLayout); err != nil {
		return fmt.Errorf("完全なschema validation failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("スキーマtransaction確定エラー: %w", err)
	}
	return nil
}

func migrateLegacyTransactionImages(tx *sql.Tx) error {
	return migrateLegacyTransactionImagesContext(context.Background(), tx)
}

func migrateLegacyTransactionImagesContext(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return errors.New("legacy image migration transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var invalidRows int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM transaction_images ti
		LEFT JOIN transactions t ON t.id = ti.transaction_id
		WHERE ti.id IS NULL
		   OR ti.transaction_id IS NULL
		   OR t.id IS NULL
		   OR ti.filename IS NULL
		   OR ti.data IS NULL
		   OR length(ti.data) <= 0
		   OR length(ti.data) > ?
		   OR ti.mime_type IS NULL`, models.MaxImageBytes).Scan(&invalidRows); err != nil {
		return fmt.Errorf("legacy image row validation failed: %w", err)
	}
	if invalidRows != 0 {
		return fmt.Errorf("legacy image table contains %d invalid/orphan rows", invalidRows)
	}
	var imageCount int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(image_count), 0)
		FROM (SELECT transaction_id, COUNT(*) AS image_count
		      FROM transaction_images GROUP BY transaction_id)`).Scan(&imageCount); err != nil {
		return fmt.Errorf("legacy per-transaction image count validation failed: %w", err)
	}
	if imageCount > int64(models.MaxImagesPerTransaction) {
		return fmt.Errorf("legacy transaction image count exceeds %d", models.MaxImagesPerTransaction)
	}
	var transactionBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(total_bytes), 0)
		FROM (SELECT transaction_id, COALESCE(SUM(length(data)), 0) AS total_bytes
		      FROM transaction_images GROUP BY transaction_id)`).Scan(&transactionBytes); err != nil {
		return fmt.Errorf("legacy per-transaction image quota validation failed: %w", err)
	}
	if transactionBytes > models.MaxImageBytesPerTransaction {
		return fmt.Errorf("legacy per-transaction image bytes exceed %d", models.MaxImageBytesPerTransaction)
	}
	var accountBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(total_bytes), 0)
		FROM (SELECT t.account, COALESCE(SUM(length(ti.data)), 0) AS total_bytes
		      FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id
		      GROUP BY t.account)`).Scan(&accountBytes); err != nil {
		return fmt.Errorf("legacy per-account image quota validation failed: %w", err)
	}
	if accountBytes > models.MaxImageBytesPerAccount {
		return fmt.Errorf("legacy per-account image bytes exceed %d", models.MaxImageBytesPerAccount)
	}
	var databaseBytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(data)), 0) FROM transaction_images").Scan(&databaseBytes); err != nil {
		return fmt.Errorf("legacy database image quota validation failed: %w", err)
	}
	if databaseBytes > models.MaxImageBytesDatabase {
		return fmt.Errorf("legacy database image bytes exceed %d", models.MaxImageBytesDatabase)
	}

	const legacyTable = "transaction_images_omni_legacy_migration"
	if _, err := tx.ExecContext(ctx, "ALTER TABLE transaction_images RENAME TO "+legacyTable); err != nil {
		return fmt.Errorf("legacy image table rename failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE transaction_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transaction_id INTEGER NOT NULL,
		filename TEXT NOT NULL,
		data BLOB NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("current image table creation failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transaction_images
		(id, transaction_id, filename, data, mime_type, created_at)
		SELECT id, transaction_id, filename, data, mime_type, created_at
		FROM `+legacyTable); err != nil {
		return fmt.Errorf("legacy image row copy failed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE "+legacyTable); err != nil {
		return fmt.Errorf("legacy image table removal failed: %w", err)
	}
	return nil
}

func validateCurrentTransactionImages(target schemaQueryer) error {
	return validateCurrentTransactionImagesContext(context.Background(), target)
}

func validateCurrentTransactionImagesContext(ctx context.Context, target schemaQueryer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var definition sql.NullString
	if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='transaction_images'").Scan(&definition); err != nil || !definition.Valid {
		return errors.New("current transaction_images definition is unavailable")
	}
	if canonicalDDL(definition.String) != expectedLedgerTableDefinitions()["transaction_images"] {
		return errors.New("transaction_images definition is not the current allowlisted DDL")
	}
	wantColumns := []expectedColumnDefinition{
		{name: "id", columnType: "INTEGER", primaryKey: 1},
		{name: "transaction_id", columnType: "INTEGER", notNull: 1},
		{name: "filename", columnType: "TEXT", notNull: 1},
		{name: "data", columnType: "BLOB", notNull: 1},
		{name: "mime_type", columnType: "TEXT", notNull: 1, defaultValue: "'image/jpeg'"},
		{name: "created_at", columnType: "DATETIME", defaultValue: "current_timestamp"},
	}
	rows, err := target.QueryContext(ctx, "PRAGMA table_info(transaction_images)")
	if err != nil {
		return err
	}
	index := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if index >= len(wantColumns) {
			_ = rows.Close()
			return errors.New("transaction_images has unexpected extra columns")
		}
		want := wantColumns[index]
		if cid != index || name != want.name || strings.ToUpper(columnType) != want.columnType || notNull != want.notNull || primaryKey != want.primaryKey || normalizeColumnDefault(defaultValue) != want.defaultValue {
			_ = rows.Close()
			return fmt.Errorf("transaction_images.%s column definition is not current", name)
		}
		index++
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if index != len(wantColumns) {
		return errors.New("transaction_images is missing current columns")
	}

	rows, err = target.QueryContext(ctx, "PRAGMA foreign_key_list(transaction_images)")
	if err != nil {
		return err
	}
	foreignKeyCount := 0
	for rows.Next() {
		var id, seq int
		var referenced, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &referenced, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			return err
		}
		foreignKeyCount++
		if foreignKeyCount != 1 || referenced != "transactions" || from != "transaction_id" || to != "id" || strings.ToUpper(onUpdate) != "NO ACTION" || strings.ToUpper(onDelete) != "CASCADE" {
			_ = rows.Close()
			return errors.New("transaction_images foreign key definition is not current")
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if foreignKeyCount != 1 {
		return errors.New("transaction_images current foreign key is missing")
	}
	rows, err = target.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign_key_check reported an orphan row")
	}
	return rows.Err()
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateCriticalSchema(target schemaQueryer) error {
	return validateCriticalSchemaContext(context.Background(), target)
}

func validateCriticalSchemaContext(ctx context.Context, target schemaQueryer) error {
	if target == nil {
		return fmt.Errorf("データベース接続が初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
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
		rows, err := target.QueryContext(ctx, "PRAGMA table_info("+table+")")
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
		if err := target.QueryRowContext(ctx,
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
	if err := validateRootTagIndexContext(ctx, target); err != nil {
		return err
	}
	return nil
}

// validateLedgerSchema validates both the pre-migration trust boundary and
// the complete current schema. requireCurrent is false for read-only listing:
// an explicitly supported legacy version may be shown, but it still needs a
// recognizable ledger identity/minimum shape and is never migrated there.
func validateLedgerSchema(target schemaQueryer, requireCurrent bool) error {
	return validateLedgerSchemaContext(context.Background(), target, requireCurrent)
}

func validateLedgerSchemaInternal(target schemaQueryer, requireCurrent, strictCurrent bool) error {
	return validateLedgerSchemaContextInternal(context.Background(), target, requireCurrent, strictCurrent)
}

func validateLedgerSchemaContext(ctx context.Context, target schemaQueryer, requireCurrent bool) error {
	return validateLedgerSchemaContextInternal(ctx, target, requireCurrent, true)
}

func validateLedgerSchemaContextInternal(ctx context.Context, target schemaQueryer, requireCurrent, strictCurrent bool) error {
	if target == nil {
		return errors.New("database schema target is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var version int
	if err := target.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > ledgerSchemaVersion {
		return fmt.Errorf("unsupported ledger schema version %d", version)
	}
	var identity int64
	if err := target.QueryRowContext(ctx, "PRAGMA application_id").Scan(&identity); err != nil {
		return fmt.Errorf("read ledger identity: %w", err)
	}
	if identity != 0 && identity != ledgerSchemaIdentity {
		return fmt.Errorf("database identity %08x is not an Omni Money ledger", identity)
	}
	var userTables int
	if err := validateInternalSQLiteTablesContext(ctx, target); err != nil {
		return fmt.Errorf("SQLite internal table validation failed: %w", err)
	}
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate).Scan(&userTables); err != nil {
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
	if err := requireColumnsContext(ctx, target, "transactions", []string{"id", "account", "date", "item", "type", "amount", "balance", "memo"}); err != nil {
		return fmt.Errorf("ledger minimum schema: %w", err)
	}
	if version == ledgerSchemaVersion {
		strict := strictCurrent
		if strictCurrent && identity == 0 {
			// application_id=0 is never a compatibility marker. A historical
			// image is accepted only when every legacy table definition matches
			// the exact allowlisted pre-identity DDL and all current objects have
			// their expected shape. Same-name forged tables/markers do not pass.
			legacy, err := isSupportedLegacyCurrentLayoutContext(ctx, target)
			if err != nil {
				return err
			}
			strict = !legacy
		}
		return validateFullLedgerSchemaContext(ctx, target, strict)
	}
	if !requireCurrent {
		return nil
	}
	if version != ledgerSchemaVersion {
		return fmt.Errorf("schema version %d was not migrated", version)
	}
	return validateFullLedgerSchemaContext(ctx, target, strictCurrent)
}

func validateLedgerSchemaAfterMigration(target schemaQueryer, strict bool) error {
	return validateLedgerSchemaAfterMigrationContext(context.Background(), target, strict)
}

func validateLedgerSchemaAfterMigrationContext(ctx context.Context, target schemaQueryer, strict bool) error {
	return validateLedgerSchemaContextInternal(ctx, target, true, strict)
}

func canonicalSQL(value string) string {
	// SQLite preserves the contents of quoted literals in sqlite_master.sql.
	// Normalize keywords and whitespace only outside quoted tokens; lowercasing
	// a literal would make semantically different CHECK/default expressions
	// share a fingerprint.
	var out strings.Builder
	spacePending := false
	var quote byte
	for index := 0; index < len(value); {
		current := value[index]
		if quote != 0 {
			out.WriteByte(current)
			index++
			if quote == '[' {
				if current == ']' {
					if index < len(value) && value[index] == ']' {
						out.WriteByte(value[index])
						index++
					} else {
						quote = 0
					}
				}
				continue
			}
			if current == quote {
				if index < len(value) && value[index] == quote {
					out.WriteByte(value[index])
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		if current == '\'' || current == '"' || current == '`' || current == '[' {
			if spacePending && out.Len() > 0 {
				out.WriteByte(' ')
			}
			spacePending = false
			out.WriteByte(current)
			quote = current
			index++
			continue
		}
		runeValue, width := utf8.DecodeRuneInString(value[index:])
		if unicode.IsSpace(runeValue) {
			spacePending = true
			index += width
			continue
		}
		if spacePending && out.Len() > 0 {
			out.WriteByte(' ')
		}
		spacePending = false
		out.WriteRune(unicode.ToLower(runeValue))
		index += width
	}
	return strings.TrimSpace(out.String())
}

// canonicalDDL removes only formatting which SQLite itself is free to add
// around punctuation when it stores sqlite_master.sql. Unlike substring
// checks, equality against these fingerprints cannot be bypassed with a
// harmless-looking token such as "WHEN 0" or "OR 1".
func canonicalDDL(value string) string {
	value = canonicalSQL(value)
	// Apply SQLite's punctuation formatting normalization only to unquoted
	// regions. In particular, a comma or parenthesis inside a string literal
	// must remain byte-for-byte part of that literal.
	var out strings.Builder
	start := 0
	for index := 0; index < len(value); {
		quote := value[index]
		if quote != '\'' && quote != '"' && quote != '`' && quote != '[' {
			index++
			continue
		}
		out.WriteString(canonicalDDLPunctuation(value[start:index]))
		end := index + 1
		for end < len(value) {
			if quote == '[' {
				if value[end] != ']' {
					end++
					continue
				}
			} else if value[end] != quote {
				end++
				continue
			}
			if end+1 < len(value) && value[end+1] == value[end] {
				end += 2
				continue
			}
			end++
			break
		}
		out.WriteString(value[index:end])
		index = end
		start = end
	}
	out.WriteString(canonicalDDLPunctuation(value[start:]))
	return out.String()
}

func canonicalDDLPunctuation(value string) string {
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
		"transaction_archive_amounts": canonicalDDL(fmt.Sprintf(`CREATE TABLE transaction_archive_amounts (
			transaction_id INTEGER PRIMARY KEY,
			amount INTEGER NOT NULL CHECK(amount > %d),
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`, validation.MaxTransactionAmount)),
		"transaction_image_archive": canonicalDDL(fmt.Sprintf(`CREATE TABLE transaction_image_archive (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL CHECK(length(CAST(filename AS BLOB)) <= %d),
			data BLOB NOT NULL CHECK(length(data) BETWEEN 0 AND %d),
			mime_type TEXT NOT NULL CHECK(length(CAST(mime_type AS BLOB)) <= %d),
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE
		)`, models.MaxArchivedImageMetadataBytes, models.MaxArchivedImageBytes, models.MaxArchivedImageMetadataBytes)),
		"tags": canonicalDDL(`CREATE TABLE tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER DEFAULT NULL,
			level INTEGER NOT NULL DEFAULT 1 CHECK(level IN (1, 2, 3)),
			legacy_duplicate INTEGER NOT NULL DEFAULT 0 CHECK(legacy_duplicate IN (0, 1)),
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
// Recognition is based on the complete immutable DDL/object family, not a
// mutable marker row/table inside the database. The migrated current objects
// are validated separately by validateFullLedgerSchema.
func isSupportedLegacyCurrentLayout(target schemaQueryer) (bool, error) {
	return isSupportedLegacyCurrentLayoutContext(context.Background(), target)
}

func isSupportedLegacyCurrentLayoutContext(ctx context.Context, target schemaQueryer) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var markerCount int
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name='omni_legacy_schema_compat'").Scan(&markerCount); err != nil {
		return false, err
	}
	if markerCount != 0 {
		// The former marker is deliberately not trusted. A forged same-name
		// object must force strict current validation, never grant a bypass.
		return false, nil
	}
	legacyTransactionDefinition := canonicalDDL(`CREATE TABLE transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account TEXT NOT NULL,
			date DATETIME NOT NULL,
			item TEXT NOT NULL,
			type TEXT NOT NULL,
			amount INTEGER NOT NULL,
			balance INTEGER NOT NULL DEFAULT 0,
			memo TEXT DEFAULT ''
		)`)
	legacyImageDefinition := canonicalDDL(`CREATE TABLE transaction_images (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transaction_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			data BLOB NOT NULL,
			mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	var transactionDefinition, imageDefinition sql.NullString
	if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='transactions'").Scan(&transactionDefinition); err != nil || !transactionDefinition.Valid {
		return false, nil
	}
	if canonicalDDL(transactionDefinition.String) != legacyTransactionDefinition {
		return false, nil
	}
	if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='transaction_images'").Scan(&imageDefinition); err != nil || !imageDefinition.Valid {
		return false, nil
	}
	currentImageDefinition := expectedLedgerTableDefinitions()["transaction_images"]
	if canonicalDDL(imageDefinition.String) != legacyImageDefinition && canonicalDDL(imageDefinition.String) != currentImageDefinition {
		return false, nil
	}
	if canonicalDDL(imageDefinition.String) == currentImageDefinition {
		// A sqlite_master DDL string is not a provenance marker: writable_schema
		// can forge it while leaving a weaker physical table behind. When this
		// compatibility path sees an already-rebuilt image table, verify its
		// columns, foreign key and orphan rows before allowing the legacy
		// transaction tables to remain relaxed.
		if err := validateCurrentTransactionImagesContext(ctx, target); err != nil {
			return false, err
		}
	}
	// The legacy family contains exactly these two user tables. Indexes/triggers
	// created by the normal migration are checked by validateFullLedgerSchema;
	// allowing them here is necessary when reopening a successfully migrated
	// version-2 image whose old transaction tables remain unchanged.
	for _, table := range []string{"transactions", "transaction_images"} {
		var count int
		if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			return false, err
		}
	}
	return true, nil
}

func isSupportedLegacyPreMigrationLayout(target schemaQueryer) (bool, error) {
	return isSupportedLegacyPreMigrationLayoutContext(context.Background(), target)
}

func isSupportedLegacyPreMigrationLayoutContext(ctx context.Context, target schemaQueryer) (bool, error) {
	legacy, err := isSupportedLegacyCurrentLayoutContext(ctx, target)
	if err != nil || !legacy {
		return legacy, err
	}
	legacyImages, err := isLegacyTransactionImagesLayoutContext(ctx, target)
	if err != nil || !legacyImages {
		return false, err
	}
	var extraTables, extraObjects int
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate+" AND name NOT IN ('transactions', 'transaction_images')").Scan(&extraTables); err != nil {
		return false, err
	}
	if err := target.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type IN ('index', 'trigger', 'view')
		  AND (type != 'index' OR sql IS NOT NULL)`).Scan(&extraObjects); err != nil {
		return false, err
	}
	return extraTables == 0 && extraObjects == 0, nil
}

func isLegacyTransactionImagesLayout(target schemaQueryer) (bool, error) {
	return isLegacyTransactionImagesLayoutContext(context.Background(), target)
}

func isLegacyTransactionImagesLayoutContext(ctx context.Context, target schemaQueryer) (bool, error) {
	var definition sql.NullString
	if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='transaction_images'").Scan(&definition); err != nil || !definition.Valid {
		return false, nil
	}
	expected := canonicalDDL(`CREATE TABLE transaction_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transaction_id INTEGER NOT NULL,
		filename TEXT NOT NULL,
		data BLOB NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	return canonicalDDL(definition.String) == expected, nil
}

func validateInternalSQLiteTables(target schemaQueryer) error {
	return validateInternalSQLiteTablesContext(context.Background(), target)
}

func validateInternalSQLiteTablesContext(ctx context.Context, target schemaQueryer) error {
	expected := map[string]string{
		"sqlite_sequence": canonicalDDL("CREATE TABLE sqlite_sequence(name,seq)"),
		"sqlite_stat1":    canonicalDDL("CREATE TABLE sqlite_stat1(tbl,idx,stat)"),
		"sqlite_stat4":    canonicalDDL("CREATE TABLE sqlite_stat4(tbl,idx,neq,nlt,ndlt,sample)"),
	}
	rows, err := target.QueryContext(ctx, "SELECT name, sql FROM sqlite_master WHERE type='table' AND name LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var definition sql.NullString
		if err := rows.Scan(&name, &definition); err != nil {
			return err
		}
		want, ok := expected[name]
		if !ok || !definition.Valid || canonicalDDL(definition.String) != want {
			return fmt.Errorf("unexpected SQLite internal table %s", name)
		}
	}
	return rows.Err()
}

func requireColumns(target schemaQueryer, table string, required []string) error {
	return requireColumnsContext(context.Background(), target, table, required)
}

func requireColumnsContext(ctx context.Context, target schemaQueryer, table string, required []string) error {
	rows, err := target.QueryContext(ctx, "PRAGMA table_info("+table+")")
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
	return validateFullLedgerSchemaContext(context.Background(), target, strictConstraints)
}

func validateFullLedgerSchemaContext(ctx context.Context, target schemaQueryer, strictConstraints bool) error {
	allowedTables := map[string]bool{
		"transactions": true, "transaction_links": true, "transaction_images": true,
		"transaction_archive_amounts": true, "transaction_image_archive": true,
		"tags": true, "transaction_tags": true, "ai_transaction_idempotency": true,
		"ai_daily_transaction_usage": true, "settings": true,
	}
	rows, err := target.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate)
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
		"transactions":                {"id", "account", "date", "item", "type", "amount", "balance", "memo"},
		"transaction_links":           {"parent_id", "child_id"},
		"transaction_images":          {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"transaction_archive_amounts": {"transaction_id", "amount"},
		"transaction_image_archive":   {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"tags":                        {"id", "name", "parent_id", "level", "legacy_duplicate"},
		"transaction_tags":            {"transaction_id", "tag_id"},
		"ai_transaction_idempotency":  {"credential_id", "idempotency_key_sha256", "request_sha256", "transaction_id", "response_account", "response_date", "created_at"},
		"ai_daily_transaction_usage":  {"credential_id", "utc_date", "successful_creates"},
		"settings":                    {"key", "value"},
	}
	for table, columns := range requiredColumns {
		if err := requireColumnsContext(ctx, target, table, columns); err != nil {
			return fmt.Errorf("full schema: %w", err)
		}
	}
	objects := []struct{ typ, name string }{
		{"index", "idx_transactions_account"}, {"index", "idx_transactions_account_date_id"},
		{"index", "idx_transactions_date"}, {"index", "idx_transactions_item"}, {"index", "idx_transactions_memo"},
		{"index", "idx_transaction_links_child_id"}, {"index", "idx_transaction_images_txid"},
		{"index", "idx_transaction_image_archive_txid"},
		{"index", "idx_tags_parent"}, {"index", "idx_tags_root_name_unique"},
		{"index", "idx_transaction_tags_txid"}, {"index", "idx_transaction_tags_tagid"},
		{"index", "idx_ai_idempotency_credential_key"}, {"index", "idx_ai_idempotency_transaction"},
		{"index", "idx_ai_daily_usage_credential_date"},
		{"trigger", "trg_transaction_images_quota_insert"}, {"trigger", "trg_transaction_images_immutable_update"},
		{"trigger", "trg_transaction_image_archive_quota_insert"},
		{"trigger", "validate_transactions_amount_insert"}, {"trigger", "validate_transactions_amount_update"},
	}
	allowedPersistentObjects := make(map[string]struct{}, len(objects))
	for _, object := range objects {
		allowedPersistentObjects[object.typ+"\x00"+object.name] = struct{}{}
	}
	// Required-object checks below are not sufficient: an attacker could add
	// a persistent trigger, index, or view that changes ledger behavior while
	// retaining every required object. The complete current schema has an exact
	// object family, excluding only SQLite's internal autoindexes.
	persistentRows, err := target.QueryContext(ctx, `
		SELECT type, name FROM sqlite_master
		WHERE type IN ('index', 'trigger', 'view')
		  AND (type != 'index' OR sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	for persistentRows.Next() {
		var typ, name string
		if err := persistentRows.Scan(&typ, &name); err != nil {
			_ = persistentRows.Close()
			return err
		}
		if _, ok := allowedPersistentObjects[typ+"\x00"+name]; !ok {
			_ = persistentRows.Close()
			return fmt.Errorf("unexpected ledger persistent object %s %s", typ, name)
		}
	}
	if err := persistentRows.Close(); err != nil {
		return err
	}
	for _, object := range objects {
		var count int
		if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?", object.typ, object.name).Scan(&count); err != nil {
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
		"transaction_image_archive":  {"idx_transaction_image_archive_txid"},
		"tags":                       {"idx_tags_parent", "idx_tags_root_name_unique"},
		"transaction_tags":           {"idx_transaction_tags_txid", "idx_transaction_tags_tagid"},
		"ai_transaction_idempotency": {"idx_ai_idempotency_credential_key", "idx_ai_idempotency_transaction"},
		"ai_daily_transaction_usage": {"idx_ai_daily_usage_credential_date"},
	} {
		for _, index := range indexes {
			var tableName string
			if err := target.QueryRowContext(ctx, "SELECT tbl_name FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&tableName); err != nil || tableName != table {
				return fmt.Errorf("index %s is not attached to %s", index, table)
			}
		}
	}
	if err := validateRootTagIndexContext(ctx, target); err != nil {
		return err
	}
	if err := validateIndexShapesContext(ctx, target); err != nil {
		return err
	}
	if err := validateTriggerDefinitionsContext(ctx, target); err != nil {
		return err
	}
	if err := validateCurrentColumnDefinitionsContext(ctx, target, !strictConstraints); err != nil {
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
		if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil || !definition.Valid {
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
		"transaction_archive_amounts": {
			{referenced: "transactions", from: "transaction_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		},
		"transaction_image_archive": {
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
		rows, err := target.QueryContext(ctx, "PRAGMA foreign_key_list("+table+")")
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
	rows, err = target.QueryContext(ctx, "PRAGMA foreign_key_check")
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
	return validateCurrentColumnDefinitionsContext(context.Background(), target, allowLegacyTables)
}

func validateCurrentColumnDefinitionsContext(ctx context.Context, target schemaQueryer, allowLegacyTables bool) error {
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
		"transaction_archive_amounts": {
			{name: "transaction_id", columnType: "INTEGER", primaryKey: 1},
			{name: "amount", columnType: "INTEGER", notNull: 1},
		},
		"transaction_image_archive": {
			{name: "id", columnType: "INTEGER", primaryKey: 1},
			{name: "transaction_id", columnType: "INTEGER", notNull: 1},
			{name: "filename", columnType: "TEXT", notNull: 1},
			{name: "data", columnType: "BLOB", notNull: 1},
			{name: "mime_type", columnType: "TEXT", notNull: 1},
			{name: "created_at", columnType: "DATETIME", defaultValue: "current_timestamp"},
		},
		"tags": {
			{name: "id", columnType: "INTEGER", primaryKey: 1},
			{name: "name", columnType: "TEXT", notNull: 1},
			{name: "parent_id", columnType: "INTEGER", defaultValue: "null"},
			{name: "level", columnType: "INTEGER", notNull: 1, defaultValue: "1"},
			{name: "legacy_duplicate", columnType: "INTEGER", notNull: 1, defaultValue: "0"},
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
		rows, err := target.QueryContext(ctx, "PRAGMA table_info("+table+")")
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
	return validateTriggerDefinitionsContext(context.Background(), target)
}

func validateTriggerDefinitionsContext(ctx context.Context, target schemaQueryer) error {
	expected := map[string]string{
		"trg_transaction_images_quota_insert": canonicalDDL(fmt.Sprintf(`CREATE TRIGGER trg_transaction_images_quota_insert
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
			END`, models.MaxImageBytes, models.MaxImagesPerTransaction, models.MaxImageBytesPerTransaction, models.MaxImageBytesPerAccount, models.MaxImageBytesDatabase)),
		"trg_transaction_image_archive_quota_insert": canonicalDDL(fmt.Sprintf(`CREATE TRIGGER trg_transaction_image_archive_quota_insert
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
			models.MaxImageBytesPerAccount, models.MaxImageBytesDatabase)),
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
		if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&definition); err != nil || !definition.Valid {
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
	return validateIndexShapesContext(context.Background(), target)
}

func validateIndexShapesContext(ctx context.Context, target schemaQueryer) error {
	expected := map[string]struct {
		table   string
		unique  bool
		partial bool
		cols    []string
		ddl     string
	}{
		"idx_transactions_account": {
			table: "transactions", cols: []string{"account"},
			ddl: "CREATE INDEX idx_transactions_account ON transactions(account)",
		},
		"idx_transactions_account_date_id": {
			table: "transactions", cols: []string{"account", "date", "id"},
			ddl: "CREATE INDEX idx_transactions_account_date_id ON transactions(account,date,id)",
		},
		"idx_transactions_date": {
			table: "transactions", cols: []string{"date"},
			ddl: "CREATE INDEX idx_transactions_date ON transactions(date)",
		},
		"idx_transactions_item": {
			table: "transactions", cols: []string{"item"},
			ddl: "CREATE INDEX idx_transactions_item ON transactions(item)",
		},
		"idx_transactions_memo": {
			table: "transactions", cols: []string{"memo"},
			ddl: "CREATE INDEX idx_transactions_memo ON transactions(memo)",
		},
		"idx_transaction_links_child_id": {
			table: "transaction_links", cols: []string{"child_id"},
			ddl: "CREATE INDEX idx_transaction_links_child_id ON transaction_links(child_id)",
		},
		"idx_transaction_images_txid": {
			table: "transaction_images", cols: []string{"transaction_id"},
			ddl: "CREATE INDEX idx_transaction_images_txid ON transaction_images(transaction_id)",
		},
		"idx_transaction_image_archive_txid": {
			table: "transaction_image_archive", cols: []string{"transaction_id"},
			ddl: "CREATE INDEX idx_transaction_image_archive_txid ON transaction_image_archive(transaction_id)",
		},
		"idx_tags_parent": {
			table: "tags", cols: []string{"parent_id"},
			ddl: "CREATE INDEX idx_tags_parent ON tags(parent_id)",
		},
		"idx_tags_root_name_unique": {
			table: "tags", unique: true, partial: true, cols: []string{"name"},
			ddl: "CREATE UNIQUE INDEX idx_tags_root_name_unique ON tags(name) WHERE parent_id IS NULL AND legacy_duplicate = 0",
		},
		"idx_transaction_tags_txid": {
			table: "transaction_tags", cols: []string{"transaction_id"},
			ddl: "CREATE INDEX idx_transaction_tags_txid ON transaction_tags(transaction_id)",
		},
		"idx_transaction_tags_tagid": {
			table: "transaction_tags", cols: []string{"tag_id"},
			ddl: "CREATE INDEX idx_transaction_tags_tagid ON transaction_tags(tag_id)",
		},
		"idx_ai_idempotency_credential_key": {
			table: "ai_transaction_idempotency", unique: true, cols: []string{"credential_id", "idempotency_key_sha256"},
			ddl: "CREATE UNIQUE INDEX idx_ai_idempotency_credential_key ON ai_transaction_idempotency(credential_id,idempotency_key_sha256)",
		},
		"idx_ai_idempotency_transaction": {
			table: "ai_transaction_idempotency", unique: true, cols: []string{"transaction_id"},
			ddl: "CREATE UNIQUE INDEX idx_ai_idempotency_transaction ON ai_transaction_idempotency(transaction_id)",
		},
		"idx_ai_daily_usage_credential_date": {
			table: "ai_daily_transaction_usage", unique: true, cols: []string{"credential_id", "utc_date"},
			ddl: "CREATE UNIQUE INDEX idx_ai_daily_usage_credential_date ON ai_daily_transaction_usage(credential_id,utc_date)",
		},
	}
	for name, want := range expected {
		var tableName string
		var definition sql.NullString
		if err := target.QueryRowContext(ctx, "SELECT tbl_name, sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&tableName, &definition); err != nil {
			return fmt.Errorf("index %s definition is unavailable: %w", name, err)
		}
		if tableName != want.table {
			return fmt.Errorf("index %s is attached to %s, want %s", name, tableName, want.table)
		}
		if !definition.Valid || canonicalDDL(definition.String) != canonicalDDL(want.ddl) {
			return fmt.Errorf("index %s DDL is not the current allowlisted definition", name)
		}
		rows, err := target.QueryContext(ctx, "PRAGMA index_list("+want.table+")")
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
				found = unique == boolToInt(want.unique) && partial == boolToInt(want.partial)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("index %s has unexpected uniqueness", name)
		}
		rows, err = target.QueryContext(ctx, "PRAGMA index_xinfo("+strconv.Quote(name)+")")
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var seq, cid, descending, key int
			var column, collation sql.NullString
			if err := rows.Scan(&seq, &cid, &column, &descending, &collation, &key); err != nil {
				_ = rows.Close()
				return err
			}
			if key == 0 {
				continue
			}
			if seq != len(columns) || descending != 0 || !column.Valid || !collation.Valid || !strings.EqualFold(collation.String, "BINARY") {
				_ = rows.Close()
				return fmt.Errorf("index %s has unexpected collation or sort order", name)
			}
			columns = append(columns, column.String)
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
	return validateRootTagIndexContext(context.Background(), target)
}

func validateRootTagIndexContext(ctx context.Context, target schemaQueryer) error {
	var objectType, tableName string
	var definition sql.NullString
	if err := target.QueryRowContext(ctx, `
		SELECT type, tbl_name, sql FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_tags_root_name_unique'`).Scan(&objectType, &tableName, &definition); err != nil {
		return fmt.Errorf("rootタグ一意index定義の検査に失敗しました: %w", err)
	}
	if objectType != "index" || tableName != "tags" || !definition.Valid {
		return fmt.Errorf("rootタグ一意indexの対象が不正です")
	}
	canonicalDefinition := canonicalDDL(definition.String)
	if canonicalDefinition != canonicalDDL("create unique index idx_tags_root_name_unique on tags(name) where parent_id is null and legacy_duplicate = 0") {
		return fmt.Errorf("rootタグ一意indexの定義が不正です: %q", definition.String)
	}

	rows, err := target.QueryContext(ctx, "PRAGMA index_list(tags)")
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

	rows, err = target.QueryContext(ctx, `PRAGMA index_info("idx_tags_root_name_unique")`)
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
	return i.CreateSnapshotContext(context.Background(), snapshotDir)
}

// CreateSnapshotContext is the request-bound snapshot capability. Cancellation
// is observed while waiting for process-wide validation admission and during
// backup/validation; once the public name is atomically published, the caller
// is allowed to finish the durable completion boundary.
func (i *Instance) CreateSnapshotContext(ctx context.Context, snapshotDir string) (string, error) {
	if i == nil {
		return "", fmt.Errorf("データベースinstanceが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := acquireSnapshotValidation(ctx); err != nil {
		return "", err
	}
	defer snapshotValidationAdmissionRelease()
	return i.createSnapshot(ctx, snapshotDir)
}

// createSnapshot performs the copy while holding the database lock.  It is
// called by CreateSnapshot and the auto-snapshot worker, both of which hold a
// read lock on snapshotLifecycle for the duration of the operation.
func (i *Instance) createSnapshot(ctx context.Context, snapshotDir string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	transactionLock, err := acquireSnapshotTransactionLock(ctx, snapshotDir)
	if err != nil {
		return "", fmt.Errorf("スナップショット世代管理lock取得エラー: %w", err)
	}
	defer transactionLock.release()
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショット世代管理directory境界エラー: %w", err)
	}
	if err := cleanupSnapshotPruneManifestTemps(context.WithoutCancel(ctx), snapshotDir, transactionLock); err != nil {
		return "", fmt.Errorf("スナップショット世代退避journal一時ファイル復旧エラー: %w", err)
	}
	// A fixed-name prune journal is the durable ownership record for the
	// retention transaction. Resolve it before creating a new staging image so
	// a retry can never replace unresolved victim state from an earlier process.
	// Recovery is a durability operation and therefore completes even when the
	// request that happened to discover it is canceled.
	if err := recoverSnapshotPruneTransactionAtStart(context.WithoutCancel(ctx), snapshotDir, transactionLock); err != nil {
		return "", fmt.Errorf("スナップショット世代管理トランザクション復旧エラー: %w", err)
	}
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショット世代管理directory境界エラー: %w", err)
	}

	budget, err := snapshotMaxTotalBytes()
	if err != nil {
		return "", err
	}
	requiredBytes, err := sqliteDatabaseSizeContext(ctx, currentDB)
	if err != nil {
		return "", fmt.Errorf("スナップショット必要容量取得エラー: %w", err)
	}
	if requiredBytes > budget {
		return "", fmt.Errorf("スナップショット必要容量 %d bytes が総容量上限 %d bytes を超えます", requiredBytes, budget)
	}
	// Do not prune existing generations before the new image is complete. A
	// canceled backup, failed validation, or failed fsync must leave the
	// existing public snapshot set untouched. Retention is finalized only
	// after the validated staging file has been atomically published below.

	// The backup is built under a hidden, random staging name. A process crash
	// can therefore leave only an ignored staging artifact; the public .db name
	// is published with one atomic rename after validation and fsync.
	stagingPath, err := randomDatabasePath(snapshotDir, ".omni-money-snapshot-staging-")
	if err != nil {
		return "", fmt.Errorf("スナップショットstaging先を作成できません: %w", err)
	}
	stagingPlaceholder, err := transactionLock.createPlaceholder(stagingPath)
	if err != nil {
		return "", fmt.Errorf("スナップショットstaging placeholder作成エラー: %w", err)
	}
	defer stagingPlaceholder.Close()
	stagingLive := true
	defer func() {
		if stagingLive {
			_ = removeSnapshotTransactionSQLiteFiles(transactionLock, stagingPath)
			_ = transactionLock.sync()
		}
	}()

	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := backupSQLiteDatabaseToPlaceholder(ctx, currentOpener, currentDB, stagingPath, stagingPlaceholder); err != nil {
		return "", fmt.Errorf("スナップショット作成エラー: %w", err)
	}
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショットstaging directory境界エラー: %w", err)
	}
	// The locked os.Root created and hardened the placeholder before Backup.
	// Pin that resulting inode before any further pathname-based opener call;
	// the descriptor is retained through publication below.
	stagingFile, err := transactionLock.openArtifactWritable(stagingPath)
	if err != nil {
		return "", fmt.Errorf("検証済みスナップショットdescriptorを開けません: %w", err)
	}
	defer stagingFile.Close()
	stagedInfo, err := stagingFile.Stat()
	if err != nil || !validSnapshotFile(stagedInfo) || !validSnapshotMode(stagedInfo, currentOpener.Encrypted()) {
		if err == nil {
			err = errors.New("staging descriptor is not a private regular file")
		}
		return "", fmt.Errorf("スナップショットstaging descriptor検査エラー: %w", err)
	}
	stagedDigest, err := digestOpenFileContext(ctx, stagingFile)
	if err != nil {
		return "", fmt.Errorf("スナップショットstaging digest検査エラー: %w", err)
	}
	if err := assertSnapshotTransactionArtifact(transactionLock, stagingFile, stagingPath); err != nil {
		return "", fmt.Errorf("スナップショットstaging identity検査エラー: %w", err)
	}
	if err := syncSnapshotTransactionFile(transactionLock, stagingFile, stagingPath); err != nil {
		return "", fmt.Errorf("スナップショットstaging fsyncエラー: %w", err)
	}
	created, err := currentOpener.Open(ctx, stagingPath, securedb.ReadOnly)
	if err != nil {
		return "", fmt.Errorf("作成済みスナップショットを開けません: %w", err)
	}
	validationErr := i.validateSnapshotDatabaseContext(ctx, created, stagingPath)
	closeErr := created.Close()
	if validationErr != nil || closeErr != nil {
		return "", errors.Join(errors.New("作成済みスナップショットの検証に失敗しました"), validationErr, closeErr)
	}
	if err := syncSnapshotTransactionFile(transactionLock, stagingFile, stagingPath); err != nil {
		return "", fmt.Errorf("検証済みスナップショットstaging fsyncエラー: %w", err)
	}
	if err := assertSnapshotTransactionArtifact(transactionLock, stagingFile, stagingPath); err != nil {
		return "", fmt.Errorf("検証済みスナップショットstaging identity再検証エラー: %w", err)
	}

	// Keep the timestamp-oriented public naming used by the desktop UI, while
	// adding cryptographic randomness so a collision cannot replace an older
	// snapshot. The random path helper checks existence before the atomic rename.
	timestamp := time.Now().UTC().Format("20060102_150405.000000000")
	timestamp = strings.ReplaceAll(timestamp, ".", "_")
	randomSuffix := make([]byte, 8)
	if _, err := cryptorand.Read(randomSuffix); err != nil {
		return "", fmt.Errorf("スナップショット名生成エラー: %w", err)
	}
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("omni_money_%s_%s.db", timestamp, hex.EncodeToString(randomSuffix)))
	clear(randomSuffix)
	if _, err := transactionLock.lstat(snapshotPath); err == nil {
		return "", errors.New("スナップショット名が衝突しました")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("スナップショット名検査エラー: %w", err)
	}
	if stagedInfo.Size() < 0 || stagedInfo.Size() > budget {
		return "", fmt.Errorf("スナップショットstaging容量が総容量上限を超えます")
	}
	plannedQuarantine, err := planSnapshotQuarantineContext(ctx, snapshotDir, 30, budget, stagedInfo.Size(), transactionLock)
	if err != nil {
		return "", fmt.Errorf("スナップショット世代退避計画エラー: %w", err)
	}
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショット世代退避directory境界エラー: %w", err)
	}
	var pruneManifest snapshotPruneManifest
	if len(plannedQuarantine) > 0 {
		pruneManifest, err = newSnapshotPruneManifest(snapshotDir, filepath.Base(snapshotPath), stagedDigest, plannedQuarantine)
		if err != nil {
			return "", fmt.Errorf("スナップショット世代退避journal計画エラー: %w", err)
		}
	}
	quarantined, err := applySnapshotQuarantine(plannedQuarantine, snapshotDir, transactionLock)
	if err != nil {
		return "", fmt.Errorf("スナップショット世代退避エラー: %w", err)
	}
	pruneJournalWritten := false
	if len(quarantined) > 0 {
		manifestErr := createSnapshotPruneManifest(snapshotDir, pruneManifest, transactionLock)
		if manifestErr != nil {
			rollbackErr := rollbackSnapshotQuarantine(quarantined, snapshotDir, transactionLock)
			return "", errors.Join(fmt.Errorf("スナップショット世代退避journal作成エラー: %w", manifestErr), rollbackErr)
		}
		pruneJournalWritten = true
	}
	rollbackQuarantine := func(cause error) error {
		rollbackErr := rollbackSnapshotQuarantine(quarantined, snapshotDir, transactionLock)
		if pruneJournalWritten && rollbackErr == nil {
			rollbackErr = removeSnapshotPruneManifest(snapshotDir, transactionLock)
		}
		return errors.Join(cause, rollbackErr)
	}
	if err := ctx.Err(); err != nil {
		return "", rollbackQuarantine(err)
	}
	if err := assertSnapshotTransactionArtifact(transactionLock, stagingFile, stagingPath); err != nil {
		return "", rollbackQuarantine(fmt.Errorf("スナップショットstaging配置前identity検証エラー: %w", err))
	}
	if err := transactionLock.verify(); err != nil {
		return "", rollbackQuarantine(fmt.Errorf("スナップショット公開directory境界エラー: %w", err))
	}
	if err := transactionLock.publishSnapshot(stagingPath, snapshotPath); err != nil {
		return "", rollbackQuarantine(fmt.Errorf("スナップショット公開エラー: %w", err))
	}
	stagingLive = false
	// From this point onward the public name is complete and must not be
	// retracted merely because the HTTP client canceled. Finish the durable
	// completion boundary with cancellation detached from the request.
	durableCtx := context.WithoutCancel(ctx)
	if err := assertSnapshotTransactionArtifact(transactionLock, stagingFile, snapshotPath); err != nil {
		cleanupErr := transactionLock.removeArtifact(snapshotPath)
		return "", errors.Join(fmt.Errorf("公開済みスナップショットidentity検証エラー: %w", err), cleanupErr)
	}
	if err := transactionLock.verify(); err != nil {
		cleanupErr := transactionLock.removeArtifact(snapshotPath)
		return "", errors.Join(fmt.Errorf("公開済みスナップショットdirectory境界エラー: %w", err), cleanupErr)
	}
	publishedDigest, err := digestOpenFileContext(durableCtx, stagingFile)
	if err != nil {
		cleanupErr := transactionLock.removeArtifact(snapshotPath)
		return "", errors.Join(fmt.Errorf("公開済みスナップショットdigest検証エラー: %w", err), cleanupErr)
	}
	if !strings.EqualFold(stagedDigest, publishedDigest) {
		cleanupErr := transactionLock.removeArtifact(snapshotPath)
		return "", errors.Join(errors.New("公開済みスナップショットが検証済みstagingと一致しません"), cleanupErr)
	}
	if err := transactionLock.sync(); err != nil {
		return "", fmt.Errorf("スナップショットdirectory fsyncエラー: %w", err)
	}
	if pruneJournalWritten {
		pruneManifest, found, manifestErr := readSnapshotPruneManifest(snapshotDir, transactionLock)
		if manifestErr != nil || !found {
			if manifestErr == nil {
				manifestErr = errors.New("snapshot prune manifest disappeared")
			}
			return "", fmt.Errorf("スナップショット世代退避journal再読込エラー: %w", manifestErr)
		}
		pruneManifest.Phase = "published"
		if manifestErr := writeSnapshotPruneManifest(snapshotDir, pruneManifest, transactionLock); manifestErr != nil {
			return "", fmt.Errorf("スナップショット世代退避journal確定エラー: %w", manifestErr)
		}
	}
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショット完了directory境界エラー: %w", err)
	}
	if err := finalizeSnapshotQuarantine(durableCtx, quarantined, snapshotDir, transactionLock); err != nil {
		return "", fmt.Errorf("スナップショット世代削除エラー: %w", err)
	}
	if pruneJournalWritten {
		if err := removeSnapshotPruneManifest(snapshotDir, transactionLock); err != nil {
			return "", fmt.Errorf("スナップショット世代退避journal削除エラー: %w", err)
		}
	}
	if err := transactionLock.verify(); err != nil {
		return "", fmt.Errorf("スナップショット返却directory境界エラー: %w", err)
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
// potentially expensive copy/open/integrity operation observes ctx, and the
// process-wide admission gate bounds temporary disk/I/O across tenants.
func (i *Instance) ListSnapshotsContext(ctx context.Context, snapshotDir string) ([]string, error) {
	return i.listSnapshotsContext(ctx, snapshotDir, nil)
}

// listSnapshotsContext keeps the snapshot transaction lock from stale
// artifact cleanup through the last validation candidate removal. The
// checkpoint is used only by cross-process regressions to stop at the exact
// candidate lifetime boundary.
func (i *Instance) listSnapshotsContext(ctx context.Context, snapshotDir string, candidateCreated func(string) error) ([]string, error) {
	if i == nil {
		return nil, fmt.Errorf("データベースinstanceが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	i.snapshotLifecycle.RLock()
	defer i.snapshotLifecycle.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := acquireSnapshotValidation(ctx); err != nil {
		return nil, err
	}
	defer snapshotValidationAdmissionRelease()
	if snapshotDir == "" {
		snapshotDir = i.getSnapshotDir()
	}
	if err := validateSnapshotDirectory(snapshotDir); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("スナップショットディレクトリが安全ではありません: %w", err)
	}
	transactionLock, err := acquireSnapshotTransactionLock(ctx, snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("スナップショットtransaction lockエラー: %w", err)
	}
	defer transactionLock.release()
	if err := transactionLock.verify(); err != nil {
		return nil, fmt.Errorf("スナップショットtransaction boundaryエラー: %w", err)
	}
	if err := cleanupSnapshotStagingDirLocked(ctx, snapshotDir, transactionLock); err != nil {
		return nil, fmt.Errorf("スナップショットstaging cleanupエラー: %w", err)
	}

	i.mu.RLock()
	opener := i.opener
	encrypted := opener != nil && opener.Encrypted()
	i.mu.RUnlock()
	if opener == nil {
		return nil, fmt.Errorf("データベースopenerが初期化されていません")
	}
	entries, err := transactionLock.readDir(ctx, maxSnapshotDirectoryEntries)
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
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if err := validateSnapshotName(entry.Name()); err != nil {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		info, err := transactionLock.lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("snapshot entry metadata: %w", err)
		}
		if !validSnapshotFile(info) || !validSnapshotMode(info, encrypted) {
			// Listing is intentionally fail-closed per entry: a stray symlink,
			// hard link, or non-regular file must never become a restore target.
			continue
		}
		checked++
		valid, used, err := i.validateSnapshotEntry(ctx, path, info, encrypted, maxSnapshotValidationWorkBytes-validationBytes, transactionLock, candidateCreated)
		validationBytes += used
		if err != nil {
			return nil, err
		}
		if valid {
			snapshots = append(snapshots, entry.Name())
		}
	}
	sort.Strings(snapshots)
	if err := transactionLock.verify(); err != nil {
		return nil, fmt.Errorf("スナップショットtransaction boundaryが変更されました: %w", err)
	}
	return snapshots, nil
}

// validateSnapshotEntry validates the bytes copied from one no-follow source
// descriptor. Keeping all cleanup in this bounded helper is important: a
// defer in the ListSnapshots loop would retain up to 256 full candidates
// until the whole directory scan returned.
func (i *Instance) validateSnapshotEntry(ctx context.Context, path string, inspected os.FileInfo, encrypted bool, remainingWork int64, transactionLock *snapshotTransactionLock, candidateCreated func(string) error) (valid bool, used int64, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if remainingWork <= 0 {
		return false, 0, errors.New("snapshot validation work budget exhausted")
	}
	file, err := transactionLock.openArtifact(path)
	if err != nil {
		// A pathname replacement/removal is an invalid entry and may be omitted;
		// a stable private path that cannot be opened is infrastructure failure.
		postInfo, statErr := transactionLock.lstat(path)
		if os.IsNotExist(err) || os.IsNotExist(statErr) || statErr == nil && (!validSnapshotFile(postInfo) || !sameSnapshotInfo(inspected, postInfo)) {
			return false, 0, nil
		}
		return false, 0, errors.Join(fmt.Errorf("snapshot source open: %w", err), statErr)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("snapshot source close: %w", closeErr))
			valid = false
		}
	}()
	fdInfo, err := file.Stat()
	if err != nil {
		return false, 0, fmt.Errorf("snapshot source stat: %w", err)
	}
	if fdInfo.Size() < 0 || fdInfo.Size() > maxSnapshotValidationBytes || fdInfo.Size() > remainingWork ||
		!validSnapshotFile(fdInfo) || !validSnapshotMode(fdInfo, encrypted) ||
		!snapshotSourceMatches(inspected, fdInfo) {
		return false, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	used = fdInfo.Size()
	// The descriptor-level checks above are required even for a positive
	// cache hit. In particular, Windows does not expose hard-link count in
	// os.FileInfo, while openSnapshotFile verifies it with the handle.
	candidateFile, candidatePath, err := transactionLock.createTemporary(".omni-money-list-validation-", ".db")
	if err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate create: %w", err)
	}
	candidateClosed := false
	defer func() {
		var closeErr error
		if !candidateClosed {
			closeErr = candidateFile.Close()
		}
		removeErr := removeSnapshotTransactionSQLiteFiles(transactionLock, candidatePath)
		if closeErr != nil || removeErr != nil {
			resultErr = errors.Join(resultErr, closeErr, removeErr)
			valid = false
		}
	}()
	if err := candidateFile.Chmod(0600); err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate chmod: %w", err)
	}
	if err := fileprivacy.Harden(candidateFile); err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate harden: %w", err)
	}
	if candidateCreated != nil {
		if err := candidateCreated(candidatePath); err != nil {
			return false, used, err
		}
	}
	if err := copyFileToOpenBoundedContext(ctx, file, candidateFile, maxSnapshotValidationBytes); err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate copy: %w", err)
	}
	if err := candidateFile.Sync(); err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate sync: %w", err)
	}
	candidateInfo, err := candidateFile.Stat()
	if err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate stat: %w", err)
	}
	candidateDigest, err := digestOpenFileContext(ctx, candidateFile)
	if err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate digest: %w", err)
	}
	if err := candidateFile.Close(); err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate close: %w", err)
	}
	candidateClosed = true
	reopened, validationPath, err := openSnapshotValidationAnchor(transactionLock, candidatePath)
	if err != nil {
		return false, used, fmt.Errorf("snapshot validation candidate reopen: %w", err)
	}
	reopenedInfo, err := reopened.Stat()
	if err != nil {
		_ = reopened.Close()
		return false, used, fmt.Errorf("snapshot validation candidate reopen stat: %w", err)
	}
	if !sameSnapshotInfo(candidateInfo, reopenedInfo) {
		_ = reopened.Close()
		return false, used, errors.New("snapshot validation candidate changed before rooted reopen")
	}
	reopenedDigest, err := digestOpenFileContext(ctx, reopened)
	if err != nil || !strings.EqualFold(candidateDigest, reopenedDigest) {
		_ = reopened.Close()
		return false, used, errors.Join(errors.New("snapshot validation candidate digest changed before rooted reopen"), err)
	}
	defer func() {
		if closeErr := reopened.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("snapshot validation candidate anchor close: %w", closeErr))
			valid = false
		}
	}()
	if err := transactionLock.verify(); err != nil {
		return false, used, fmt.Errorf("snapshot validation transaction boundary: %w", err)
	}
	// Re-check the source descriptor after copying. A same-inode writer or
	// replacement must not cause a partially copied object to be treated as a
	// validated snapshot.
	postFDInfo, err := file.Stat()
	if err != nil {
		return false, used, fmt.Errorf("snapshot source restat: %w", err)
	}
	if !snapshotSourceMatches(fdInfo, postFDInfo) {
		return false, used, nil
	}
	if err := ctx.Err(); err != nil {
		return false, used, err
	}
	if encrypted {
		// validationPath is /proc/self/fd/N or /dev/fd/N on Unix. Feeding that
		// symlink back through RequireEncryptedHeader(path) is both unnecessary
		// and guaranteed to fail its O_NOFOLLOW boundary. Verify the exact
		// digest-bound descriptor instead.
		if headerErr := securedb.RequireEncryptedHeaderFile(reopened); headerErr != nil {
			if boundaryErr := validateListSnapshotCandidateBoundary(transactionLock, candidatePath, reopened, candidateInfo, candidateDigest); boundaryErr != nil {
				return false, used, errors.Join(fmt.Errorf("snapshot validation encrypted header: %w", headerErr), boundaryErr)
			}
			if errors.Is(headerErr, securedb.ErrPlaintextHeader) || errors.Is(headerErr, io.EOF) || errors.Is(headerErr, io.ErrUnexpectedEOF) {
				return false, used, nil
			}
			return false, used, fmt.Errorf("snapshot validation encrypted header: %w", headerErr)
		}
	}
	// The validation copy is already pinned and digest-bound. Immutable mode
	// prevents SQLite/SQLCipher from attempting locks or sidecars beside a
	// Unix descriptor path while preserving the same-object binding.
	db, err := i.opener.Open(ctx, validationPath, securedb.ImmutableReadOnly)
	if err != nil {
		if boundaryErr := validateListSnapshotCandidateBoundary(transactionLock, candidatePath, reopened, candidateInfo, candidateDigest); boundaryErr != nil {
			return false, used, errors.Join(fmt.Errorf("snapshot validation candidate open: %w", err), boundaryErr)
		}
		if err := ctx.Err(); err != nil {
			return false, used, err
		}
		if invalidSnapshotContentError(err) {
			return false, used, nil
		}
		return false, used, fmt.Errorf("snapshot validation candidate open: %w", err)
	}
	if boundaryErr := validateListSnapshotCandidateBoundary(transactionLock, candidatePath, reopened, candidateInfo, candidateDigest); boundaryErr != nil {
		_ = db.Close()
		return false, used, boundaryErr
	}
	validErr := i.validateSnapshotDatabaseContextHeaderValidated(ctx, db, validationPath, encrypted)
	closeErr := db.Close()
	if closeErr != nil {
		return false, used, fmt.Errorf("snapshot validation database close: %w", closeErr)
	}
	if boundaryErr := validateListSnapshotCandidateBoundary(transactionLock, candidatePath, reopened, candidateInfo, candidateDigest); boundaryErr != nil {
		return false, used, errors.Join(validErr, boundaryErr)
	}
	if validErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, used, ctxErr
		}
		if errors.Is(validErr, errInvalidSnapshotContent) {
			return false, used, nil
		}
		return false, used, fmt.Errorf("snapshot validation database: %w", validErr)
	}
	// The path may not have been replaced while its descriptor was being
	// copied/validated. The candidate remains the validated bytes even if the
	// path was briefly swapped away, but do not cache or expose that name.
	postInfo, postErr := transactionLock.lstat(path)
	if postErr != nil {
		if os.IsNotExist(postErr) {
			return false, used, nil
		}
		return false, used, fmt.Errorf("snapshot source final metadata: %w", postErr)
	}
	if !sameSnapshotInfo(fdInfo, postInfo) {
		return false, used, nil
	}
	return true, used, nil
}

func validateListSnapshotCandidateBoundary(transactionLock *snapshotTransactionLock, candidatePath string, anchor *os.File, expected os.FileInfo, expectedDigest string) error {
	if err := transactionLock.verify(); err != nil {
		return fmt.Errorf("snapshot validation transaction boundary: %w", err)
	}
	anchorInfo, err := anchor.Stat()
	if err != nil || !sameSnapshotInfo(expected, anchorInfo) {
		return errors.Join(errors.New("snapshot validation candidate anchor identity changed"), err)
	}
	anchorDigest, err := digestOpenFile(anchor)
	if err != nil || !strings.EqualFold(expectedDigest, anchorDigest) {
		return errors.Join(errors.New("snapshot validation candidate anchor digest changed"), err)
	}
	actualFile, err := transactionLock.openArtifact(candidatePath)
	if err != nil {
		return fmt.Errorf("snapshot validation candidate path: %w", err)
	}
	actual, err := actualFile.Stat()
	if err != nil || !sameSnapshotInfo(expected, actual) {
		return errors.Join(errors.New("snapshot validation candidate identity changed"), err, actualFile.Close())
	}
	actualDigest, err := digestOpenFile(actualFile)
	if err != nil || !strings.EqualFold(expectedDigest, actualDigest) {
		return errors.Join(errors.New("snapshot validation candidate path digest changed"), err, actualFile.Close())
	}
	if err := actualFile.Close(); err != nil {
		return fmt.Errorf("snapshot validation candidate path close: %w", err)
	}
	return nil
}

func invalidSnapshotContentError(err error) bool {
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code == sqlite3.ErrNotADB || sqliteErr.Code == sqlite3.ErrCorrupt
}

var errInvalidSnapshotContent = errors.New("invalid snapshot content")

func sameSnapshotInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode().Perm() == b.Mode().Perm()
}

// assertOpenFileAtPath proves that path still names the exact object held by
// file. It uses the same no-follow opener as snapshot sources, so a reparse or
// symlink substitution cannot pass the check merely by preserving pathname
// metadata.
func assertOpenFileAtPath(file *os.File, path string) error {
	if file == nil {
		return errors.New("candidate descriptor is nil")
	}
	pathFile, err := openSnapshotFile(path)
	if err != nil {
		return err
	}
	defer pathFile.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := pathFile.Stat()
	if err != nil {
		return err
	}
	if !validSnapshotFile(fileInfo) || !validSnapshotFile(pathInfo) || !sameSnapshotInfo(fileInfo, pathInfo) {
		return errors.New("candidate pathname no longer names the validated descriptor")
	}
	return nil
}

// validateSnapshotDatabase is deliberately read-only. Legacy snapshots may
// be migrated only during restore; listing must not mutate an off-host file.
func (i *Instance) validateSnapshotDatabase(target *sql.DB, path string) error {
	return i.validateSnapshotDatabaseContext(context.Background(), target, path)
}

func (i *Instance) validateSnapshotDatabaseContext(ctx context.Context, target *sql.DB, path string) error {
	return i.validateSnapshotDatabaseContextHeaderValidated(ctx, target, path, false)
}

// validateSnapshotDatabaseContextHeaderValidated separates invalid ledger
// content from operational failures. Listing may omit the former, but must
// propagate opener, descriptor, I/O and close failures rather than silently
// presenting an empty snapshot set.
func (i *Instance) validateSnapshotDatabaseContextHeaderValidated(ctx context.Context, target *sql.DB, path string, encryptedHeaderValidated bool) error {
	if target == nil {
		return errors.New("snapshot database is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.opener != nil && i.opener.Encrypted() && !encryptedHeaderValidated {
		if err := securedb.RequireEncryptedHeader(path); err != nil {
			if errors.Is(err, securedb.ErrPlaintextHeader) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: encrypted header: %v", errInvalidSnapshotContent, err)
			}
			return err
		}
	}
	if err := i.checkIntegrityContext(ctx, target); err != nil {
		if snapshotValidationInfrastructureError(err) {
			return err
		}
		return fmt.Errorf("%w: integrity: %v", errInvalidSnapshotContent, err)
	}
	var userTables int
	if err := validateInternalSQLiteTablesContext(ctx, target); err != nil {
		if snapshotValidationInfrastructureError(err) {
			return fmt.Errorf("SQLite internal table validation failed: %w", err)
		}
		return fmt.Errorf("%w: SQLite internal table validation failed: %v", errInvalidSnapshotContent, err)
	}
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate).Scan(&userTables); err != nil {
		if snapshotValidationInfrastructureError(err) {
			return err
		}
		return fmt.Errorf("%w: table count: %v", errInvalidSnapshotContent, err)
	}
	if userTables == 0 {
		return fmt.Errorf("%w: empty database is not a ledger snapshot", errInvalidSnapshotContent)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLedgerSchemaContext(ctx, target, false); err != nil {
		if snapshotValidationInfrastructureError(err) {
			return err
		}
		return fmt.Errorf("%w: ledger schema: %v", errInvalidSnapshotContent, err)
	}
	return nil
}

func snapshotValidationInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code {
	case sqlite3.ErrBusy, sqlite3.ErrLocked, sqlite3.ErrIoErr, sqlite3.ErrCantOpen,
		sqlite3.ErrProtocol, sqlite3.ErrFull, sqlite3.ErrReadonly, sqlite3.ErrInterrupt,
		sqlite3.ErrPerm, sqlite3.ErrAuth, sqlite3.ErrMisuse, sqlite3.ErrNomem:
		return true
	default:
		return false
	}
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
	return i.RestoreSnapshotContext(context.Background(), snapshotDir, snapshotName)
}

// RestoreSnapshotContext keeps request cancellation active until the live
// pathname replacement begins. After that point it completes the durable
// publication/validation boundary so cancellation cannot strand a half-swap.
func (i *Instance) RestoreSnapshotContext(ctx context.Context, snapshotDir, snapshotName string) error {
	if i == nil {
		return fmt.Errorf("データベースinstanceが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// スナップショット名の検証（パストラバーサル防止）。
	// APIから任意の名前が渡り得るため、ディレクトリ区切りや ".." を含む名前、
	// snapshots/ 直下の .db ファイル以外は拒否する。
	if err := validateSnapshotName(snapshotName); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Drain/lifecycle comes before process-wide admission. An auto-snapshot
	// worker may already be marked running while waiting for that admission;
	// taking the gate first would make this restore wait for the worker while
	// the worker waits for the gate.
	i.beginDBLifecycle()
	defer i.endDBLifecycle()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := acquireSnapshotValidation(ctx); err != nil {
		return err
	}
	defer snapshotValidationAdmissionRelease()

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
	sourceDigest, err := digestOpenFileContext(ctx, snapshotFile)
	if err != nil {
		return fmt.Errorf("スナップショットdigest検証エラー: %w", err)
	}
	if err := copyFileToOpenBoundedContext(ctx, snapshotFile, candidateFile, maxSnapshotValidationBytes); err != nil {
		return fmt.Errorf("スナップショット候補コピーエラー: %w", err)
	}
	if err := candidateFile.Sync(); err != nil {
		return fmt.Errorf("復元候補のsyncエラー: %w", err)
	}
	postSourceDigest, err := digestOpenFileContext(ctx, snapshotFile)
	if err != nil || !strings.EqualFold(sourceDigest, postSourceDigest) {
		if err == nil {
			err = errors.New("スナップショットがコピー中に変更されました")
		}
		return fmt.Errorf("スナップショットdigest再検証エラー: %w", err)
	}
	// Keep the original candidate descriptor as the identity anchor. Digesting
	// by pathname here would let a same-account writer substitute another
	// valid database between validation and the eventual replace.
	candidateDigest, err := digestOpenFileContext(ctx, candidateFile)
	if err != nil || !strings.EqualFold(sourceDigest, candidateDigest) {
		if err == nil {
			err = errors.New("復元候補digestがスナップショットと一致しません")
		}
		return fmt.Errorf("復元候補digest検証エラー: %w", err)
	}
	if err := assertOpenFileAtPath(candidateFile, candidatePath); err != nil {
		return fmt.Errorf("復元候補identity検証エラー: %w", err)
	}
	candidateDB, err := i.opener.Open(ctx, candidatePath, securedb.Writable)
	if err != nil {
		return fmt.Errorf("復元候補のDB接続エラー: %w", err)
	}
	if err := i.validateRestoreDatabaseContext(ctx, candidateDB, candidatePath); err != nil {
		_ = candidateDB.Close()
		return err
	}
	if err := checkpointAndCloseContext(ctx, candidateDB, candidatePath); err != nil {
		return fmt.Errorf("復元候補の耐久化エラー: %w", err)
	}
	// Flush through the original O_RDWR identity anchor. On Windows,
	// openSnapshotFile intentionally returns a read-only no-reparse handle and
	// FlushFileBuffers rejects that handle with ACCESS_DENIED. Reusing the
	// creator descriptor also avoids a fresh pathname lookup before the final
	// identity/digest proof.
	if err := syncOpenFileAndDirectory(candidateFile, dir); err != nil {
		return fmt.Errorf("復元候補のfsyncエラー: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Migration/checkpoint may rewrite the candidate in place. Re-digest the
	// same open descriptor and bind the final pathname to that descriptor again
	// before closing it for the platform-specific atomic replace.
	candidateDigest, err = digestOpenFileContext(ctx, candidateFile)
	if err != nil {
		return fmt.Errorf("復元候補descriptor digestエラー: %w", err)
	}
	if err := assertOpenFileAtPath(candidateFile, candidatePath); err != nil {
		return fmt.Errorf("復元候補identity再検証エラー: %w", err)
	}
	candidateIdentity, err := candidateFile.Stat()
	if err != nil {
		return fmt.Errorf("復元候補identity取得エラー: %w", err)
	}
	if err := candidateFile.Close(); err != nil {
		return fmt.Errorf("復元候補のクローズエラー: %w", err)
	}
	candidateFile = nil

	// Close the live handle only after candidate validation. Its WAL is
	// checkpointed first so the live file is a complete database. The original
	// pathname is deliberately kept in place until the replacement is ready:
	// a rename of live -> backup followed by a second rename would expose a
	// missing database if the process were killed at the boundary.
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := i.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("現行DBのcheckpointエラー: %w", err)
	}
	closeErr := i.db.Close()
	i.db = nil
	if closeErr != nil {
		return fmt.Errorf("現行DBのクローズエラー: %w", closeErr)
	}
	// The live handle is now closed and i.db is nil. Ignore request
	// cancellation from this point onward so cleanup, journal, replacement and
	// reopen complete as one durable restore transaction.
	durableCtx := context.WithoutCancel(ctx)
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
	backupFile, err := copyDatabaseFileOpen(currentPath, backupPath)
	if err != nil {
		return fmt.Errorf("現行DB退避コピーエラー: %w", err)
	}
	defer func() {
		if backupFile != nil {
			_ = backupFile.Close()
		}
	}()
	if err := syncOpenFileAndDirectory(backupFile, dir); err != nil {
		return fmt.Errorf("現行DB退避のfsyncエラー: %w", err)
	}
	backupIdentity, err := backupFile.Stat()
	if err != nil {
		return fmt.Errorf("現行DB退避identity検証エラー: %w", err)
	}
	oldDigest, err := digestOpenFileContext(durableCtx, backupFile)
	if err != nil {
		return fmt.Errorf("現行DB退避digest検証エラー: %w", err)
	}
	// Re-prove both generated names from stable descriptor identity/content
	// immediately before publication. The manifest must never be built from a
	// pathname that a same-account process could have replaced after validation.
	if err := assertPathDigest(candidatePath, candidateIdentity, candidateDigest); err != nil {
		return fmt.Errorf("復元候補digest再検証エラー: %w", err)
	}
	if err := assertOpenFileAtPath(backupFile, backupPath); err != nil {
		return fmt.Errorf("現行DB退避digest再検証エラー: %w", err)
	}
	manifest := restoreManifest{
		Version:   restoreManifestVersion,
		Phase:     "prepared",
		Current:   filepath.Base(currentPath),
		Backup:    filepath.Base(backupPath),
		Candidate: filepath.Base(candidatePath),
		OldDigest: oldDigest,
		NewDigest: candidateDigest,
	}
	if err := writeRestoreManifest(currentPath, manifest); err != nil {
		return fmt.Errorf("restore intent journal作成エラー: %w", err)
	}
	// Keep the descriptor-derived identities bound through the final
	// publication boundary as well as the manifest write. If either generated
	// pathname was exchanged after the manifest was prepared, leave the old
	// live file in place and let startup recovery handle the durable journal.
	if err := assertPathDigest(candidatePath, candidateIdentity, candidateDigest); err != nil {
		return fmt.Errorf("復元候補配置前のidentity/digest検証エラー: %w", err)
	}
	if anchorDigest, digestErr := digestOpenFileContext(durableCtx, backupFile); digestErr != nil || !strings.EqualFold(oldDigest, anchorDigest) {
		return fmt.Errorf("現行DB退避配置前のdescriptor digest検証エラー: %w", errors.Join(errors.New("backup descriptor content changed"), digestErr))
	}
	if err := assertOpenFileAtPath(backupFile, backupPath); err != nil {
		return fmt.Errorf("現行DB退避配置前のidentity/digest検証エラー: %w", err)
	}
	if finalBackupIdentity, statErr := backupFile.Stat(); statErr != nil || !sameSnapshotInfo(backupIdentity, finalBackupIdentity) {
		return fmt.Errorf("現行DB退避配置前のidentity検証エラー: %w", errors.Join(errors.New("backup descriptor identity changed"), statErr))
	}
	// ReplaceFileW cannot replace a backup pathname while our anchor handle is
	// open. Close it only at this final boundary; the immediate pathname proof
	// below covers the short close-to-replace interval.
	if err := backupFile.Close(); err != nil {
		return fmt.Errorf("現行DB退避のクローズエラー: %w", err)
	}
	backupFile = nil
	if err := assertPathDigest(backupPath, backupIdentity, oldDigest); err != nil {
		return fmt.Errorf("現行DB退避配置直前のidentity/digest検証エラー: %w", err)
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
	// On Windows ReplaceFileW deletes the prepared copy and asks the OS to
	// recreate backupPath from the former live target. Validate that OS-produced
	// rollback image before relying on it at any subsequent crash boundary. On
	// Unix this revalidates the still-existing prepared copy.
	_, replacedBackupDigest, err := digestSnapshotPath(backupPath)
	if err != nil || !strings.EqualFold(oldDigest, replacedBackupDigest) {
		if err == nil {
			err = errors.New("OS-produced restore backup does not match the prepared live image")
		}
		return errors.Join(fmt.Errorf("復元配置後の退避DB検証エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}
	if err := syncDirectory(dir); err != nil {
		return errors.Join(fmt.Errorf("復元配置のfsyncエラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}
	installedDigest, err := digestDatabaseFile(currentPath)
	if err != nil || !strings.EqualFold(candidateDigest, installedDigest) {
		if err == nil {
			err = errors.New("復元配置DBが検証済み候補と一致しません")
		}
		return errors.Join(fmt.Errorf("復元配置identity/digest検証エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}
	manifest.Phase = "swapped"
	if err := writeRestoreManifest(currentPath, manifest); err != nil {
		return errors.Join(fmt.Errorf("restore intent journal更新エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}

	newDB, err := i.opener.Open(durableCtx, currentPath, securedb.Writable)
	if err == nil {
		err = i.validateRestoreDatabaseContext(durableCtx, newDB, currentPath)
	}
	if err != nil {
		if newDB != nil {
			_ = newDB.Close()
		}
		return errors.Join(fmt.Errorf("復元後DB検証エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}
	if err := checkpointAndCloseContext(durableCtx, newDB, currentPath); err != nil {
		_ = newDB.Close()
		return errors.Join(fmt.Errorf("復元後DB耐久化エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
	}
	// Reopen once more after checkpointing so i.db never references a handle
	// whose pager state predates the final durable candidate.
	newDB, err = i.opener.Open(durableCtx, currentPath, securedb.Writable)
	if err == nil {
		err = i.validateRestoreDatabaseContext(durableCtx, newDB, currentPath)
	}
	if err != nil {
		if newDB != nil {
			_ = newDB.Close()
		}
		return errors.Join(fmt.Errorf("復元後DB再検証エラー: %w", err), rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, i, oldDigest))
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
	return i.validateRestoreDatabaseContext(context.Background(), target, path)
}

func (i *Instance) validateRestoreDatabaseContext(ctx context.Context, target *sql.DB, path string) error {
	if target == nil {
		return fmt.Errorf("復元候補DBが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Restore validation is not fresh-database initialization. A blank
	// user_version=0 SQLite file must never be accepted and then populated by
	// createTablesOn, otherwise a same-key but unrelated file becomes a valid
	// ledger. Supported legacy snapshots must at least contain the historical
	// transaction shape before migration.
	var userTables int
	if err := validateInternalSQLiteTablesContext(ctx, target); err != nil {
		return fmt.Errorf("SQLite internal table validation failed: %w", err)
	}
	if err := target.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND "+sqliteUserTablePredicate).Scan(&userTables); err != nil {
		return fmt.Errorf("復元DB table count検査エラー: %w", err)
	}
	if userTables == 0 {
		return errors.New("空のSQLiteファイルは復元候補ではありません")
	}
	if err := validateLedgerSchemaContext(ctx, target, false); err != nil {
		return fmt.Errorf("復元DB schema最低要件エラー: %w", err)
	}
	if err := requireFullSynchronousContext(ctx, target); err != nil {
		return fmt.Errorf("復元DB耐久性設定エラー: %w", err)
	}
	if err := i.checkIntegrityContext(ctx, target); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := createTablesOnContext(ctx, target); err != nil {
		return fmt.Errorf("復元DBスキーマ更新エラー: %w", err)
	}
	if err := i.checkIntegrityContext(ctx, target); err != nil {
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
	return checkpointAndCloseContext(context.Background(), target, path)
}

func checkpointAndCloseContext(ctx context.Context, target *sql.DB, path string) error {
	if target == nil {
		return fmt.Errorf("DBが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := target.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
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
	return requireFullSynchronousContext(context.Background(), target)
}

func requireFullSynchronousContext(ctx context.Context, target *sql.DB) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var synchronous int
	if err := target.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
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
		strings.HasPrefix(name, ".") ||
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
	if _, err := os.Lstat(snapshotDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	transactionLock, err := acquireSnapshotTransactionLock(context.Background(), snapshotDir)
	if err != nil {
		return err
	}
	defer transactionLock.release()
	if err := transactionLock.verify(); err != nil {
		return err
	}
	if err := cleanupSnapshotPruneManifestTemps(context.Background(), snapshotDir, transactionLock); err != nil {
		return err
	}
	if err := pruneSnapshotsContext(context.Background(), snapshotDir, maxKeep, budget, "", transactionLock); err != nil {
		return err
	}
	return transactionLock.verify()
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
	return sqliteDatabaseSizeContext(context.Background(), target)
}

func sqliteDatabaseSizeContext(ctx context.Context, target *sql.DB) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var pageCount, pageSize int64
	if err := target.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := target.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
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

func readDirectoryEntriesContext(ctx context.Context, path string, maxEntries int) ([]os.DirEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxEntries <= 0 {
		return nil, errors.New("directory entry limit is invalid")
	}
	directory, err := os.Open(path) // #nosec G304 -- path is a caller-validated private directory.
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries := make([]os.DirEntry, 0, minInt(maxEntries, snapshotDirectoryReadBatchSize))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := maxEntries - len(entries)
		batchSize := snapshotDirectoryReadBatchSize
		if remaining < batchSize {
			batchSize = remaining
		}
		batch, readErr := directory.ReadDir(batchSize)
		entries = append(entries, batch...)
		if len(entries) > maxEntries {
			return nil, errors.New("directory entry limit exceeded")
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return entries, nil
			}
			return nil, readErr
		}
		if len(entries) == maxEntries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			extra, extraErr := directory.ReadDir(1)
			if len(extra) != 0 {
				return nil, errors.New("directory entry limit exceeded")
			}
			if extraErr != nil && !errors.Is(extraErr, io.EOF) {
				return nil, extraErr
			}
			return entries, nil
		}
	}
}

// cleanupSnapshotStagingDir removes only hidden, generated staging families.
// Public snapshot names are never part of this cleanup set, and unsafe
// symlink/hardlink artifacts fail closed instead of being followed or
// unlinked. This is called before listing and during startup so a crash cannot
// turn a partial final-name/scratch file into a restore or retention target.
func cleanupSnapshotStagingDir(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateSnapshotDirectory(path); err != nil {
		return err
	}
	transactionLock, err := acquireSnapshotTransactionLock(ctx, path)
	if err != nil {
		return err
	}
	defer transactionLock.release()
	if err := transactionLock.verify(); err != nil {
		return err
	}
	return cleanupSnapshotStagingDirLocked(ctx, path, transactionLock)
}

func cleanupSnapshotStagingDirLocked(ctx context.Context, path string, transactionLock *snapshotTransactionLock) error {
	if transactionLock == nil {
		return errors.New("snapshot staging cleanup transaction lock is nil")
	}
	if err := cleanupSnapshotPruneManifestTemps(ctx, path, transactionLock); err != nil {
		return err
	}
	if err := cleanupSnapshotPruneArtifacts(ctx, path, transactionLock); err != nil {
		return err
	}
	entries, err := transactionLock.readDir(ctx, maxSnapshotDirectoryEntries)
	if err != nil {
		return err
	}
	var cleanupErrs []error
	removed := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		isGenerated := strings.HasPrefix(name, ".omni-money-snapshot-staging-") || strings.HasPrefix(name, ".omni-money-list-validation-")
		isDatabase := isGenerated && strings.HasSuffix(name, ".db")
		isSidecar := isGenerated && (strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-journal"))
		if !isDatabase && !isSidecar {
			continue
		}
		artifactPath := filepath.Join(path, name)
		var removeErr error
		if isDatabase {
			removeErr = removeSnapshotTransactionSQLiteFiles(transactionLock, artifactPath)
		} else {
			removeErr = transactionLock.removeArtifact(artifactPath)
		}
		if removeErr != nil {
			cleanupErrs = append(cleanupErrs, removeErr)
		} else {
			removed = true
		}
	}
	if removed {
		if err := transactionLock.sync(); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	return errors.Join(errors.Join(cleanupErrs...), transactionLock.verify())
}

func parseSnapshotPruneArtifact(name string) (string, bool) {
	if !strings.HasPrefix(name, snapshotPruneArtifactPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(name, snapshotPruneArtifactPrefix)
	if len(rest) < 33 || rest[32] != '-' {
		return "", false
	}
	if _, err := hex.DecodeString(rest[:32]); err != nil {
		return "", false
	}
	original := rest[33:]
	if err := validateSnapshotName(original); err != nil {
		return "", false
	}
	return original, true
}

func snapshotPruneManifestPath(snapshotDir string) string {
	return filepath.Join(snapshotDir, snapshotPruneManifestName)
}

func newSnapshotPruneManifest(snapshotDir, snapshotName, newDigest string, entries []snapshotQuarantineEntry) (snapshotPruneManifest, error) {
	manifest := snapshotPruneManifest{
		Version:   snapshotPruneManifestVersion,
		Phase:     "prepared",
		Snapshot:  snapshotName,
		NewDigest: newDigest,
	}
	if err := validateSnapshotName(snapshotName); err != nil {
		return snapshotPruneManifest{}, err
	}
	if len(newDigest) != sha256.Size*2 {
		return snapshotPruneManifest{}, errors.New("snapshot prune digest is invalid")
	}
	if _, err := hex.DecodeString(newDigest); err != nil {
		return snapshotPruneManifest{}, errors.New("snapshot prune digest is not hexadecimal")
	}
	if len(entries) > maxSnapshotDirectoryEntries {
		return snapshotPruneManifest{}, errors.New("snapshot prune victim count exceeds limit")
	}
	for _, item := range entries {
		original := filepath.Base(item.original)
		quarantined := filepath.Base(item.quarantined)
		parsed, ok := parseSnapshotPruneArtifact(quarantined)
		if !ok || parsed != original {
			return snapshotPruneManifest{}, errors.New("snapshot prune victim path is invalid")
		}
		if !validSHA256Digest(item.digest) {
			return snapshotPruneManifest{}, errors.New("snapshot prune victim digest is invalid")
		}
		victim := snapshotPruneManifestVictim{Original: original, Quarantined: quarantined, Digest: item.digest}
		for _, sidecar := range item.sidecars {
			if !sidecar.present {
				continue
			}
			suffix := strings.TrimPrefix(sidecar.original, item.original)
			if suffix != "-wal" && suffix != "-shm" && suffix != "-journal" ||
				filepath.Base(sidecar.original) != original+suffix || filepath.Base(sidecar.quarantined) != quarantined+suffix {
				return snapshotPruneManifest{}, errors.New("snapshot prune sidecar path is invalid")
			}
			if !validSHA256Digest(sidecar.digest) {
				return snapshotPruneManifest{}, errors.New("snapshot prune sidecar digest is invalid")
			}
			victim.Sidecars = append(victim.Sidecars, snapshotPruneManifestSidecar{
				Original: original + suffix, Quarantined: quarantined + suffix, Digest: sidecar.digest,
			})
		}
		manifest.Victims = append(manifest.Victims, victim)
	}
	_ = snapshotDir // retained in the signature to keep construction tied to the validated directory.
	return manifest, validateSnapshotPruneManifest(snapshotDir, manifest)
}

func validateSnapshotPruneManifest(snapshotDir string, manifest snapshotPruneManifest) error {
	if manifest.Version != snapshotPruneManifestVersion || (manifest.Phase != "prepared" && manifest.Phase != "published") {
		return errors.New("unsupported snapshot prune manifest")
	}
	if err := validateSnapshotName(manifest.Snapshot); err != nil {
		return errors.New("snapshot prune manifest target is invalid")
	}
	if !validSHA256Digest(manifest.NewDigest) {
		return errors.New("snapshot prune manifest digest is not hexadecimal")
	}
	if len(manifest.Victims) == 0 || len(manifest.Victims) > maxSnapshotDirectoryEntries {
		return errors.New("snapshot prune manifest victim count is invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Victims))
	for _, victim := range manifest.Victims {
		if filepath.Base(victim.Original) != victim.Original || validateSnapshotName(victim.Original) != nil ||
			filepath.Base(victim.Quarantined) != victim.Quarantined {
			return errors.New("snapshot prune manifest victim name is invalid")
		}
		original, ok := parseSnapshotPruneArtifact(victim.Quarantined)
		if !ok || original != victim.Original {
			return errors.New("snapshot prune manifest quarantine name is invalid")
		}
		if _, ok := seen[victim.Original]; ok {
			return errors.New("snapshot prune manifest contains duplicate victims")
		}
		if !validSHA256Digest(victim.Digest) {
			return errors.New("snapshot prune manifest victim digest is invalid")
		}
		seen[victim.Original] = struct{}{}
		for _, sidecar := range victim.Sidecars {
			if filepath.Base(sidecar.Original) != sidecar.Original || filepath.Base(sidecar.Quarantined) != sidecar.Quarantined {
				return errors.New("snapshot prune manifest sidecar name is invalid")
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if sidecar.Original == victim.Original+suffix && sidecar.Quarantined == victim.Quarantined+suffix {
					if !validSHA256Digest(sidecar.Digest) {
						return errors.New("snapshot prune manifest sidecar digest is invalid")
					}
					goto validSidecar
				}
			}
			return errors.New("snapshot prune manifest sidecar suffix is invalid")
		validSidecar:
		}
	}
	if filepath.Clean(snapshotDir) == "." {
		return errors.New("snapshot prune manifest directory is invalid")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if int64(len(encoded))+1 > maxSnapshotPruneManifestBytes {
		return fmt.Errorf("snapshot prune manifest exceeds %d bytes", maxSnapshotPruneManifestBytes)
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// createSnapshotPruneManifest establishes ownership of a new retention
// transaction. A complete, synced private temporary is atomically published
// with no-replace semantics, so process death can leave either a removable
// orphan temp or a complete fixed journal, never a partial fixed journal.
func createSnapshotPruneManifest(snapshotDir string, manifest snapshotPruneManifest, locks ...*snapshotTransactionLock) error {
	return createSnapshotPruneManifestInternal(snapshotDir, manifest, nil, firstSnapshotTransactionLock(locks))
}

func createSnapshotPruneManifestWithCheckpoint(snapshotDir string, manifest snapshotPruneManifest, checkpoint func(string) error) error {
	return createSnapshotPruneManifestInternal(snapshotDir, manifest, checkpoint, nil)
}

func createSnapshotPruneManifestInternal(snapshotDir string, manifest snapshotPruneManifest, checkpoint func(string) error, lock *snapshotTransactionLock) error {
	if manifest.Phase != "prepared" {
		return errors.New("initial snapshot prune manifest is not prepared")
	}
	if err := validateSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	var file *os.File
	var tmpPath string
	if lock != nil {
		file, tmpPath, err = lock.createTemporary(snapshotPruneCreateTempPrefix, ".tmp")
	} else {
		file, err = os.CreateTemp(snapshotDir, snapshotPruneCreateTempPrefix+"*.tmp")
		if err == nil {
			tmpPath = file.Name()
		}
	}
	if err != nil {
		return err
	}
	closed := false
	published := false
	var expectedInfo os.FileInfo
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !published {
			if lock != nil {
				_ = lock.root.Remove(filepath.Base(tmpPath))
			} else {
				_ = os.Remove(tmpPath)
			}
		}
	}()
	fail := func(cause error) error {
		var statErr error
		if !closed {
			expectedInfo, statErr = file.Stat()
			closeErr := file.Close()
			closed = true
			statErr = errors.Join(statErr, closeErr)
		}
		var cleanupErr error
		if lock != nil {
			cleanupErr = lock.removeArtifact(tmpPath)
			cleanupErr = errors.Join(cleanupErr, lock.sync())
		} else {
			cleanupErr = removeCreatedSnapshotPruneManifest(nil, tmpPath, snapshotDir, expectedInfo)
		}
		if statErr != nil {
			cleanupErr = errors.Join(cleanupErr, statErr)
		}
		return errors.Join(cause, cleanupErr)
	}
	if err := file.Chmod(0600); err != nil {
		return fail(err)
	}
	if err := fileprivacy.Harden(file); err != nil {
		return fail(err)
	}
	if checkpoint != nil {
		if err := checkpoint("harden"); err != nil {
			return fail(err)
		}
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fail(err)
	}
	expectedInfo, err = file.Stat()
	if err != nil {
		return fail(err)
	}
	if checkpoint != nil {
		if err := checkpoint("write"); err != nil {
			return fail(err)
		}
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if checkpoint != nil {
		if err := checkpoint("sync"); err != nil {
			return fail(err)
		}
	}
	if checkpoint != nil {
		if err := checkpoint("close"); err != nil {
			return fail(err)
		}
	}
	if err := file.Close(); err != nil {
		closed = true
		return fail(err)
	}
	closed = true
	if checkpoint != nil {
		if err := checkpoint("publish"); err != nil {
			return fail(err)
		}
	}
	var publishErr error
	if lock != nil {
		publishErr = lock.publishManifestNoReplace(tmpPath, snapshotPruneManifestPath(snapshotDir))
	} else {
		publishErr = publishSnapshotPruneManifestNoReplace(tmpPath, snapshotPruneManifestPath(snapshotDir))
	}
	if publishErr != nil {
		return fail(publishErr)
	}
	published = true
	if checkpoint != nil {
		if err := checkpoint("published"); err != nil {
			// Publication is already complete. Leave the fixed, fully synced
			// journal for ordinary startup recovery rather than deleting it.
			return err
		}
	}
	return syncSnapshotTransactionDirectory(lock, snapshotDir)
}

func removeCreatedSnapshotPruneManifest(file *os.File, path, snapshotDir string, expected os.FileInfo) error {
	if file != nil {
		if held, err := file.Stat(); err == nil {
			expected = held
		}
	}
	if expected == nil || !expected.Mode().IsRegular() {
		return errors.New("cannot identify failed snapshot prune manifest")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return syncDirectory(snapshotDir)
		}
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !sameSnapshotInfo(expected, pathInfo) {
		return errors.New("failed snapshot prune manifest pathname changed before cleanup")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("failed snapshot prune manifest remained after cleanup")
	} else if !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(snapshotDir)
}

func writeSnapshotPruneManifest(snapshotDir string, manifest snapshotPruneManifest, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	var err error
	if err := validateSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		return err
	}
	var tmp *os.File
	var tmpPath string
	if lock != nil {
		tmp, tmpPath, err = lock.createTemporary(snapshotPruneUpdateTempPrefix, ".tmp")
	} else {
		tmp, err = os.CreateTemp(snapshotDir, snapshotPruneUpdateTempPrefix+"*.tmp")
		if err == nil {
			tmpPath = tmp.Name()
		}
	}
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = tmp.Close()
		if !complete {
			if lock != nil {
				_ = lock.root.Remove(filepath.Base(tmpPath))
			} else {
				_ = os.Remove(tmpPath)
			}
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
	if lock != nil {
		err = lock.replaceManifest(tmpPath, snapshotPruneManifestPath(snapshotDir))
	} else {
		err = replaceManifestFile(tmpPath, snapshotPruneManifestPath(snapshotDir))
	}
	if err != nil {
		return err
	}
	complete = true
	return syncSnapshotTransactionDirectory(lock, snapshotDir)
}

func readSnapshotPruneManifest(snapshotDir string, locks ...*snapshotTransactionLock) (snapshotPruneManifest, bool, error) {
	lock := firstSnapshotTransactionLock(locks)
	var file *os.File
	var err error
	if lock != nil {
		file, err = lock.openArtifact(snapshotPruneManifestPath(snapshotDir))
	} else {
		file, err = openSnapshotFile(snapshotPruneManifestPath(snapshotDir))
	}
	if os.IsNotExist(err) {
		return snapshotPruneManifest{}, false, nil
	}
	if err != nil {
		return snapshotPruneManifest{}, true, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return snapshotPruneManifest{}, true, err
	}
	if !validSnapshotFile(info) || info.Size() > maxSnapshotPruneManifestBytes {
		return snapshotPruneManifest{}, true, errors.New("snapshot prune manifest is not a private regular file")
	}
	var manifest snapshotPruneManifest
	decoder := json.NewDecoder(io.LimitReader(file, maxSnapshotPruneManifestBytes))
	if err := decoder.Decode(&manifest); err != nil {
		return snapshotPruneManifest{}, true, fmt.Errorf("decode snapshot prune manifest: %w", err)
	}
	if err := validateSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		return snapshotPruneManifest{}, true, err
	}
	return manifest, true, nil
}

func removeSnapshotPruneManifest(snapshotDir string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	if lock != nil {
		if err := lock.removeArtifact(snapshotPruneManifestPath(snapshotDir)); err != nil {
			return err
		}
		return lock.sync()
	}
	if err := os.Remove(snapshotPruneManifestPath(snapshotDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(snapshotDir)
}

func cleanupSnapshotPruneManifestTemps(ctx context.Context, snapshotDir string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	if ctx == nil {
		ctx = context.Background()
	}
	var entries []os.DirEntry
	var err error
	if lock != nil {
		entries, err = lock.readDir(ctx, maxSnapshotPruneTemporaryEntries)
	} else {
		entries, err = readDirectoryEntriesContext(ctx, snapshotDir, maxSnapshotPruneTemporaryEntries)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	removed := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !isSnapshotPruneManifestTempName(name) {
			continue
		}
		path := filepath.Join(snapshotDir, name)
		var info os.FileInfo
		if lock != nil {
			file, openErr := lock.openArtifact(path)
			if openErr == nil {
				info, openErr = file.Stat()
				_ = file.Close()
			}
			err = openErr
		} else {
			info, err = validatePrivateArtifact(path)
		}
		if err != nil || !validSnapshotMode(info, true) {
			if err == nil {
				err = errors.New("temporary snapshot prune manifest is not owner-private")
			}
			return fmt.Errorf("unsafe temporary snapshot prune manifest %q: %w", name, err)
		}
		if lock != nil {
			err = lock.removeArtifact(path)
		} else {
			err = removePrivateSQLiteFile(path)
		}
		if err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncSnapshotTransactionDirectory(lock, snapshotDir)
	}
	return nil
}

func isSnapshotPruneManifestTempName(name string) bool {
	if !strings.HasSuffix(name, ".tmp") {
		return false
	}
	return strings.HasPrefix(name, snapshotPruneCreateTempPrefix) || strings.HasPrefix(name, snapshotPruneUpdateTempPrefix)
}

func snapshotQuarantineEntriesFromManifest(snapshotDir string, manifest snapshotPruneManifest) ([]snapshotQuarantineEntry, error) {
	if err := validateSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		return nil, err
	}
	entries := make([]snapshotQuarantineEntry, 0, len(manifest.Victims))
	for _, victim := range manifest.Victims {
		item := snapshotQuarantineEntry{
			original: filepath.Join(snapshotDir, victim.Original), quarantined: filepath.Join(snapshotDir, victim.Quarantined), digest: victim.Digest,
		}
		for _, sidecar := range victim.Sidecars {
			for index, suffix := range []string{"-wal", "-shm", "-journal"} {
				if sidecar.Original == victim.Original+suffix {
					item.sidecars[index] = snapshotQuarantineSidecar{
						original: filepath.Join(snapshotDir, sidecar.Original), quarantined: filepath.Join(snapshotDir, sidecar.Quarantined), digest: sidecar.Digest, present: true,
					}
				}
			}
		}
		entries = append(entries, item)
	}
	return entries, nil
}

func recoverSnapshotPruneTransactionAtStart(ctx context.Context, snapshotDir string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	_, found, err := readSnapshotPruneManifest(snapshotDir, lock)
	if err != nil {
		return err
	}
	if found {
		if err := recoverSnapshotPruneManifest(ctx, snapshotDir, lock); err != nil {
			return err
		}
	}
	// Recovery returning nil is not enough: a partially finalized operation
	// must retain its journal, and a concurrent owner may have established one
	// while recovery was running. Never proceed while the fixed name is present.
	_, found, err = readSnapshotPruneManifest(snapshotDir, lock)
	if err != nil {
		return err
	}
	if found {
		return errors.New("snapshot prune manifest remains unresolved")
	}
	return nil
}

// recoverSnapshotPruneManifest completes or rolls back the quarantine
// transaction idempotently. A prepared transaction with no published target
// restores victims; a target matching the staged digest is treated as an
// already-published transaction and only then finalizes victim deletion.
func recoverSnapshotPruneManifest(ctx context.Context, snapshotDir string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	manifest, found, err := readSnapshotPruneManifest(snapshotDir, lock)
	if err != nil || !found {
		return err
	}
	entries, err := snapshotQuarantineEntriesFromManifest(snapshotDir, manifest)
	if err != nil {
		return err
	}
	target := filepath.Join(snapshotDir, manifest.Snapshot)
	if manifest.Phase == "prepared" {
		targetFile, targetExists, targetErr := openSnapshotPruneArtifact(target, manifest.NewDigest, lock)
		if targetFile != nil {
			_ = targetFile.Close()
		}
		if !targetExists && targetErr == nil {
			if rollbackErr := rollbackSnapshotQuarantine(entries, snapshotDir, lock); rollbackErr != nil {
				// Preserve the prepared journal when any victim could not be
				// restored. Deleting the only durable recovery record would turn
				// a recoverable partial rollback into silent data loss.
				return rollbackErr
			}
			return removeSnapshotPruneManifest(snapshotDir, lock)
		}
		if targetErr != nil {
			return targetErr
		}
		manifest.Phase = "published"
		if err := writeSnapshotPruneManifest(snapshotDir, manifest, lock); err != nil {
			return err
		}
	}
	// Published recovery is allowed to destroy victims only while the durable
	// public generation still names the exact bytes recorded by the journal.
	// Otherwise target removal or substitution would turn recovery into data
	// loss.
	targetFile, targetExists, targetErr := openSnapshotPruneArtifact(target, manifest.NewDigest, lock)
	if targetFile != nil {
		_ = targetFile.Close()
	}
	if targetErr != nil {
		return targetErr
	}
	if !targetExists {
		return errors.New("published snapshot target is missing")
	}
	if err := finalizeSnapshotQuarantine(ctx, entries, snapshotDir, lock); err != nil {
		return err
	}
	return removeSnapshotPruneManifest(snapshotDir, lock)
}

// cleanupSnapshotPruneArtifacts recovers a pre-publication quarantine after
// a process crash. A hidden victim is restored to its original public name;
// if only its sidecar survived finalization, the orphan sidecar is removed.
// Existing public names are never overwritten, so ambiguity fails closed.
func cleanupSnapshotPruneArtifacts(ctx context.Context, path string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	if ctx == nil {
		ctx = context.Background()
	}
	if _, found, err := readSnapshotPruneManifest(path, lock); err != nil {
		return err
	} else if found {
		return recoverSnapshotPruneManifest(ctx, path, lock)
	}
	var entries []os.DirEntry
	var err error
	if lock != nil {
		entries, err = lock.readDir(ctx, maxSnapshotDirectoryEntries)
	} else {
		entries, err = readDirectoryEntriesContext(ctx, path, maxSnapshotDirectoryEntries)
	}
	if err != nil {
		return err
	}
	changed := false
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), snapshotPruneArtifactPrefix) || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		original, ok := parseSnapshotPruneArtifact(entry.Name())
		if !ok {
			errs = append(errs, fmt.Errorf("invalid snapshot quarantine artifact: %s", entry.Name()))
			continue
		}
		hiddenPath := filepath.Join(path, entry.Name())
		if _, err := validateSnapshotTransactionArtifact(lock, hiddenPath); err != nil {
			errs = append(errs, err)
			continue
		}
		originalPath := filepath.Join(path, original)
		if _, err := snapshotTransactionLstat(lock, originalPath); err == nil {
			errs = append(errs, fmt.Errorf("snapshot quarantine target already exists: %s", original))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		if err := snapshotTransactionRename(lock, hiddenPath, originalPath); err != nil {
			errs = append(errs, err)
			continue
		}
		changed = true
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, snapshotPruneArtifactPrefix) || !strings.HasSuffix(name, ".db-wal") && !strings.HasSuffix(name, ".db-shm") && !strings.HasSuffix(name, ".db-journal") {
			continue
		}
		hiddenPath := filepath.Join(path, name)
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "-wal"), "-shm"), "-journal")
		original, ok := parseSnapshotPruneArtifact(base)
		if !ok {
			errs = append(errs, fmt.Errorf("invalid snapshot quarantine sidecar: %s", name))
			continue
		}
		if _, err := snapshotTransactionLstat(lock, hiddenPath); err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		originalPath := filepath.Join(path, original) + strings.TrimPrefix(name, base)
		if _, err := snapshotTransactionLstat(lock, originalPath); err == nil {
			errs = append(errs, fmt.Errorf("snapshot quarantine sidecar target already exists: %s", filepath.Base(originalPath)))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		hiddenDBPath := filepath.Join(path, base)
		_, hiddenDBErr := snapshotTransactionLstat(lock, hiddenDBPath)
		_, originalDBErr := snapshotTransactionLstat(lock, filepath.Join(path, original))
		if hiddenDBErr == nil {
			// The database itself was not recovered (for example because the
			// original public name already existed). Never discard its sidecar.
			errs = append(errs, fmt.Errorf("snapshot quarantine database remains: %s", filepath.Base(hiddenDBPath)))
			continue
		}
		if originalDBErr != nil && !os.IsNotExist(originalDBErr) {
			errs = append(errs, originalDBErr)
			continue
		}
		if os.IsNotExist(originalDBErr) {
			var removeErr error
			if lock != nil {
				removeErr = lock.removeArtifact(hiddenPath)
			} else {
				removeErr = removePrivateSQLiteFile(hiddenPath)
			}
			if removeErr != nil {
				errs = append(errs, removeErr)
			} else {
				changed = true
			}
			continue
		}
		if _, err := validateSnapshotTransactionArtifact(lock, hiddenPath); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := snapshotTransactionRename(lock, hiddenPath, originalPath); err != nil {
			errs = append(errs, err)
		} else {
			changed = true
		}
	}
	if changed {
		if err := syncSnapshotTransactionDirectory(lock, path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type snapshotQuarantineEntry struct {
	original    string
	quarantined string
	digest      string
	sidecars    [3]snapshotQuarantineSidecar
}

type snapshotQuarantineSidecar struct {
	original    string
	quarantined string
	digest      string
	present     bool
}

// planSnapshotQuarantineContext selects and fingerprints every retention
// victim without changing the public namespace. The caller can therefore
// prove that the complete durable manifest fits its reader limit before the
// first rename occurs.
func planSnapshotQuarantineContext(ctx context.Context, snapshotDir string, maxKeep int, maxBytes, newBytes int64, locks ...*snapshotTransactionLock) ([]snapshotQuarantineEntry, error) {
	lock := firstSnapshotTransactionLock(locks)
	if ctx == nil {
		ctx = context.Background()
	}
	if maxKeep <= 0 || maxBytes < 0 || newBytes < 0 || newBytes > maxBytes {
		return nil, errors.New("snapshot quarantine retention boundary is invalid")
	}
	var entries []os.DirEntry
	var err error
	if lock != nil {
		entries, err = lock.readDir(ctx, maxSnapshotDirectoryEntries)
	} else {
		entries, err = readDirectoryEntriesContext(ctx, snapshotDir, maxSnapshotDirectoryEntries)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	usage := make([]snapshotUsageEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		info, err := validateSnapshotTransactionArtifact(lock, path)
		if err != nil {
			return nil, err
		}
		size := info.Size()
		if size < 0 || total > (1<<63-1)-size {
			return nil, errors.New("snapshot retention size overflow")
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			sidecarInfo, sidecarErr := validateSnapshotTransactionArtifact(lock, path+suffix)
			if os.IsNotExist(sidecarErr) {
				continue
			}
			if sidecarErr != nil {
				return nil, sidecarErr
			}
			if sidecarInfo.Size() < 0 || size > (1<<63-1)-sidecarInfo.Size() {
				return nil, fmt.Errorf("snapshot sidecar is unsafe: %s", filepath.Base(path+suffix))
			}
			size += sidecarInfo.Size()
		}
		if total > (1<<63-1)-size {
			return nil, errors.New("snapshot retention size overflow")
		}
		total += size
		usage = append(usage, snapshotUsageEntry{name: entry.Name(), path: path, size: size})
	}
	sort.Slice(usage, func(left, right int) bool { return usage[left].name < usage[right].name })
	var planned []snapshotQuarantineEntry
	for len(usage)+1 > maxKeep || snapshotRetentionExceedsBudget(total, newBytes, maxBytes) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(usage) == 0 {
			return nil, errors.New("snapshot retention cannot fit the new generation")
		}
		victim := usage[0]
		random := make([]byte, 16)
		if _, err := cryptorand.Read(random); err != nil {
			return nil, err
		}
		quarantinePath := filepath.Join(snapshotDir, snapshotPruneArtifactPrefix+hex.EncodeToString(random)+"-"+victim.name)
		clear(random)
		if _, err := snapshotTransactionLstat(lock, quarantinePath); err == nil {
			return nil, errors.New("snapshot quarantine name collision")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		var digestFile *os.File
		if lock != nil {
			digestFile, err = lock.openArtifact(victim.path)
		} else {
			digestFile, err = openSnapshotFile(victim.path)
		}
		if err != nil {
			return nil, err
		}
		victimDigest, err := digestOpenFile(digestFile)
		_ = digestFile.Close()
		if err != nil {
			return nil, err
		}
		item := snapshotQuarantineEntry{original: victim.path, quarantined: quarantinePath, digest: victimDigest}
		for index, suffix := range []string{"-wal", "-shm", "-journal"} {
			original := victim.path + suffix
			quarantinedPath := quarantinePath + suffix
			_, statErr := validateSnapshotTransactionArtifact(lock, original)
			if os.IsNotExist(statErr) {
				item.sidecars[index] = snapshotQuarantineSidecar{original: original, quarantined: quarantinedPath}
				continue
			}
			if statErr != nil {
				return nil, errors.Join(statErr, errors.New("snapshot sidecar is unsafe"))
			}
			var sidecarFile *os.File
			var sidecarDigest string
			var digestErr error
			if lock != nil {
				sidecarFile, digestErr = lock.openArtifact(original)
			} else {
				sidecarFile, digestErr = openSnapshotFile(original)
			}
			if digestErr == nil {
				sidecarDigest, digestErr = digestOpenFile(sidecarFile)
				_ = sidecarFile.Close()
			}
			if digestErr != nil {
				return nil, digestErr
			}
			item.sidecars[index] = snapshotQuarantineSidecar{
				original: original, quarantined: quarantinedPath, digest: sidecarDigest, present: true,
			}
		}
		planned = append(planned, item)
		total -= victim.size
		usage = usage[1:]
	}
	return planned, nil
}

func snapshotRetentionExceedsBudget(total, additional, limit int64) bool {
	return total < 0 || additional < 0 || limit < 0 || additional > limit || total > limit-additional
}

// applySnapshotQuarantine performs a fully planned transaction. Every source
// is re-proved against its recorded digest immediately before rename, and any
// failure rolls back the prefix already moved.
func applySnapshotQuarantine(entries []snapshotQuarantineEntry, snapshotDir string, locks ...*snapshotTransactionLock) ([]snapshotQuarantineEntry, error) {
	lock := firstSnapshotTransactionLock(locks)
	var moved []snapshotQuarantineEntry
	rollback := func(cause error) ([]snapshotQuarantineEntry, error) {
		return nil, errors.Join(cause, rollbackSnapshotQuarantine(moved, snapshotDir, lock))
	}
	for _, item := range entries {
		moved = append(moved, item)
		if _, err := snapshotTransactionLstat(lock, item.quarantined); err == nil {
			return rollback(errors.New("snapshot quarantine name collision"))
		} else if !os.IsNotExist(err) {
			return rollback(err)
		}
		originalFile, exists, err := openSnapshotPruneArtifact(item.original, item.digest, lock)
		if err != nil {
			return rollback(err)
		}
		if !exists {
			return rollback(fmt.Errorf("snapshot quarantine source disappeared: %s", filepath.Base(item.original)))
		}
		if err := assertSnapshotTransactionArtifact(lock, originalFile, item.original); err != nil {
			_ = originalFile.Close()
			return rollback(err)
		}
		if err := snapshotTransactionRename(lock, item.original, item.quarantined); err != nil {
			_ = originalFile.Close()
			return rollback(err)
		}
		_ = originalFile.Close()
		for _, sidecar := range item.sidecars {
			if _, err := snapshotTransactionLstat(lock, sidecar.quarantined); err == nil {
				return rollback(fmt.Errorf("snapshot sidecar quarantine name collision: %s", filepath.Base(sidecar.quarantined)))
			} else if !os.IsNotExist(err) {
				return rollback(err)
			}
			if !sidecar.present {
				if _, err := snapshotTransactionLstat(lock, sidecar.original); err == nil {
					return rollback(fmt.Errorf("snapshot sidecar appeared after planning: %s", filepath.Base(sidecar.original)))
				} else if !os.IsNotExist(err) {
					return rollback(err)
				}
				continue
			}
			sidecarFile, exists, err := openSnapshotPruneArtifact(sidecar.original, sidecar.digest, lock)
			if err != nil {
				return rollback(err)
			}
			if !exists {
				return rollback(fmt.Errorf("snapshot sidecar disappeared after planning: %s", filepath.Base(sidecar.original)))
			}
			if err := assertSnapshotTransactionArtifact(lock, sidecarFile, sidecar.original); err != nil {
				_ = sidecarFile.Close()
				return rollback(err)
			}
			if err := snapshotTransactionRename(lock, sidecar.original, sidecar.quarantined); err != nil {
				_ = sidecarFile.Close()
				return rollback(err)
			}
			_ = sidecarFile.Close()
		}
	}
	if len(moved) > 0 {
		if err := syncSnapshotTransactionDirectory(lock, snapshotDir); err != nil {
			return rollback(err)
		}
	}
	return moved, nil
}

// quarantineSnapshotsContext preserves the package-internal compatibility
// path used by focused retention tests. Production creation plans and sizes
// its manifest explicitly before calling applySnapshotQuarantine.
func quarantineSnapshotsContext(ctx context.Context, snapshotDir string, maxKeep int, maxBytes, newBytes int64) ([]snapshotQuarantineEntry, error) {
	planned, err := planSnapshotQuarantineContext(ctx, snapshotDir, maxKeep, maxBytes, newBytes)
	if err != nil {
		return nil, err
	}
	return applySnapshotQuarantine(planned, snapshotDir)
}

func rollbackSnapshotQuarantine(entries []snapshotQuarantineEntry, snapshotDir string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	var errs []error
	for index := len(entries) - 1; index >= 0; index-- {
		item := entries[index]
		if err := rollbackSnapshotPruneArtifact(item.original, item.quarantined, item.digest, lock); err != nil {
			errs = append(errs, err)
		}
		for _, sidecar := range item.sidecars {
			if !sidecar.present {
				continue
			}
			if err := rollbackSnapshotPruneArtifact(sidecar.original, sidecar.quarantined, sidecar.digest, lock); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(entries) > 0 {
		if err := syncSnapshotTransactionDirectory(lock, snapshotDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func openSnapshotPruneArtifact(path, expectedDigest string, locks ...*snapshotTransactionLock) (*os.File, bool, error) {
	if !validSHA256Digest(expectedDigest) {
		return nil, false, errors.New("snapshot prune artifact digest is invalid")
	}
	lock := firstSnapshotTransactionLock(locks)
	var file *os.File
	var err error
	if lock != nil {
		file, err = lock.openArtifact(path)
	} else {
		file, err = openSnapshotFile(path)
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	fail := func(err error) (*os.File, bool, error) {
		_ = file.Close()
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !validSnapshotFile(info) {
		return fail(errors.New("snapshot prune artifact is not a private regular file"))
	}
	digest, err := digestOpenFile(file)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(digest, expectedDigest) {
		return fail(fmt.Errorf("snapshot prune artifact digest mismatch: %s", filepath.Base(path)))
	}
	if err := assertSnapshotTransactionArtifact(lock, file, path); err != nil {
		return fail(err)
	}
	return file, true, nil
}

func rollbackSnapshotPruneArtifact(original, quarantined, expectedDigest string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	originalFile, originalExists, err := openSnapshotPruneArtifact(original, expectedDigest, lock)
	if err != nil {
		return err
	}
	if originalFile != nil {
		defer originalFile.Close()
	}
	quarantinedFile, quarantinedExists, err := openSnapshotPruneArtifact(quarantined, expectedDigest, lock)
	if err != nil {
		return err
	}
	if quarantinedFile != nil {
		defer quarantinedFile.Close()
	}
	switch {
	case originalExists && quarantinedExists:
		return fmt.Errorf("snapshot prune rollback has both original and quarantine: %s", filepath.Base(original))
	case originalExists:
		// A prior attempt completed this atomic rename and crashed before
		// advancing to the next artifact. Matching bytes make it safe to resume.
		return nil
	case quarantinedExists:
		if err := assertSnapshotTransactionArtifact(lock, quarantinedFile, quarantined); err != nil {
			return err
		}
		if _, err := snapshotTransactionLstat(lock, original); err == nil {
			return fmt.Errorf("snapshot prune rollback target appeared: %s", filepath.Base(original))
		} else if !os.IsNotExist(err) {
			return err
		}
		return snapshotTransactionRename(lock, quarantined, original)
	default:
		// In prepared phase neither path can mean successful deletion. Treat it
		// as loss/tampering and preserve the journal for operator recovery.
		return fmt.Errorf("snapshot prune rollback lost both original and quarantine: %s", filepath.Base(original))
	}
}

func finalizeSnapshotQuarantine(ctx context.Context, entries []snapshotQuarantineEntry, snapshotDir string, locks ...*snapshotTransactionLock) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lock := firstSnapshotTransactionLock(locks)
	var errs []error
	removed := false
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		itemRemoved, err := finalizeSnapshotPruneArtifact(item.original, item.quarantined, item.digest, lock)
		if err != nil {
			errs = append(errs, err)
		} else if itemRemoved {
			removed = true
		}
		for _, sidecar := range item.sidecars {
			if !sidecar.present {
				continue
			}
			sidecarRemoved, err := finalizeSnapshotPruneArtifact(sidecar.original, sidecar.quarantined, sidecar.digest, lock)
			if err != nil {
				errs = append(errs, err)
			} else if sidecarRemoved {
				removed = true
			}
		}
	}
	if removed {
		if err := syncSnapshotTransactionDirectory(lock, snapshotDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func finalizeSnapshotPruneArtifact(original, quarantined, expectedDigest string, locks ...*snapshotTransactionLock) (bool, error) {
	lock := firstSnapshotTransactionLock(locks)
	originalFile, originalExists, err := openSnapshotPruneArtifact(original, expectedDigest, lock)
	if err != nil {
		return false, err
	}
	if originalFile != nil {
		defer originalFile.Close()
	}
	quarantinedFile, quarantinedExists, err := openSnapshotPruneArtifact(quarantined, expectedDigest, lock)
	if err != nil {
		return false, err
	}
	if quarantinedFile != nil {
		defer quarantinedFile.Close()
	}
	switch {
	case originalExists && quarantinedExists:
		return false, fmt.Errorf("published snapshot prune has both original and quarantine: %s", filepath.Base(original))
	case originalExists:
		return false, fmt.Errorf("published snapshot prune victim reappeared: %s", filepath.Base(original))
	case quarantinedExists:
		if err := assertSnapshotTransactionArtifact(lock, quarantinedFile, quarantined); err != nil {
			return false, err
		}
		if _, err := snapshotTransactionLstat(lock, original); err == nil {
			return false, fmt.Errorf("published snapshot prune victim appeared: %s", filepath.Base(original))
		} else if !os.IsNotExist(err) {
			return false, err
		}
		if lock != nil {
			return true, lock.removeArtifact(quarantined)
		}
		return true, removePrivateSQLiteFile(quarantined)
	default:
		// Published finalization deletes the quarantine atomically. Neither path
		// therefore means a previous attempt already completed this artifact.
		return false, nil
	}
}

func acquireSnapshotValidation(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case snapshotValidationAdmission <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func snapshotValidationAdmissionRelease() {
	<-snapshotValidationAdmission
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func pruneSnapshots(snapshotDir string, maxKeep int, maxBytes int64, protectedPath string) error {
	return pruneSnapshotsContext(context.Background(), snapshotDir, maxKeep, maxBytes, protectedPath)
}

func pruneSnapshotsContext(ctx context.Context, snapshotDir string, maxKeep int, maxBytes int64, protectedPath string, locks ...*snapshotTransactionLock) error {
	lock := firstSnapshotTransactionLock(locks)
	if maxKeep <= 0 || maxBytes < 0 {
		return fmt.Errorf("スナップショット保持境界が無効です")
	}
	var entries []os.DirEntry
	var err error
	if lock != nil {
		entries, err = lock.readDir(ctx, maxSnapshotDirectoryEntries)
	} else {
		entries, err = readDirectoryEntriesContext(ctx, snapshotDir, maxSnapshotDirectoryEntries)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	usage := make([]snapshotUsageEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		info, err := validateSnapshotTransactionArtifact(lock, path)
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
			sidecarInfo, sidecarErr := validateSnapshotTransactionArtifact(lock, sidecar)
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
		var removeErr error
		if lock != nil {
			removeErr = lock.removeArtifact(victim.path)
		} else {
			removeErr = removePrivateSQLiteFile(victim.path)
		}
		if removeErr != nil {
			return fmt.Errorf("古いスナップショット削除エラー (%s): %w", victim.name, removeErr)
		}
		for _, sidecar := range []string{victim.path + "-wal", victim.path + "-shm", victim.path + "-journal"} {
			if lock != nil {
				removeErr = lock.removeArtifact(sidecar)
			} else {
				removeErr = removePrivateSQLiteFile(sidecar)
			}
			if removeErr != nil {
				return fmt.Errorf("古いスナップショットsidecar削除エラー (%s): %w", victim.name, removeErr)
			}
		}
		total -= victim.size
		usage = append(usage[:candidate], usage[candidate+1:]...)
		removed = true
		log.Printf("security_event=snapshot_prune result=success remaining_bytes=%d", total)
	}
	if removed {
		if err := syncSnapshotTransactionDirectory(lock, snapshotDir); err != nil {
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
		if !i.acquireAutoSnapshotValidation() {
			// Closing instances must not leave a worker permanently blocked on the
			// process-wide admission gate. snapshotRunning is cleared below so a
			// lifecycle drain can complete deterministically.
			log.Printf("security_event=snapshot_create result=error")
		} else {
			_, err := i.createSnapshot(context.Background(), "")
			if err != nil {
				log.Printf("security_event=snapshot_create result=error")
			} else if err := i.cleanOldSnapshots("", 30); err != nil {
				log.Printf("security_event=snapshot_prune result=error")
			}
			snapshotValidationAdmissionRelease()
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

// acquireAutoSnapshotValidation bounds the wait for an asynchronous worker.
// It uses short context-aware admission attempts so beginDBLifecycle can mark
// the instance closing and release the worker even when another tenant owns
// the global validation budget.
func (i *Instance) acquireAutoSnapshotValidation() bool {
	firstAttempt := true
	for {
		i.snapshotMu.Lock()
		closing := i.snapshotClosing
		i.snapshotMu.Unlock()
		if closing && !firstAttempt {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := acquireSnapshotValidation(ctx)
		cancel()
		if err == nil {
			i.snapshotMu.Lock()
			closing = i.snapshotClosing
			i.snapshotMu.Unlock()
			if closing && !firstAttempt {
				snapshotValidationAdmissionRelease()
				return false
			}
			return true
		}
		firstAttempt = false
		i.snapshotMu.Lock()
		closing = i.snapshotClosing
		i.snapshotMu.Unlock()
		if closing {
			return false
		}
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
func backupSQLiteDatabase(ctx context.Context, opener *securedb.Opener, source *sql.DB, snapshotPath string) error {
	if opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return opener.Backup(ctx, source, snapshotPath)
}

func backupSQLiteDatabaseToPlaceholder(ctx context.Context, opener *securedb.Opener, source *sql.DB, snapshotPath string, placeholder *os.File) error {
	if opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return opener.BackupToPlaceholder(ctx, source, snapshotPath, placeholder)
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

// validatePrivateArtifact validates both pathname metadata and the no-follow
// descriptor. The descriptor check is required on Windows, where Unix mode
// bits do not expose hard-link count or the protected DACL.
func validatePrivateArtifact(path string) (os.FileInfo, error) {
	file, err := openSnapshotFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !validSnapshotFile(info) || !snapshotSourceMatches(pathInfo, info) {
		return nil, errors.New("private artifact is not a stable regular file")
	}
	return info, nil
}

func validSnapshotMode(info os.FileInfo, encrypted bool) bool {
	return snapshotModeAllowed(info, encrypted)
}

// ValidateSnapshotTransactionLock proves that path is the exact empty,
// owner-private, single-link, no-follow file created by snapshot coordination.
// It is intentionally narrower than general snapshot validation so migration
// cannot use this exception to admit source, unknown, or content-bearing files.
func ValidateSnapshotTransactionLock(path string) error {
	if filepath.Base(path) != snapshotTransactionLockName {
		return errors.New("snapshot transaction lock name is invalid")
	}
	file, err := openSnapshotFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !validSnapshotFile(info) || !validSnapshotMode(info, true) || info.Size() != 0 {
		return errors.New("snapshot transaction lock is not an empty private regular file")
	}
	return assertOpenFileAtPath(file, path)
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
	out, err := copyDatabaseFileOpen(source, destination)
	if err != nil {
		return err
	}
	return out.Close()
}

// copyDatabaseFileOpen returns the private O_RDWR creation descriptor after
// the bytes have been flushed. Restore keeps this identity anchor open through
// digest, manifest and final pre-publication checks. This is also required on
// Windows, where Sync on a GENERIC_READ handle fails with ACCESS_DENIED.
func copyDatabaseFileOpen(source, destination string) (*os.File, error) {
	in, err := openSnapshotFile(source)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- destination is generated in the private DB directory.
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		if !completed {
			_ = out.Close()
			_ = os.Remove(destination)
		}
	}()
	if err := out.Chmod(0600); err != nil {
		return nil, err
	}
	if err := fileprivacy.Harden(out); err != nil {
		return nil, err
	}
	if err := copyFileToOpenBounded(in, out, maxSnapshotValidationBytes); err != nil {
		return nil, err
	}
	if err := out.Sync(); err != nil {
		return nil, err
	}
	completed = true
	return out, nil
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

// digestSnapshotPath returns the identity and digest observed from one
// no-follow descriptor. Callers that publish or roll back the image retain the
// identity and re-prove the pathname immediately before the atomic operation;
// this avoids treating a pathname-only second read as the validated bytes.
func digestSnapshotPath(path string) (os.FileInfo, string, error) {
	file, err := openSnapshotFile(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !validSnapshotFile(info) {
		return nil, "", errors.New("snapshot path is not a private regular file")
	}
	digest, err := digestOpenFile(file)
	if err != nil {
		return nil, "", err
	}
	return info, digest, nil
}

func assertPathDigest(path string, expectedInfo os.FileInfo, expectedDigest string) error {
	actualInfo, actualDigest, err := digestSnapshotPath(path)
	if err != nil {
		return err
	}
	if !sameSnapshotInfo(expectedInfo, actualInfo) {
		return errors.New("snapshot pathname no longer names the validated descriptor")
	}
	if !strings.EqualFold(expectedDigest, actualDigest) {
		return errors.New("snapshot pathname content no longer matches the validated descriptor")
	}
	return nil
}

func digestOpenFile(file *os.File) (string, error) {
	return digestOpenFileContext(context.Background(), file)
}

func digestOpenFileContext(ctx context.Context, file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("nil file for digest")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
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
	if _, err := io.CopyN(hash, contextReader{ctx: ctx, r: io.LimitReader(file, info.Size())}, info.Size()); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
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
	if err := cleanupSnapshotStagingDir(context.Background(), filepath.Join(dir, "snapshots")); err != nil {
		return fmt.Errorf("snapshot staging recovery failed: %w", err)
	}
	manifest, hasManifest, err := readRestoreManifest(databasePath)
	if err != nil {
		return err
	}
	entries, err := readDirectoryEntriesContext(context.Background(), dir, maxSnapshotDirectoryEntries)
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
	entries, err := readDirectoryEntriesContext(context.Background(), dir, maxSnapshotDirectoryEntries)
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
	file, err := openDurableDatabaseFile(path)
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

func syncOpenFileAndDirectory(file *os.File, dir string) error {
	if file == nil {
		return errors.New("cannot sync a nil database descriptor")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func rollbackRestoreFiles(currentPath, backupPath, candidatePath string, instance *Instance) error {
	return rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath, instance, "")
}

// rollbackRestoreFilesExpected restores only the backup image whose digest was
// captured before publication. A backup pathname is not trusted merely because
// it still exists: a same-account writer may have exchanged it for another
// valid database while the restore was validating the new image. If that
// happens, fail closed and keep the instance detached instead of reopening the
// wrong ledger.
func rollbackRestoreFilesExpected(currentPath, backupPath, candidatePath string, instance *Instance, expectedBackupDigest string) error {
	dir := filepath.Dir(currentPath)
	var errs []error
	// Never unlink currentPath: this rollback runs after the candidate has
	// already replaced it, and unlink-then-rename would recreate the same
	// crash window as the original restore implementation. Sidecars can be
	// removed first; the main file is replaced in one OS atomic operation.
	if err := removeSQLiteSidecars(currentPath); err != nil {
		errs = append(errs, fmt.Errorf("remove failed restore sidecars: %w", err))
	}
	if expectedBackupDigest != "" {
		_, actualDigest, err := digestSnapshotPath(backupPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("verify restore backup identity: %w", err))
		} else if !strings.EqualFold(expectedBackupDigest, actualDigest) {
			errs = append(errs, errors.New("restore backup content no longer matches the prepared image"))
		}
	}
	if len(errs) == 0 {
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
		} else if expectedBackupDigest != "" {
			// The open itself is pathname-based. Re-check after opening and detach
			// the instance if a concurrent exchange selected a different image.
			if liveDigest, digestErr := digestDatabaseFile(currentPath); digestErr != nil || !strings.EqualFold(expectedBackupDigest, liveDigest) {
				if instance.db != nil {
					_ = instance.db.Close()
				}
				instance.db = nil
				if digestErr == nil {
					digestErr = errors.New("reopened rollback image does not match the prepared backup")
				}
				errs = append(errs, fmt.Errorf("rollback reopened image identity mismatch: %w", digestErr))
			}
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
