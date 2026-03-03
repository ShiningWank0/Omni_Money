// Package database はSQLite接続、初期化、スナップショット機能を提供する
package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

var (
	db   *sql.DB
	mu   sync.RWMutex
	once sync.Once
)

// InitDB はSQLiteデータベースを初期化する
func InitDB(dbPath string) error {
	var initErr error
	once.Do(func() {
		if dbPath == "" {
			dbPath = "omni_money.db"
		}

		// データベースディレクトリが存在しない場合は作成
		dir := filepath.Dir(dbPath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				initErr = fmt.Errorf("データベースディレクトリ作成エラー: %w", err)
				return
			}
		}

		var err error
		db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			initErr = fmt.Errorf("データベース接続エラー: %w", err)
			return
		}

		// 接続テスト
		if err := db.Ping(); err != nil {
			initErr = fmt.Errorf("データベースping失敗: %w", err)
			return
		}

		// テーブル作成
		if err := createTables(); err != nil {
			initErr = fmt.Errorf("テーブル作成エラー: %w", err)
			return
		}

		log.Printf("データベース初期化完了: %s", dbPath)
	})
	return initErr
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
		// 設定テーブル
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		// インデックス
		`CREATE INDEX IF NOT EXISTS idx_transactions_account ON transactions(account)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_date ON transactions(date)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_item ON transactions(item)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("SQL実行エラー (%s): %w", stmt[:50], err)
		}
	}

	return nil
}
