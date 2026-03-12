// Package core はアプリケーションの主要な論理処理（ビジネスロジック）を提供する
package core

import (
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

// GetAccounts はデータベースから口座名のリストを返す
func GetAccounts() ([]string, error) {
	db := database.GetDB()
	rows, err := db.Query("SELECT DISTINCT account FROM transactions ORDER BY account")
	if err != nil {
		return nil, fmt.Errorf("口座リスト取得エラー: %w", err)
	}
	defer rows.Close()

	var accounts []string
	for rows.Next() {
		var account string
		if err := rows.Scan(&account); err != nil {
			return nil, fmt.Errorf("口座スキャンエラー: %w", err)
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

// GetItems は項目名のリストを返す
func GetItems(account string) ([]string, error) {
	db := database.GetDB()

	var query string
	var args []interface{}
	if account != "" {
		query = "SELECT DISTINCT item FROM transactions WHERE account = ? ORDER BY item"
		args = []interface{}{account}
	} else {
		query = "SELECT DISTINCT item FROM transactions ORDER BY item"
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("項目リスト取得エラー: %w", err)
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, fmt.Errorf("項目スキャンエラー: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

// GetTransactions は取引履歴を返す
func GetTransactions(account string, search string) ([]models.TransactionResponse, error) {
	db := database.GetDB()

	query := "SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE 1=1"
	args := []interface{}{}

	if account != "" {
		query += " AND account = ?"
		args = append(args, account)
	}
	if search != "" {
		query += " AND (item LIKE ? OR memo LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	query += " ORDER BY date, id"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("取引履歴取得エラー: %w", err)
	}
	defer rows.Close()

	var transactions []models.TransactionResponse
	for rows.Next() {
		var t models.Transaction
		var dateStr string
		if err := rows.Scan(&t.ID, &t.Account, &dateStr, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo); err != nil {
			return nil, fmt.Errorf("取引スキャンエラー: %w", err)
		}
		t.Date = parseDate(dateStr)
		resp := t.ToResponse()
		resp.Tags, _ = GetTransactionTags(int64(t.ID))
		transactions = append(transactions, resp)
	}

	if transactions == nil {
		transactions = []models.TransactionResponse{}
	}
	return transactions, nil
}

// AddTransaction は新しい取引を追加する
// INSERT後にrecalculateBalanceで口座全体の残高を再計算するため、
// INSERT時のbalanceは仮値（0）で挿入する。
func AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	db := database.GetDB()

	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return nil, err
	}

	if err := validateTransactionData(req); err != nil {
		return nil, err
	}

	// INSERT（balanceは仮値0。直後にrecalculateBalanceで正しい値に上書きされる）
	result, err := db.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)",
		req.Account, date, req.Item, req.Type, req.Amount, req.Memo,
	)
	if err != nil {
		return nil, fmt.Errorf("取引追加エラー: %w", err)
	}

	id, _ := result.LastInsertId()

	// 画像添付処理
	if len(req.Images) > 0 {
		for _, img := range req.Images {
			if err := addTransactionImage(db, id, img); err != nil {
				// 画像添付エラーは警告として続行
				fmt.Printf("画像添付警告 (tx=%d): %v\n", id, err)
			}
		}
	}

	// タグ紐付け処理
	if len(req.Tags) > 0 {
		for _, tagID := range req.Tags {
			db.Exec("INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", id, tagID)
		}
	}

	// バックデート挿入も含め、口座全体の残高を時系列順に再計算
	if err := recalculateBalance(req.Account); err != nil {
		return nil, fmt.Errorf("残高再計算エラー: %w", err)
	}

	// 再計算後の正しい値を取得して返却
	var inserted models.Transaction
	var dateStr string
	err = db.QueryRow(
		"SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&inserted.ID, &inserted.Account, &dateStr, &inserted.Item, &inserted.Type, &inserted.Amount, &inserted.Balance, &inserted.Memo)
	if err != nil {
		return nil, fmt.Errorf("追加後データ取得エラー: %w", err)
	}
	inserted.Date = parseDate(dateStr)
	resp := inserted.ToResponse()
	resp.Tags, _ = GetTransactionTags(id)
	database.AutoSnapshot()
	return &resp, nil
}

// UpdateTransaction は既存の取引を更新する
func UpdateTransaction(id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	db := database.GetDB()

	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return nil, err
	}

	if err := validateTransactionData(req); err != nil {
		return nil, err
	}

	// 既存データの口座名を取得
	var oldAccount string
	err = db.QueryRow("SELECT account FROM transactions WHERE id = ?", id).Scan(&oldAccount)
	if err != nil {
		return nil, fmt.Errorf("取引が見つかりません: %w", err)
	}

	// 更新
	_, err = db.Exec(
		"UPDATE transactions SET account = ?, date = ?, item = ?, type = ?, amount = ?, memo = ? WHERE id = ?",
		req.Account, date, req.Item, req.Type, req.Amount, req.Memo, id,
	)
	if err != nil {
		return nil, fmt.Errorf("取引更新エラー: %w", err)
	}

	// タグの更新: 既存のタグを削除して再挿入
	db.Exec("DELETE FROM transaction_tags WHERE transaction_id = ?", id)
	if len(req.Tags) > 0 {
		for _, tagID := range req.Tags {
			db.Exec("INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", id, tagID)
		}
	}

	// 関連口座の残高を再計算
	accounts := []string{req.Account}
	if oldAccount != req.Account {
		accounts = append(accounts, oldAccount)
	}
	for _, acc := range accounts {
		if err := recalculateBalance(acc); err != nil {
			return nil, fmt.Errorf("残高再計算エラー: %w", err)
		}
	}

	// 更新後のデータを取得
	var t models.Transaction
	var dateStr string
	err = db.QueryRow(
		"SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&t.ID, &t.Account, &dateStr, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo)
	if err != nil {
		return nil, fmt.Errorf("更新後データ取得エラー: %w", err)
	}
	t.Date = parseDate(dateStr)
	resp := t.ToResponse()
	resp.Tags, _ = GetTransactionTags(int64(t.ID))
	database.AutoSnapshot()
	return &resp, nil
}

// DeleteTransaction は取引を削除する
func DeleteTransaction(id int64) error {
	db := database.GetDB()

	var account string
	err := db.QueryRow("SELECT account FROM transactions WHERE id = ?", id).Scan(&account)
	if err != nil {
		return fmt.Errorf("取引が見つかりません: %w", err)
	}

	if _, err := db.Exec("DELETE FROM transactions WHERE id = ?", id); err != nil {
		return fmt.Errorf("取引削除エラー: %w", err)
	}

	err = recalculateBalance(account)
	if err == nil {
		database.AutoSnapshot()
	}
	return err
}

// GetBalanceHistory は残高推移データを返す
func GetBalanceHistory() (*models.BalanceHistoryResponse, error) {
	db := database.GetDB()
	rows, err := db.Query(
		"SELECT account, date, balance FROM transactions ORDER BY date, id",
	)
	if err != nil {
		return nil, fmt.Errorf("残高履歴取得エラー: %w", err)
	}
	defer rows.Close()

	return buildBalanceHistory(rows)
}

// GetBalanceHistoryFiltered はクレジットカード除外を考慮した残高推移データを返す
func GetBalanceHistoryFiltered(fundItems []string) (*models.BalanceHistoryResponse, error) {
	if len(fundItems) == 0 {
		return &models.BalanceHistoryResponse{
			Accounts: []string{},
			Dates:    []string{},
			Balances: map[string][]int64{},
		}, nil
	}

	db := database.GetDB()

	// クレジットカード設定を取得
	creditCardItems, _ := GetCreditCardSettings()
	creditCardMap := make(map[string]bool)
	for _, item := range creditCardItems {
		creditCardMap[item] = true
	}

	// 全てクレジットカード項目かチェック
	allCredit := true
	for _, item := range fundItems {
		if !creditCardMap[item] {
			allCredit = false
			break
		}
	}

	var queryItems []string
	if allCredit {
		queryItems = fundItems
	} else {
		for _, item := range fundItems {
			if !creditCardMap[item] {
				queryItems = append(queryItems, item)
			}
		}
	}

	if len(queryItems) == 0 {
		queryItems = fundItems
	}

	// IN句を構築
	placeholders := make([]string, len(queryItems))
	args := make([]interface{}, len(queryItems))
	for i, item := range queryItems {
		placeholders[i] = "?"
		args[i] = item
	}

	query := fmt.Sprintf(
		"SELECT account, date, balance FROM transactions WHERE account IN (%s) ORDER BY date, id",
		strings.Join(placeholders, ","),
	)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("フィルタリング残高履歴取得エラー: %w", err)
	}
	defer rows.Close()

	return buildBalanceHistory(rows)
}

// GetCreditCardSettings はクレジットカード設定を取得する
func GetCreditCardSettings() ([]string, error) {
	db := database.GetDB()
	var value string
	err := db.QueryRow("SELECT value FROM settings WHERE key = 'credit_card_items'").Scan(&value)
	if err != nil {
		return []string{}, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return []string{}, nil
	}
	return items, nil
}

// SaveCreditCardSettings はクレジットカード設定を保存する
func SaveCreditCardSettings(items []string) error {
	db := database.GetDB()
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("JSONシリアライズエラー: %w", err)
	}
	_, err = db.Exec(
		"INSERT OR REPLACE INTO settings (key, value) VALUES ('credit_card_items', ?)",
		string(data),
	)
	if err != nil {
		return fmt.Errorf("クレジットカード設定保存エラー: %w", err)
	}
	database.AutoSnapshot()
	return nil
}

// BackupToCSV はCSVバックアップファイルのパスを返す
func BackupToCSV() (string, error) {
	db := database.GetDB()
	rows, err := db.Query(
		"SELECT id, account, date, item, type, amount, balance, memo FROM transactions ORDER BY date",
	)
	if err != nil {
		return "", fmt.Errorf("バックアップ用データ取得エラー: %w", err)
	}
	defer rows.Close()

	var builder strings.Builder
	writer := csv.NewWriter(&builder)

	// ヘッダー
	writer.Write([]string{"id", "account", "date", "item", "type", "amount", "balance", "memo"})

	for rows.Next() {
		var id, amount, balance int64
		var account, dateStr, item, txType, memo string
		if err := rows.Scan(&id, &account, &dateStr, &item, &txType, &amount, &balance, &memo); err != nil {
			return "", fmt.Errorf("バックアップスキャンエラー: %w", err)
		}
		writer.Write([]string{
			fmt.Sprintf("%d", id),
			account,
			dateStr,
			item,
			txType,
			fmt.Sprintf("%d", amount),
			fmt.Sprintf("%d", balance),
			memo,
		})
	}
	writer.Flush()

	return builder.String(), nil
}

// BackupToCSVFile はCSVバックアップファイルをユーザーのダウンロードフォルダに保存する
func BackupToCSVFile() (string, error) {
	csvContent, err := BackupToCSV()
	if err != nil {
		return "", err
	}

	downloadsDir, err := getDownloadsDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		return "", fmt.Errorf("ダウンロードフォルダ作成エラー: %w", err)
	}

	filename := fmt.Sprintf("transactions_backup_%s.csv", time.Now().Format("2006-01-02"))
	filePath := filepath.Join(downloadsDir, filename)

	// BOMを付与してExcel互換にする
	bom := "\xEF\xBB\xBF"
	if err := os.WriteFile(filePath, []byte(bom+csvContent), 0644); err != nil {
		return "", fmt.Errorf("CSVファイル書き出しエラー: %w", err)
	}

	return filePath, nil
}

// getDownloadsDir はOS標準のダウンロードフォルダパスを返す
func getDownloadsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリ取得エラー: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		// Windows: %USERPROFILE%\Downloads
		return filepath.Join(homeDir, "Downloads"), nil
	case "darwin":
		// macOS: ~/Downloads
		return filepath.Join(homeDir, "Downloads"), nil
	default:
		// Linux: XDG_DOWNLOAD_DIR or ~/Downloads
		if xdgDownload := os.Getenv("XDG_DOWNLOAD_DIR"); xdgDownload != "" {
			return xdgDownload, nil
		}
		return filepath.Join(homeDir, "Downloads"), nil
	}
}

// ImportCSV はCSVコンテンツからデータをインポートする。
// replaceモードでは既存データのDELETEとINSERTをトランザクションで包み、
// 途中失敗時にデータが消失しないようにする。
func ImportCSV(content string, mode string) (int, error) {
	db := database.GetDB()

	reader := csv.NewReader(strings.NewReader(content))
	headers, err := reader.Read()
	if err != nil {
		return 0, fmt.Errorf("CSVヘッダー読み取りエラー: %w", err)
	}

	// ヘッダーのインデックスを特定
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[strings.TrimSpace(h)] = i
	}

	requiredHeaders := []string{"account", "date", "item", "type", "amount"}
	for _, h := range requiredHeaders {
		if _, ok := headerMap[h]; !ok {
			return 0, fmt.Errorf("必須ヘッダーが不足: %s", h)
		}
	}

	// トランザクション開始
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	// replaceモード: トランザクション内でDELETE
	if mode == "replace" {
		if _, err := tx.Exec("DELETE FROM transactions"); err != nil {
			return 0, fmt.Errorf("既存データ削除エラー: %w", err)
		}
	}

	stmt, err := tx.Prepare(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)")
	if err != nil {
		return 0, fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	defer stmt.Close()

	imported := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("CSV行読み取りエラー (行%d): %w", imported+2, err)
		}

		account := strings.TrimSpace(record[headerMap["account"]])
		dateStr := strings.TrimSpace(record[headerMap["date"]])
		item := strings.TrimSpace(record[headerMap["item"]])
		txType := strings.ToLower(strings.TrimSpace(record[headerMap["type"]]))
		amountStr := strings.TrimSpace(record[headerMap["amount"]])

		var amount int64
		fmt.Sscanf(amountStr, "%d", &amount)

		memo := ""
		if idx, ok := headerMap["memo"]; ok && idx < len(record) {
			memo = strings.TrimSpace(record[idx])
		}

		date := parseDate(dateStr)

		_, err = stmt.Exec(account, date, item, txType, amount, memo)
		if err != nil {
			return 0, fmt.Errorf("CSVインポートエラー (行%d): %w", imported+2, err)
		}
		imported++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("インポートコミットエラー: %w", err)
	}

	// 全口座の残高を再計算
	accounts, _ := GetAccounts()
	for _, acc := range accounts {
		if err := recalculateBalance(acc); err != nil {
			return imported, fmt.Errorf("残高再計算エラー (%s): %w", acc, err)
		}
	}

	database.AutoSnapshot()
	return imported, nil
}

// --- ヘルパー関数 ---

// recalculateBalance は口座内の全取引を時系列順に辿り、残高を再計算する。
// 複数のUPDATEをSQLトランザクションで包み、途中失敗時のデータ不整合を防止する。
func recalculateBalance(account string) error {
	db := database.GetDB()

	// 時系列順で取引データを取得
	rows, err := db.Query(
		"SELECT id, type, amount FROM transactions WHERE account = ? ORDER BY date, id",
		account,
	)
	if err != nil {
		return fmt.Errorf("残高再計算クエリエラー: %w", err)
	}
	defer rows.Close()

	// メモリに読み込んでからトランザクション内で一括更新
	type balanceUpdate struct {
		id      int64
		balance int64
	}
	var updates []balanceUpdate
	var runningBalance int64

	for rows.Next() {
		var id, amount int64
		var txType string
		if err := rows.Scan(&id, &txType, &amount); err != nil {
			return fmt.Errorf("残高再計算スキャンエラー: %w", err)
		}
		if txType == "income" {
			runningBalance += amount
		} else {
			runningBalance -= amount
		}
		updates = append(updates, balanceUpdate{id: id, balance: runningBalance})
	}
	rows.Close() // 明示的に閉じてからトランザクションを開始

	if len(updates) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("UPDATE transactions SET balance = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.Exec(u.balance, u.id); err != nil {
			return fmt.Errorf("残高更新エラー (id=%d): %w", u.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	return nil
}

// parseDate は複数の受け入れ可能なフォーマットを許容し、
// どれにも一致しない場合は現在時刻を返す。
func parseDate(dateStr string) time.Time {
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t
		}
	}
	return time.Now()
}

func parseTransactionDate(dateStr, timeStr string) (time.Time, error) {
	if timeStr != "" {
		combined := fmt.Sprintf("%s %s", dateStr, timeStr)
		t, err := time.Parse("2006-01-02 15:04", combined)
		if err != nil {
			return time.Time{}, fmt.Errorf("日時形式が正しくありません: %w", err)
		}
		return t, nil
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("日付形式が正しくありません: %w", err)
	}
	return t, nil
}

func validateTransactionData(req models.TransactionRequest) error {
	if req.Account == "" {
		return fmt.Errorf("口座名は必須です")
	}
	if req.Item == "" {
		return fmt.Errorf("項目は必須です")
	}
	if req.Type != "income" && req.Type != "expense" {
		return fmt.Errorf("種別はincomeまたはexpenseである必要があります")
	}
	if req.Amount <= 0 {
		return fmt.Errorf("金額は正の数値である必要があります")
	}
	return nil
}

func buildBalanceHistory(rows interface {
	Next() bool
	Scan(...interface{}) error
}) (*models.BalanceHistoryResponse, error) {
	accountBalances := make(map[string]map[string]int64)
	allDates := make(map[string]bool)

	type rowData struct {
		account, dateStr string
		balance          int64
	}

	for rows.Next() {
		var account, dateStr string
		var balance int64
		if err := rows.Scan(&account, &dateStr, &balance); err != nil {
			return nil, fmt.Errorf("残高履歴スキャンエラー: %w", err)
		}
		date := parseDate(dateStr)
		dateKey := date.Format("2006-01-02")

		if _, ok := accountBalances[account]; !ok {
			accountBalances[account] = make(map[string]int64)
		}
		accountBalances[account][dateKey] = balance
		allDates[dateKey] = true
	}

	// 日付をソート
	dates := make([]string, 0, len(allDates))
	for d := range allDates {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	// 各口座の残高データを整理
	accounts := make([]string, 0, len(accountBalances))
	for acc := range accountBalances {
		accounts = append(accounts, acc)
	}
	sort.Strings(accounts)

	balances := make(map[string][]int64)
	for _, acc := range accounts {
		balances[acc] = make([]int64, len(dates))
		var lastBalance int64
		for i, date := range dates {
			if b, ok := accountBalances[acc][date]; ok {
				lastBalance = b
			}
			balances[acc][i] = lastBalance
		}
	}

	return &models.BalanceHistoryResponse{
		Accounts: accounts,
		Dates:    dates,
		Balances: balances,
	}, nil
}

// --- 画像管理 (Agent.md §6.5) ---

// addTransactionImage は取引に画像を追加する（内部ヘルパー）
func addTransactionImage(db *sql.DB, transactionID int64, img models.TransactionImageRequest) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("Base64デコードエラー: %w", err)
	}

	mimeType := img.MimeType
	if mimeType == "" {
		mimeType = guessMimeType(img.Filename)
	}

	_, err = db.Exec(
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, ?, ?, ?)",
		transactionID, img.Filename, data, mimeType,
	)
	return err
}

// AddTransactionImage は取引に画像を追加する
func AddTransactionImage(transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	db := database.GetDB()
	if err := addTransactionImage(db, transactionID, img); err != nil {
		return nil, err
	}

	// 追加された画像のIDを取得
	var id int64
	var createdAt string
	err := db.QueryRow(
		"SELECT id, created_at FROM transaction_images WHERE transaction_id = ? ORDER BY id DESC LIMIT 1",
		transactionID,
	).Scan(&id, &createdAt)
	if err != nil {
		return nil, err
	}

	resp := &models.TransactionImageResponse{
		ID:        id,
		Filename:  img.Filename,
		MimeType:  img.MimeType,
		CreatedAt: createdAt,
	}
	database.AutoSnapshot()
	return resp, nil
}

// GetTransactionImages は取引の画像一覧を返す
func GetTransactionImages(transactionID int64) ([]models.TransactionImageResponse, error) {
	db := database.GetDB()
	rows, err := db.Query(
		"SELECT id, filename, data, mime_type, created_at FROM transaction_images WHERE transaction_id = ? ORDER BY created_at",
		transactionID,
	)
	if err != nil {
		return nil, fmt.Errorf("画像一覧取得エラー: %w", err)
	}
	defer rows.Close()

	var images []models.TransactionImageResponse
	for rows.Next() {
		var id int64
		var filename, mimeType, createdAt string
		var data []byte
		if err := rows.Scan(&id, &filename, &data, &mimeType, &createdAt); err != nil {
			return nil, err
		}
		images = append(images, models.TransactionImageResponse{
			ID:        id,
			Filename:  filename,
			MimeType:  mimeType,
			CreatedAt: createdAt,
			DataURL:   fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)),
		})
	}
	if images == nil {
		images = []models.TransactionImageResponse{}
	}
	return images, nil
}

// DeleteTransactionImage は取引から画像を削除する
func DeleteTransactionImage(imageID int64) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM transaction_images WHERE id = ?", imageID)
	if err == nil {
		database.AutoSnapshot()
	}
	return err
}

// guessMimeType はファイル名からMIMEタイプを推定する
func guessMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// --- タグ管理 (Agent.md §6.6) ---

// CreateTag は新しいタグを作成する
func CreateTag(name string, parentID *int64) (*models.Tag, error) {
	db := database.GetDB()

	level := 1
	if parentID != nil {
		var parentLevel int
		err := db.QueryRow("SELECT level FROM tags WHERE id = ?", *parentID).Scan(&parentLevel)
		if err != nil {
			return nil, fmt.Errorf("親タグが見つかりません: %w", err)
		}
		if parentLevel >= 3 {
			return nil, fmt.Errorf("タグは3階層までです")
		}
		level = parentLevel + 1
	}

	result, err := db.Exec(
		"INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)",
		name, parentID, level,
	)
	if err != nil {
		return nil, fmt.Errorf("タグ作成エラー: %w", err)
	}

	id, _ := result.LastInsertId()
	tag := &models.Tag{
		ID:       id,
		Name:     name,
		ParentID: parentID,
		Level:    level,
	}
	database.AutoSnapshot()
	return tag, nil
}

// GetTags はタグ一覧をツリー構造で返す
func GetTags() ([]models.Tag, error) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, name, parent_id, level FROM tags ORDER BY level, name")
	if err != nil {
		return nil, fmt.Errorf("タグ一覧取得エラー: %w", err)
	}
	defer rows.Close()

	var allTags []models.Tag
	tagMap := make(map[int64]*models.Tag)

	for rows.Next() {
		var tag models.Tag
		var parentID sql.NullInt64
		if err := rows.Scan(&tag.ID, &tag.Name, &parentID, &tag.Level); err != nil {
			return nil, err
		}
		if parentID.Valid {
			pid := parentID.Int64
			tag.ParentID = &pid
		}
		allTags = append(allTags, tag)
		tagMap[tag.ID] = &allTags[len(allTags)-1]
	}

	// ツリー構造を構築
	var rootTags []models.Tag
	for i := range allTags {
		tag := &allTags[i]
		if tag.ParentID == nil {
			rootTags = append(rootTags, *tag)
		} else {
			if parent, ok := tagMap[*tag.ParentID]; ok {
				parent.Children = append(parent.Children, *tag)
			}
		}
	}

	// rootTagsの子を再帰的に設定
	for i := range rootTags {
		populateChildren(&rootTags[i], tagMap, allTags)
	}

	if rootTags == nil {
		rootTags = []models.Tag{}
	}
	return rootTags, nil
}

// populateChildren は再帰的に子タグを設定する
func populateChildren(tag *models.Tag, tagMap map[int64]*models.Tag, allTags []models.Tag) {
	var children []models.Tag
	for _, t := range allTags {
		if t.ParentID != nil && *t.ParentID == tag.ID {
			child := t
			populateChildren(&child, tagMap, allTags)
			children = append(children, child)
		}
	}
	tag.Children = children
}

// UpdateTag はタグ名を更新する
func UpdateTag(id int64, name string) error {
	db := database.GetDB()
	_, err := db.Exec("UPDATE tags SET name = ? WHERE id = ?", name, id)
	if err == nil {
		database.AutoSnapshot()
	}
	return err
}

// CreateTagByPath は「/」区切りのパスからタグを階層的に作成する
// 例: "推し活/超かぐや姫！" → 「推し活」(L1) → 「超かぐや姫！」(L2) を作成
func CreateTagByPath(path string) (*models.Tag, error) {
	db := database.GetDB()

	parts := strings.Split(path, "/")
	var segments []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			segments = append(segments, p)
		}
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("タグ名が空です")
	}
	if len(segments) > 3 {
		return nil, fmt.Errorf("タグは3階層までです")
	}

	var parentID *int64
	var tag *models.Tag

	for i, name := range segments {
		level := i + 1
		var existingID int64
		var err error

		if parentID == nil {
			err = db.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id IS NULL", name).Scan(&existingID)
		} else {
			err = db.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id = ?", name, *parentID).Scan(&existingID)
		}

		if err == nil {
			tag = &models.Tag{ID: existingID, Name: name, ParentID: parentID, Level: level}
			pid := existingID
			parentID = &pid
		} else {
			result, insertErr := db.Exec(
				"INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)",
				name, parentID, level,
			)
			if insertErr != nil {
				return nil, fmt.Errorf("タグ作成エラー: %w", insertErr)
			}
			id, _ := result.LastInsertId()
			tag = &models.Tag{ID: id, Name: name, ParentID: parentID, Level: level}
			pid := id
			parentID = &pid
		}
	}

	database.AutoSnapshot()
	return tag, nil
}

// DeleteTag はタグを削除する（子タグも連鎖削除）
func DeleteTag(id int64) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM tags WHERE id = ?", id)
	if err == nil {
		database.AutoSnapshot()
	}
	return err
}

// GetTransactionTags は取引に紐付いたタグを返す
func GetTransactionTags(transactionID int64) ([]models.Tag, error) {
	db := database.GetDB()
	rows, err := db.Query(
		"SELECT t.id, t.name, t.parent_id, t.level FROM tags t INNER JOIN transaction_tags tt ON t.id = tt.tag_id WHERE tt.transaction_id = ? ORDER BY t.level, t.name",
		transactionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []models.Tag
	for rows.Next() {
		var tag models.Tag
		var parentID sql.NullInt64
		if err := rows.Scan(&tag.ID, &tag.Name, &parentID, &tag.Level); err != nil {
			return nil, err
		}
		if parentID.Valid {
			pid := parentID.Int64
			tag.ParentID = &pid
		}
		tags = append(tags, tag)
	}
	if tags == nil {
		tags = []models.Tag{}
	}
	return tags, nil
}

// AddTransactionTags は取引にタグを追加する
func AddTransactionTags(transactionID int64, tagIDs []int64) error {
	db := database.GetDB()
	for _, tagID := range tagIDs {
		_, err := db.Exec(
			"INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)",
			transactionID, tagID,
		)
		if err != nil {
			return fmt.Errorf("タグ追加エラー: %w", err)
		}
	}
	database.AutoSnapshot()
	return nil
}

// RemoveTransactionTag は取引からタグを削除する
func RemoveTransactionTag(transactionID, tagID int64) error {
	db := database.GetDB()
	_, err := db.Exec(
		"DELETE FROM transaction_tags WHERE transaction_id = ? AND tag_id = ?",
		transactionID, tagID,
	)
	if err == nil {
		database.AutoSnapshot()
	}
	return err
}

// GetTagSummary はタグ別集計データを返す（円グラフ用）
func GetTagSummary(txType string, startDate, endDate string) ([]models.TagSummary, error) {
	db := database.GetDB()

	// メインクエリ: タグ別の金額集計
	query := `SELECT t.id, t.name, t.level, t.parent_id,
		COALESCE(SUM(tr.amount), 0) as total_amount,
		COUNT(tr.id) as tx_count
		FROM tags t
		LEFT JOIN transaction_tags tt ON t.id = tt.tag_id
		LEFT JOIN transactions tr ON tt.transaction_id = tr.id`

	conditions := []string{}
	args := []interface{}{}

	if txType != "" {
		conditions = append(conditions, "tr.type = ?")
		args = append(args, txType)
	}
	if startDate != "" {
		conditions = append(conditions, "tr.date >= ?")
		args = append(args, startDate)
	}
	if endDate != "" {
		conditions = append(conditions, "tr.date <= ?")
		args = append(args, endDate+" 23:59:59")
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " GROUP BY t.id ORDER BY total_amount DESC"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("タグ集計エラー: %w", err)
	}
	defer rows.Close()

	type tagData struct {
		id       int64
		name     string
		level    int
		parentID sql.NullInt64
		amount   int64
		count    int
	}
	var allData []tagData
	var totalAmount int64

	for rows.Next() {
		var td tagData
		if err := rows.Scan(&td.id, &td.name, &td.level, &td.parentID, &td.amount, &td.count); err != nil {
			return nil, err
		}
		allData = append(allData, td)
		if td.level == 1 {
			totalAmount += td.amount
		}
	}

	// ツリー構造の集計データを構築
	var buildSummary func(parentID *int64) []models.TagSummary
	buildSummary = func(parentID *int64) []models.TagSummary {
		var summaries []models.TagSummary
		for _, td := range allData {
			match := false
			if parentID == nil && !td.parentID.Valid {
				match = true
			} else if parentID != nil && td.parentID.Valid && td.parentID.Int64 == *parentID {
				match = true
			}
			if match {
				ratio := float64(0)
				if totalAmount > 0 {
					ratio = float64(td.amount) / float64(totalAmount)
				}
				s := models.TagSummary{
					TagID:    td.id,
					TagName:  td.name,
					Amount:   td.amount,
					Count:    td.count,
					Ratio:    ratio,
					Children: buildSummary(&td.id),
				}
				summaries = append(summaries, s)
			}
		}
		return summaries
	}

	result := buildSummary(nil)
	if result == nil {
		result = []models.TagSummary{}
	}
	return result, nil
}

// --- AI分析 (Agent.md §6.3) ---

// AnalyzeTransactions はAIエージェント向けの取引分析を行う
func AnalyzeTransactions(req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	db := database.GetDB()

	query := "SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE 1=1"
	args := []interface{}{}

	if req.Account != "" {
		query += " AND account = ?"
		args = append(args, req.Account)
	}
	if req.Type != "" {
		query += " AND type = ?"
		args = append(args, req.Type)
	}
	if req.StartDate != "" {
		query += " AND date >= ?"
		args = append(args, req.StartDate)
	}
	if req.EndDate != "" {
		query += " AND date <= ?"
		args = append(args, req.EndDate+" 23:59:59")
	}

	// タグフィルタ
	if len(req.TagIDs) > 0 {
		placeholders := make([]string, len(req.TagIDs))
		for i, id := range req.TagIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += fmt.Sprintf(" AND id IN (SELECT transaction_id FROM transaction_tags WHERE tag_id IN (%s))",
			strings.Join(placeholders, ","))
	}

	query += " ORDER BY date, id"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("分析クエリエラー: %w", err)
	}
	defer rows.Close()

	resp := &models.AnalysisResponse{}
	for rows.Next() {
		var t models.Transaction
		var dateStr string
		if err := rows.Scan(&t.ID, &t.Account, &dateStr, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo); err != nil {
			return nil, err
		}
		t.Date = parseDate(dateStr)
		txResp := t.ToResponse()
		resp.Transactions = append(resp.Transactions, txResp)
		resp.Count++
		if t.Type == "income" {
			resp.TotalIncome += t.Amount
		} else {
			resp.TotalExpense += t.Amount
		}
	}
	resp.NetAmount = resp.TotalIncome - resp.TotalExpense

	// タグ別集計も含める
	tagSummaries, _ := GetTagSummary(req.Type, req.StartDate, req.EndDate)
	resp.TagSummaries = tagSummaries

	if resp.Transactions == nil {
		resp.Transactions = []models.TransactionResponse{}
	}

	return resp, nil
}
