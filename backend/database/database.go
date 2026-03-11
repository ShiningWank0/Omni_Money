// Package database はSQLite接続、初期化、スナップショット機能を提供する
package database

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
)

// InitDB はSQLiteデータベースを初期化する。
// wails build でバインディング生成時にも呼ばれるため、sync.Once は使わない。
// 既に接続がある場合はまず閉じてから再接続する。
func InitDB(path string) error {
	mu.Lock()
	defer mu.Unlock()

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
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("データベースディレクトリ作成エラー: %w", err)
		}
	}

	var err error
	db, err = sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON")
	if err != nil {
		return fmt.Errorf("データベース接続エラー: %w", err)
	}

	// 接続テスト
	if err := db.Ping(); err != nil {
		return fmt.Errorf("データベースping失敗: %w", err)
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
		`CREATE TABLE IF NOT EXISTS transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account TEXT NOT NULL,
			date DATETIME NOT NULL,
			item TEXT NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('income', 'expense')),
			amount INTEGER NOT NULL,
			balance INTEGER NOT NULL DEFAULT 0,
			memo TEXT DEFAULT ''
		)`,
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
	mu.RLock()
	currentPath := dbPath
	currentDB := db
	mu.RUnlock()

	if currentPath == "" {
		return "", fmt.Errorf("データベースが初期化されていません")
	}

	// WALの内容をメインDBファイルにフラッシュしてからコピーする
	if currentDB != nil {
		if _, err := currentDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			log.Printf("WALチェックポイント警告: %v", err)
		}
	}

	if snapshotDir == "" {
		snapshotDir = getSnapshotDir()
	}

	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return "", fmt.Errorf("スナップショットディレクトリ作成エラー: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405.000")
	// ドットをアンダースコアに置換してファイル名に安全な形式にする
	timestamp = strings.ReplaceAll(timestamp, ".", "_")
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("omni_money_%s.db", timestamp))

	// ファイルコピー
	if err := copyFile(currentPath, snapshotPath); err != nil {
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
	if snapshotDir == "" {
		snapshotDir = getSnapshotDir()
	}

	snapshotPath := filepath.Join(snapshotDir, snapshotName)
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return fmt.Errorf("スナップショットが見つかりません: %s", snapshotName)
	}

	// --- 手順1: データベース接続の完全な遮断 ---
	mu.Lock()
	currentPath := dbPath
	if db != nil {
		// WALの内容をメインDBファイルにフラッシュしてからCloseする
		db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		db.Close()
		db = nil
	}
	mu.Unlock()

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
			if err := InitDB(currentPath); err != nil {
				log.Printf("復旧後のDB再接続エラー: %v", err)
			}
		}
	}()

	// --- 手順2: 現在状態の退避 ---
	if err := os.Rename(currentPath, backupPath); err != nil {
		// リネーム失敗時はそのまま再接続して返す
		restoreFailed = false
		InitDB(currentPath)
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
	mu.Lock()
	db = newDB
	dbPath = currentPath
	mu.Unlock()

	restoreFailed = false
	os.Remove(backupPath)

	log.Printf("スナップショット復元完了: %s (integrity_check: ok)", snapshotName)
	return nil
}

// CleanOldSnapshots は古いスナップショットを削除する（世代管理: 最新N件を残す）
func CleanOldSnapshots(snapshotDir string, maxKeep int) error {
	if snapshotDir == "" {
		snapshotDir = getSnapshotDir()
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
		os.Remove(filepath.Join(snapshotDir, name))
		log.Printf("古いスナップショットを削除: %s", name)
	}
	return nil
}

// AutoSnapshot は操作ごとに自動スナップショットを作成し、30世代を維持する
func AutoSnapshot() {
	go func() {
		_, err := CreateSnapshot("")
		if err != nil {
			log.Printf("自動スナップショット作成エラー: %v", err)
			return
		}
		if err := CleanOldSnapshots("", 30); err != nil {
			log.Printf("スナップショットクリーンアップエラー: %v", err)
		}
	}()
}

// copyFile はファイルをコピーする
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
