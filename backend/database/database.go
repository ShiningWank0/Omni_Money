// Package database はSQLite接続、初期化、スナップショット機能を提供する
package database

import (
	"context"
	"database/sql"
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
		// タグテーブル（Agent.md §6.6: 3階層タグシステム）
		`CREATE TABLE IF NOT EXISTS tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER DEFAULT NULL,
			level INTEGER NOT NULL DEFAULT 1 CHECK(level IN (1, 2, 3)),
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
				OR (SELECT COUNT(*) FROM transaction_images WHERE transaction_id = NEW.transaction_id) >= %d
				OR COALESCE((SELECT SUM(length(data)) FROM transaction_images WHERE transaction_id = NEW.transaction_id), 0) + length(NEW.data) > %d
				OR COALESCE((
					SELECT SUM(length(ti.data))
					FROM transaction_images ti
					JOIN transactions t ON t.id = ti.transaction_id
					WHERE t.account = (SELECT account FROM transactions WHERE id = NEW.transaction_id)
				), 0) + length(NEW.data) > %d
				OR COALESCE((SELECT SUM(length(data)) FROM transaction_images), 0) + length(NEW.data) > %d
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

	for _, stmt := range statements {
		if _, err := target.Exec(stmt); err != nil {
			return fmt.Errorf("SQL実行エラー (%s): %w", stmt[:50], err)
		}
	}
	if err := validateCriticalSchema(target); err != nil {
		return err
	}

	return nil
}

func validateCriticalSchema(target *sql.DB) error {
	requiredColumns := map[string][]string{
		"transactions":               {"id", "account", "date", "item", "type", "amount", "balance", "memo"},
		"transaction_images":         {"id", "transaction_id", "filename", "data", "mime_type", "created_at"},
		"ai_transaction_idempotency": {"credential_id", "idempotency_key_sha256", "request_sha256", "transaction_id", "response_account", "response_date", "created_at"},
		"ai_daily_transaction_usage": {"credential_id", "utc_date", "successful_creates"},
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
		{objectType: "index", name: "idx_ai_idempotency_credential_key"},
		{objectType: "index", name: "idx_ai_idempotency_transaction"},
		{objectType: "index", name: "idx_ai_daily_usage_credential_date"},
		{objectType: "trigger", name: "trg_transaction_images_quota_insert"},
		{objectType: "trigger", name: "trg_transaction_images_immutable_update"},
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

	log.Printf("スナップショット作成完了: %s", snapshotPath)
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
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
			snapshots = append(snapshots, entry.Name())
		}
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

	if snapshotDir == "" {
		snapshotDir = filepath.Join(filepath.Dir(i.path), "snapshots")
	}

	snapshotPath := filepath.Join(snapshotDir, snapshotName)
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return fmt.Errorf("スナップショットが見つかりません: %s", snapshotName)
	}

	// --- 手順1: データベース接続の完全な遮断 ---
	currentPath := i.path
	if i.db != nil {
		// WALの内容をメインDBファイルにフラッシュしてからCloseする
		i.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		i.db.Close()
		i.db = nil
	}

	backupPath := currentPath + ".bak"
	restoreFailed := true
	var candidateDB *sql.DB

	// 失敗時は退避ファイルから元の状態に自動復旧する
	defer func() {
		if restoreFailed {
			log.Printf("復元失敗: 退避ファイルから元の状態に復旧します")
			if candidateDB != nil {
				_ = candidateDB.Close()
				candidateDB = nil
			}
			if i.db != nil {
				_ = i.db.Close()
				i.db = nil
			}
			os.Remove(currentPath)
			os.Remove(currentPath + "-wal")
			os.Remove(currentPath + "-shm")
			if err := os.Rename(backupPath, currentPath); err != nil {
				log.Printf("退避データベースの復旧エラー: %v", err)
				return
			}
			if err := i.initDBLocked(currentPath, i.opener, false); err != nil {
				log.Printf("復旧後のDB再接続エラー: %v", err)
			}
		}
	}()

	// --- 手順2: 現在状態の退避 ---
	if err := os.Rename(currentPath, backupPath); err != nil {
		// リネーム失敗時はそのまま再接続して返す
		restoreFailed = false
		i.initDBLocked(currentPath, i.opener, false)
		return fmt.Errorf("データベース退避エラー: %w", err)
	}

	// --- 手順3: WAL/SHM 一時ファイルの確実な消去 ---
	os.Remove(currentPath + "-wal")
	os.Remove(currentPath + "-shm")

	// --- 手順4: スナップショットの複製と配置 ---
	if err := copyFile(snapshotPath, currentPath); err != nil {
		return fmt.Errorf("スナップショットコピーエラー: %w", err)
	}

	// --- 手順5: 再接続と現行スキーマの再適用 ---
	if i.opener == nil {
		return fmt.Errorf("データベースopenerが初期化されていません")
	}
	newDB, err := i.opener.Open(context.Background(), currentPath, securedb.Writable)
	if err != nil {
		return fmt.Errorf("復元後のDB接続エラー: %w", err)
	}
	candidateDB = newDB
	if err := requireFullSynchronous(newDB); err != nil {
		return fmt.Errorf("復元後のDB耐久性設定エラー: %w", err)
	}

	// 破損DBへDDLを適用しないよう、移行前にも整合性を確認する。
	if err := i.checkIntegrity(newDB); err != nil {
		return err
	}
	if err := createTablesOn(newDB); err != nil {
		return fmt.Errorf("復元後のスキーマ更新エラー: %w", err)
	}

	// --- 手順6: スキーマ更新後の整合性の検査 ---
	if err := i.checkIntegrity(newDB); err != nil {
		return err
	}

	// --- 手順7: 参照の更新と退避ファイルの削除 ---
	i.db = newDB
	candidateDB = nil
	i.path = currentPath

	restoreFailed = false
	os.Remove(backupPath)

	log.Printf("スナップショット復元完了: %s (integrity_check: ok)", snapshotName)
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
		if !info.Mode().IsRegular() {
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
		log.Printf("古いスナップショットを削除: %s (remaining_bytes=%d)", victim.name, total)
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
			log.Printf("自動スナップショット作成エラー: %v", err)
		} else if err := i.cleanOldSnapshots("", 30); err != nil {
			log.Printf("スナップショットクリーンアップエラー: %v", err)
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
