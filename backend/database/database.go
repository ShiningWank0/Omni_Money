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
	"strings"
	"sync"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"omni_money/backend/validation"
)

var (
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex

	// snapshotLifecycle serializes database lifecycle changes with snapshot
	// operations.  A snapshot keeps a database handle and path for the whole
	// copy, so closing/reinitializing the database must wait for it to finish.
	snapshotLifecycle sync.RWMutex
	dbLifecycleMu     sync.Mutex
	snapshotMu        sync.Mutex
	snapshotCond      = sync.NewCond(&snapshotMu)
	snapshotRunning   bool
	snapshotPending   bool
	snapshotClosing   bool
)

// InitDB はSQLiteデータベースを初期化する。
// wails build でバインディング生成時にも呼ばれるため、sync.Once は使わない。
// 既に接続がある場合はまず閉じてから再接続する。
func InitDB(path string) error {
	beginDBLifecycle()
	defer endDBLifecycle()

	mu.Lock()
	defer mu.Unlock()
	return initDBLocked(path)
}

// initDBLocked は mu.Lock() を保持した状態で呼び出す前提の初期化本体。
// RestoreSnapshot のようにロックを保持したまま再初期化する経路と共有する。
func initDBLocked(path string) error {
	// 既存の接続があればまず閉じる
	if db != nil {
		db.Close()
		db = nil
	}

	if path == "" {
		path = "omni_money.db"
	}
	dbPath = path

	// データベースディレクトリが存在しない場合は作成
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("データベースディレクトリ作成エラー: %w", err)
		}
		if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- DB directory is intentionally private to the local user.
			return fmt.Errorf("データベースディレクトリ権限設定エラー: %w", err)
		}
	}

	var err error
	db, err = sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON")
	if err != nil {
		return fmt.Errorf("データベース接続エラー: %w", err)
	}

	// 接続テスト
	if err := db.Ping(); err != nil {
		db.Close()
		db = nil
		return fmt.Errorf("データベースping失敗: %w", err)
	}
	// SQLite creates the file on first open.  Restrict an existing database as
	// well: it may contain the user's complete financial history.
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		db = nil
		return fmt.Errorf("データベースファイル権限設定エラー: %w", err)
	}

	// テーブル作成
	if err := createTables(); err != nil {
		return fmt.Errorf("テーブル作成エラー: %w", err)
	}

	log.Printf("データベース初期化完了: %s", path)
	return nil
}

// GetDB はデータベース接続を返す
func GetDB() *sql.DB {
	mu.RLock()
	defer mu.RUnlock()
	return db
}

// CloseDB はデータベース接続を閉じる
func CloseDB() {
	beginDBLifecycle()
	defer endDBLifecycle()

	mu.Lock()
	defer mu.Unlock()
	if db != nil {
		db.Close()
		db = nil
		log.Println("データベース接続を閉じました")
	}
}

// createTables はデータベーステーブルを作成する
func createTables() error {
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
		// 設定テーブル
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		// インデックス
		`CREATE INDEX IF NOT EXISTS idx_transactions_account ON transactions(account)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_date ON transactions(date)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_item ON transactions(item)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_memo ON transactions(memo)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_images_txid ON transaction_images(transaction_id)`,
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
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("SQL実行エラー (%s): %w", stmt[:50], err)
		}
	}

	return nil
}

// --- スナップショット機能 (Agent.md §6.2) ---

// getSnapshotDir はDBパスと同じディレクトリ配下の snapshots/ を返す。
// ユーザーが保存場所を意識しなくて済むようにアプリデータ内に格納する。
func getSnapshotDir() string {
	mu.RLock()
	p := dbPath
	mu.RUnlock()
	if p == "" {
		return "snapshots"
	}
	return filepath.Join(filepath.Dir(p), "snapshots")
}

// CreateSnapshot は現在のDBファイルのスナップショットを作成する。
// snapshotDir にタイムスタンプ付きのコピーを保存する。
func CreateSnapshot(snapshotDir string) (string, error) {
	snapshotLifecycle.RLock()
	defer snapshotLifecycle.RUnlock()
	return createSnapshot(snapshotDir)
}

// createSnapshot performs the copy while holding the database lock.  It is
// called by CreateSnapshot and the auto-snapshot worker, both of which hold a
// read lock on snapshotLifecycle for the duration of the operation.
func createSnapshot(snapshotDir string) (string, error) {
	mu.Lock()
	defer mu.Unlock()

	currentPath := dbPath
	currentDB := db

	if currentPath == "" {
		return "", fmt.Errorf("データベースが初期化されていません")
	}

	if currentDB == nil {
		return "", fmt.Errorf("データベースが初期化されていません")
	}

	if snapshotDir == "" {
		snapshotDir = filepath.Join(filepath.Dir(currentPath), "snapshots")
	}

	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		return "", fmt.Errorf("スナップショットディレクトリ作成エラー: %w", err)
	}
	if err := os.Chmod(snapshotDir, 0700); err != nil { // #nosec G302 -- Snapshot directory is intentionally private to the local user.
		return "", fmt.Errorf("スナップショットディレクトリ権限設定エラー: %w", err)
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
	if err := backupSQLiteDatabase(currentDB, snapshotPath); err != nil {
		return "", fmt.Errorf("スナップショット作成エラー: %w", err)
	}

	log.Printf("スナップショット作成完了: %s", snapshotPath)
	return snapshotPath, nil
}

// ListSnapshots は利用可能なスナップショットのリストを返す
func ListSnapshots(snapshotDir string) ([]string, error) {
	if snapshotDir == "" {
		snapshotDir = getSnapshotDir()
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
//  5. 再接続し PRAGMA integrity_check で整合性を検証
//  6. 成功なら退避ファイルを削除、失敗なら退避から復旧
func RestoreSnapshot(snapshotDir, snapshotName string) error {
	// スナップショット名の検証（パストラバーサル防止）。
	// APIから任意の名前が渡り得るため、ディレクトリ区切りや ".." を含む名前、
	// snapshots/ 直下の .db ファイル以外は拒否する。
	if err := validateSnapshotName(snapshotName); err != nil {
		return err
	}

	beginDBLifecycle()
	defer endDBLifecycle()

	// 復元中に他のリクエストが nil の DB 接続へアクセスして panic しないよう、
	// ファイル差し替えと再接続が終わるまでロックを保持し続ける。
	mu.Lock()
	defer mu.Unlock()

	if snapshotDir == "" {
		snapshotDir = filepath.Join(filepath.Dir(dbPath), "snapshots")
	}

	snapshotPath := filepath.Join(snapshotDir, snapshotName)
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return fmt.Errorf("スナップショットが見つかりません: %s", snapshotName)
	}

	// --- 手順1: データベース接続の完全な遮断 ---
	currentPath := dbPath
	if db != nil {
		// WALの内容をメインDBファイルにフラッシュしてからCloseする
		db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		db.Close()
		db = nil
	}

	backupPath := currentPath + ".bak"
	restoreFailed := true

	// 失敗時は退避ファイルから元の状態に自動復旧する
	defer func() {
		if restoreFailed {
			log.Printf("復元失敗: 退避ファイルから元の状態に復旧します")
			os.Remove(currentPath)
			os.Remove(currentPath + "-wal")
			os.Remove(currentPath + "-shm")
			os.Rename(backupPath, currentPath)
			if err := initDBLocked(currentPath); err != nil {
				log.Printf("復旧後のDB再接続エラー: %v", err)
			}
		}
	}()

	// --- 手順2: 現在状態の退避 ---
	if err := os.Rename(currentPath, backupPath); err != nil {
		// リネーム失敗時はそのまま再接続して返す
		restoreFailed = false
		initDBLocked(currentPath)
		return fmt.Errorf("データベース退避エラー: %w", err)
	}

	// --- 手順3: WAL/SHM 一時ファイルの確実な消去 ---
	os.Remove(currentPath + "-wal")
	os.Remove(currentPath + "-shm")

	// --- 手順4: スナップショットの複製と配置 ---
	if err := copyFile(snapshotPath, currentPath); err != nil {
		return fmt.Errorf("スナップショットコピーエラー: %w", err)
	}

	// --- 手順5: 再接続と整合性の検査 ---
	newDB, err := sql.Open("sqlite3", currentPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON")
	if err != nil {
		return fmt.Errorf("復元後のDB接続エラー: %w", err)
	}

	var integrityResult string
	if err := newDB.QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		newDB.Close()
		return fmt.Errorf("整合性チェック実行エラー: %w", err)
	}
	if integrityResult != "ok" {
		newDB.Close()
		return fmt.Errorf("整合性チェック失敗: %s", integrityResult)
	}

	// --- 手順6: 参照の更新と退避ファイルの削除 ---
	db = newDB
	dbPath = currentPath

	restoreFailed = false
	os.Remove(backupPath)

	log.Printf("スナップショット復元完了: %s (integrity_check: ok)", snapshotName)
	return nil
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
	snapshotLifecycle.RLock()
	defer snapshotLifecycle.RUnlock()
	return cleanOldSnapshots(snapshotDir, maxKeep)
}

func cleanOldSnapshots(snapshotDir string, maxKeep int) error {
	if snapshotDir == "" {
		mu.RLock()
		path := dbPath
		mu.RUnlock()
		snapshotDir = filepath.Join(filepath.Dir(path), "snapshots")
	}
	if maxKeep <= 0 {
		maxKeep = 30
	}

	snapshots, err := ListSnapshots(snapshotDir)
	if err != nil {
		return err
	}

	// snapshotsは名前でソート済み（古い順）
	if len(snapshots) <= maxKeep {
		return nil
	}

	// 古いものから削除
	toDelete := snapshots[:len(snapshots)-maxKeep]
	for _, name := range toDelete {
		if err := os.Remove(filepath.Join(snapshotDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("古いスナップショット削除エラー (%s): %w", name, err)
		}
		log.Printf("古いスナップショットを削除: %s", name)
	}
	return nil
}

// AutoSnapshot は操作ごとに自動スナップショットを作成し、30世代を維持する
func AutoSnapshot() {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	if snapshotClosing {
		return
	}
	if snapshotRunning {
		// Coalesce bursts of writes into one follow-up snapshot.  This avoids
		// spawning an unbounded number of goroutines while preserving the
		// asynchronous API expected by callers.
		snapshotPending = true
		return
	}
	snapshotRunning = true
	go runAutoSnapshots()
}

func runAutoSnapshots() {
	for {
		snapshotLifecycle.RLock()
		_, err := createSnapshot("")
		if err != nil {
			log.Printf("自動スナップショット作成エラー: %v", err)
		} else if err := cleanOldSnapshots("", 30); err != nil {
			log.Printf("スナップショットクリーンアップエラー: %v", err)
		}
		snapshotLifecycle.RUnlock()

		snapshotMu.Lock()
		if snapshotPending && !snapshotClosing {
			snapshotPending = false
			snapshotMu.Unlock()
			continue
		}
		snapshotPending = false
		snapshotRunning = false
		snapshotCond.Broadcast()
		snapshotMu.Unlock()
		return
	}
}

// beginDBLifecycle prevents new automatic snapshots and waits for an already
// scheduled worker to finish before a database is closed or replaced.  The
// lifecycle lock also waits for direct CreateSnapshot calls that are already
// in progress.
func beginDBLifecycle() {
	// Serialize lifecycle transitions themselves.  Without this outer mutex,
	// two concurrent CloseDB/InitDB calls could both observe the closing state,
	// and the first one to finish could reopen the window while the second still
	// owns the database lifecycle lock.
	dbLifecycleMu.Lock()

	snapshotMu.Lock()
	snapshotClosing = true
	for snapshotRunning {
		snapshotCond.Wait()
	}
	snapshotMu.Unlock()
	snapshotLifecycle.Lock()
}

func endDBLifecycle() {
	snapshotLifecycle.Unlock()
	snapshotMu.Lock()
	snapshotClosing = false
	snapshotCond.Broadcast()
	snapshotMu.Unlock()
	dbLifecycleMu.Unlock()
}

// backupSQLiteDatabase はSQLiteのオンラインBackup APIで一貫した複製を作る。
func backupSQLiteDatabase(source *sql.DB, snapshotPath string) (err error) {
	// 先にprivate directory内へ排他的に作成し、既存ファイルやsymlinkを上書きしない。
	placeholder, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- path is generated inside the configured private snapshot directory.
	if err != nil {
		return err
	}
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(snapshotPath)
		return err
	}

	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.Remove(snapshotPath)
		}
	}()

	snapshotURI := (&url.URL{Scheme: "file", Path: snapshotPath}).String() + "?mode=rw&_busy_timeout=5000"
	destination, err := sql.Open("sqlite3", snapshotURI)
	if err != nil {
		return err
	}
	defer func() { _ = destination.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()

	destinationConn, err := destination.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()

	err = destinationConn.Raw(func(destinationDriverConn any) error {
		destinationSQLiteConn, ok := destinationDriverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected destination SQLite driver connection %T", destinationDriverConn)
		}
		return sourceConn.Raw(func(sourceDriverConn any) error {
			sourceSQLiteConn, ok := sourceDriverConn.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected source SQLite driver connection %T", sourceDriverConn)
			}

			backup, err := destinationSQLiteConn.Backup("main", sourceSQLiteConn, "main")
			if err != nil {
				return err
			}
			for {
				done, stepErr := backup.Step(128)
				if stepErr != nil {
					_ = backup.Finish()
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					_ = backup.Finish()
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	})
	if err != nil {
		return err
	}
	if err := os.Chmod(snapshotPath, 0600); err != nil {
		return err
	}
	succeeded = true
	return nil
}

// copyFile はファイルをコピーする
func copyFile(src, dst string) error {
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
	defer out.Close()
	if err := out.Chmod(0600); err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
