// Package core はアプリケーションの主要な論理処理（ビジネスロジック）を提供する
package core

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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
	var rows interface{ Scan(...interface{}) error }
	var err error
	var sqlRows interface {
		Next() bool
		Scan(...interface{}) error
		Close() error
	}

	if account != "" {
		sqlRows2, err2 := db.Query(
			"SELECT DISTINCT item FROM transactions WHERE account = ? ORDER BY item", account)
		if err2 != nil {
			return nil, fmt.Errorf("項目リスト取得エラー: %w", err2)
		}
		defer sqlRows2.Close()
		_ = rows
		_ = err

		var items []string
		for sqlRows2.Next() {
			var item string
			if err := sqlRows2.Scan(&item); err != nil {
				return nil, fmt.Errorf("項目スキャンエラー: %w", err)
			}
			items = append(items, item)
		}
		return items, nil
	}

	sqlRows, err = db.Query("SELECT DISTINCT item FROM transactions ORDER BY item")
	if err != nil {
		return nil, fmt.Errorf("項目リスト取得エラー: %w", err)
	}
	defer sqlRows.Close()

	var items []string
	for sqlRows.Next() {
		var item string
		if err := sqlRows.Scan(&item); err != nil {
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
		query += " AND item LIKE ?"
		args = append(args, "%"+search+"%")
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
		transactions = append(transactions, t.ToResponse())
	}

	if transactions == nil {
		transactions = []models.TransactionResponse{}
	}
	return transactions, nil
}

// AddTransaction は新しい取引を追加する
func AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	db := database.GetDB()

	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return nil, err
	}

	if err := validateTransactionData(req); err != nil {
		return nil, err
	}

	// 指定された口座の最新残高を取得
	var currentBalance int64
	err = db.QueryRow(
		"SELECT balance FROM transactions WHERE account = ? ORDER BY date DESC, id DESC LIMIT 1",
		req.Account,
	).Scan(&currentBalance)
	if err != nil {
		currentBalance = 0 // 初回取引の場合
	}

	// 新しい残高を計算
	newBalance := currentBalance
	if req.Type == "income" {
		newBalance += req.Amount
	} else {
		newBalance -= req.Amount
	}

	result, err := db.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, ?, ?)",
		req.Account, date, req.Item, req.Type, req.Amount, newBalance, req.Memo,
	)
	if err != nil {
		return nil, fmt.Errorf("取引追加エラー: %w", err)
	}

	id, _ := result.LastInsertId()
	resp := &models.TransactionResponse{
		ID:       id,
		FundItem: req.Account,
		Account:  req.Account,
		Date:     date.Format("2006-01-02"),
		Item:     req.Item,
		Type:     req.Type,
		Amount:   req.Amount,
		Balance:  newBalance,
		Memo:     req.Memo,
	}
	if req.Time != "" {
		resp.Date = date.Format("2006-01-02 15:04:05")
	}

	return resp, nil
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

	return recalculateBalance(account)
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

// ImportCSV はCSVコンテンツからデータをインポートする
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

	// replaceモード
	if mode == "replace" {
		if _, err := db.Exec("DELETE FROM transactions"); err != nil {
			return 0, fmt.Errorf("既存データ削除エラー: %w", err)
		}
	}

	imported := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return imported, fmt.Errorf("CSV行読み取りエラー: %w", err)
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

		_, err = db.Exec(
			"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)",
			account, date, item, txType, amount, memo,
		)
		if err != nil {
			return imported, fmt.Errorf("CSVインポートエラー: %w", err)
		}
		imported++
	}

	// 全口座の残高を再計算
	accounts, _ := GetAccounts()
	for _, acc := range accounts {
		recalculateBalance(acc)
	}

	return imported, nil
}

// --- ヘルパー関数 ---

func recalculateBalance(account string) error {
	db := database.GetDB()
	rows, err := db.Query(
		"SELECT id, type, amount FROM transactions WHERE account = ? ORDER BY date, id",
		account,
	)
	if err != nil {
		return fmt.Errorf("残高再計算クエリエラー: %w", err)
	}
	defer rows.Close()

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
		if _, err := db.Exec("UPDATE transactions SET balance = ? WHERE id = ?", runningBalance, id); err != nil {
			return fmt.Errorf("残高更新エラー: %w", err)
		}
	}
	return nil
}

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
