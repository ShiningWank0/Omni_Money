// Package core はアプリケーションの主要な論理処理（ビジネスロジック）を提供する
package core

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/database"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

const (
	csvVersionHeader = "omni_money_csv_version"
	csvVersion2      = "2"
)

func encodeCSVTextCell(value string) string {
	if needsCSVFormulaEscape(value) {
		return "'" + value
	}
	return value
}

func decodeCSVTextCellV2(value string) (string, error) {
	if strings.HasPrefix(value, "'") {
		decoded := strings.TrimPrefix(value, "'")
		if !needsCSVFormulaEscape(decoded) {
			return "", fmt.Errorf("不要なCSVエスケープです")
		}
		return decoded, nil
	}
	if needsCSVFormulaEscape(value) {
		return "", fmt.Errorf("危険なCSVセルがエスケープされていません")
	}
	return value, nil
}

func needsCSVFormulaEscape(value string) bool {
	if value == "" {
		return false
	}
	r, size := utf8.DecodeRuneInString(value)
	if r == utf8.RuneError && size == 1 {
		return true
	}
	if strings.ContainsRune("=+-@'", r) {
		return true
	}
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
}

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
		query = "SELECT item FROM transactions WHERE account = ? GROUP BY item ORDER BY COUNT(*) DESC, item ASC"
		args = []interface{}{account}
	} else {
		query = "SELECT item FROM transactions GROUP BY item ORDER BY COUNT(*) DESC, item ASC"
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

	whereClause := " WHERE 1=1"
	args := []interface{}{}

	if account != "" {
		whereClause += " AND account = ?"
		args = append(args, account)
	}
	if search != "" {
		whereClause += " AND (item LIKE ? OR memo LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	query := "SELECT id, account, date, item, type, amount, balance, memo FROM transactions" + whereClause
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
		transactions = append(transactions, resp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("取引履歴行取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("取引履歴行クローズエラー: %w", err)
	}

	// 取引ごとのタグを1件ずつ取得すると取引件数に比例してSQLが増えるため、
	// 同じ取引フィルターを使った1回のJOINでまとめて取得する。
	tagsByTransaction, err := getTransactionTagsForFilteredTransactions(db, whereClause, args)
	if err != nil {
		return nil, fmt.Errorf("取引タグ一括取得エラー: %w", err)
	}
	for i := range transactions {
		tags := tagsByTransaction[transactions[i].ID]
		if tags == nil {
			tags = []models.Tag{}
		}
		transactions[i].Tags = tags
	}

	if transactions == nil {
		transactions = []models.TransactionResponse{}
	}
	return transactions, nil
}

// getTransactionTagsForFilteredTransactions はGetTransactionsと同じ条件に一致する
// 取引のタグを一括取得する。タグ順序は従来のGetTransactionTagsと同じlevel, name順。
func getTransactionTagsForFilteredTransactions(db *sql.DB, whereClause string, args []interface{}) (map[int64][]models.Tag, error) {
	query := `WITH filtered_transactions AS (
		SELECT id FROM transactions` + whereClause + `
	)
	SELECT ft.id, t.id, t.name, t.parent_id, t.level
	FROM filtered_transactions ft
	INNER JOIN transaction_tags tt ON tt.transaction_id = ft.id
	INNER JOIN tags t ON t.id = tt.tag_id
	ORDER BY ft.id, t.level, t.name`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tagsByTransaction := make(map[int64][]models.Tag)
	for rows.Next() {
		var transactionID int64
		var tag models.Tag
		var parentID sql.NullInt64
		if err := rows.Scan(&transactionID, &tag.ID, &tag.Name, &parentID, &tag.Level); err != nil {
			return nil, err
		}
		if parentID.Valid {
			pid := parentID.Int64
			tag.ParentID = &pid
		}
		tagsByTransaction[transactionID] = append(tagsByTransaction[transactionID], tag)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tagsByTransaction, nil
}

// AddTransaction は新しい取引を追加する
// INSERT後にrecalculateBalanceで口座全体の残高を再計算するため、
// INSERT時のbalanceは仮値（0）で挿入する。
func AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	db := database.GetDB()
	prepared, err := prepareTransactionInsert(req)
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	resp, err := addPreparedTransactionIn(tx, prepared)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	resp.Tags, _ = GetTransactionTags(resp.ID)
	database.AutoSnapshot()
	return resp, nil
}

type preparedTransactionInsert struct {
	request models.TransactionRequest
	date    time.Time
	images  []preparedTransactionImage
}

// prepareTransactionInsert performs all validation and expensive image
// decoding before a SQL write transaction is opened. Both UI and AI writes use
// this exact path so their ledger semantics remain identical.
func prepareTransactionInsert(req models.TransactionRequest) (preparedTransactionInsert, error) {
	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return preparedTransactionInsert{}, err
	}
	if err := validateTransactionData(req); err != nil {
		return preparedTransactionInsert{}, err
	}
	preparedImages, err := prepareTransactionImages(req.Images)
	if err != nil {
		return preparedTransactionInsert{}, err
	}
	return preparedTransactionInsert{request: req, date: date, images: preparedImages}, nil
}

// addPreparedTransactionIn mutates the ledger using the caller-owned SQL
// transaction. The caller alone decides whether to commit, which lets the AI
// path atomically combine idempotency, quota accounting, and the ledger write.
func addPreparedTransactionIn(tx *sql.Tx, prepared preparedTransactionInsert) (*models.TransactionResponse, error) {
	req := prepared.request
	result, err := tx.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)",
		req.Account, prepared.date, req.Item, req.Type, req.Amount, req.Memo,
	)
	if err != nil {
		return nil, fmt.Errorf("取引追加エラー: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("取引ID取得エラー: %w", err)
	}

	if err := insertPreparedTransactionImages(tx, id, prepared.images); err != nil {
		return nil, err
	}
	for _, tagID := range req.Tags {
		if _, err := tx.Exec("INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", id, tagID); err != nil {
			return nil, fmt.Errorf("タグ紐付けエラー: %w", err)
		}
	}

	if err := recalculateBalanceIn(tx, req.Account); err != nil {
		return nil, fmt.Errorf("残高再計算エラー: %w", err)
	}

	var inserted models.Transaction
	var dateStr string
	if err := tx.QueryRow(
		"SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&inserted.ID, &inserted.Account, &dateStr, &inserted.Item, &inserted.Type, &inserted.Amount, &inserted.Balance, &inserted.Memo); err != nil {
		return nil, fmt.Errorf("追加後データ取得エラー: %w", err)
	}
	inserted.Date = parseDate(dateStr)
	response := inserted.ToResponse()
	return &response, nil
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
	preparedImages, err := prepareTransactionImages(req.Images)
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	// 既存データの口座名を取得
	var oldAccount string
	err = tx.QueryRow("SELECT account FROM transactions WHERE id = ?", id).Scan(&oldAccount)
	if err != nil {
		return nil, fmt.Errorf("取引が見つかりません: %w", err)
	}

	// 更新
	_, err = tx.Exec(
		"UPDATE transactions SET account = ?, date = ?, item = ?, type = ?, amount = ?, memo = ? WHERE id = ?",
		req.Account, date, req.Item, req.Type, req.Amount, req.Memo, id,
	)
	if err != nil {
		return nil, fmt.Errorf("取引更新エラー: %w", err)
	}
	if err := insertPreparedTransactionImages(tx, id, preparedImages); err != nil {
		return nil, err
	}
	if oldAccount != req.Account && len(preparedImages) == 0 {
		if err := checkImageStorageQuota(tx, id, nil); err != nil {
			return nil, err
		}
	}

	// タグの更新: 既存のタグを削除して再挿入
	if _, err := tx.Exec("DELETE FROM transaction_tags WHERE transaction_id = ?", id); err != nil {
		return nil, fmt.Errorf("タグ紐付け削除エラー: %w", err)
	}
	if len(req.Tags) > 0 {
		for _, tagID := range req.Tags {
			if _, err := tx.Exec("INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", id, tagID); err != nil {
				return nil, fmt.Errorf("タグ紐付けエラー: %w", err)
			}
		}
	}

	// 関連口座の残高を再計算
	accounts := []string{req.Account}
	if oldAccount != req.Account {
		accounts = append(accounts, oldAccount)
	}
	for _, acc := range accounts {
		if err := recalculateBalanceIn(tx, acc); err != nil {
			return nil, fmt.Errorf("残高再計算エラー: %w", err)
		}
	}

	// 更新後のデータを取得
	var t models.Transaction
	var dateStr string
	err = tx.QueryRow(
		"SELECT id, account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&t.ID, &t.Account, &dateStr, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo)
	if err != nil {
		return nil, fmt.Errorf("更新後データ取得エラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	if err := pruneInvalidTransactionLinks(); err != nil {
		return nil, fmt.Errorf("紐付け整合性チェックエラー: %w", err)
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
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	var account string
	err = tx.QueryRow("SELECT account FROM transactions WHERE id = ?", id).Scan(&account)
	if err != nil {
		return fmt.Errorf("取引が見つかりません: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM transactions WHERE id = ?", id); err != nil {
		return fmt.Errorf("取引削除エラー: %w", err)
	}

	if err := recalculateBalanceIn(tx, account); err != nil {
		return fmt.Errorf("残高再計算エラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	database.AutoSnapshot()
	return nil
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
	return getStringSliceSetting("credit_card_items")
}

// SaveCreditCardSettings はクレジットカード設定を保存する
func SaveCreditCardSettings(items []string) error {
	if err := saveStringSliceSetting("credit_card_items", items); err != nil {
		return fmt.Errorf("クレジットカード設定保存エラー: %w", err)
	}
	if err := pruneInvalidTransactionLinks(); err != nil {
		return fmt.Errorf("紐付け整合性チェックエラー: %w", err)
	}
	database.AutoSnapshot()
	return nil
}

// GetBankAccountSettings はカード引き落とし元の銀行口座設定を取得する
func GetBankAccountSettings() ([]string, error) {
	return getStringSliceSetting("bank_account_items")
}

// SaveBankAccountSettings はカード引き落とし元の銀行口座設定を保存する
func SaveBankAccountSettings(items []string) error {
	if err := saveStringSliceSetting("bank_account_items", items); err != nil {
		return fmt.Errorf("銀行口座設定保存エラー: %w", err)
	}
	if err := pruneInvalidTransactionLinks(); err != nil {
		return fmt.Errorf("紐付け整合性チェックエラー: %w", err)
	}
	database.AutoSnapshot()
	return nil
}

func getStringSliceSetting(key string) ([]string, error) {
	db := database.GetDB()
	var value string
	err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		return []string{}, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return []string{}, nil
	}
	return items, nil
}

func saveStringSliceSetting(key string, items []string) error {
	db := database.GetDB()
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("JSONシリアライズエラー: %w", err)
	}
	_, err = db.Exec(
		"INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)",
		key,
		string(data),
	)
	return err
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
	if err := writer.Write([]string{"id", "account", "date", "item", "type", "amount", "balance", "memo", csvVersionHeader}); err != nil {
		return "", fmt.Errorf("CSVヘッダー書き出しエラー: %w", err)
	}

	for rows.Next() {
		var id, amount, balance int64
		var account, dateStr, item, txType, memo string
		if err := rows.Scan(&id, &account, &dateStr, &item, &txType, &amount, &balance, &memo); err != nil {
			return "", fmt.Errorf("バックアップスキャンエラー: %w", err)
		}
		if err := writer.Write([]string{
			fmt.Sprintf("%d", id),
			encodeCSVTextCell(account),
			encodeCSVTextCell(dateStr),
			encodeCSVTextCell(item),
			encodeCSVTextCell(txType),
			fmt.Sprintf("%d", amount),
			fmt.Sprintf("%d", balance),
			encodeCSVTextCell(memo),
			csvVersion2,
		}); err != nil {
			return "", fmt.Errorf("CSV行書き出しエラー: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("バックアップ行取得エラー: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("CSV書き出しエラー: %w", err)
	}

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

	// BOMを付与してExcel互換にする
	bom := "\xEF\xBB\xBF"
	filePath, err := writeUniquePrivateFile(downloadsDir, filename, []byte(bom+csvContent))
	if err != nil {
		return "", fmt.Errorf("CSVファイル書き出しエラー: %w", err)
	}

	return filePath, nil
}

// writeUniquePrivateFile は既存ファイルやsymlinkを上書きせず、所有者だけが
// 読み書きできる新規ファイルへ内容を保存する。
func writeUniquePrivateFile(dir, filename string, data []byte) (string, error) {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for attempt := 0; attempt < 100; attempt++ {
		candidateName := filename
		if attempt > 0 {
			candidateName = fmt.Sprintf("%s_%d%s", base, attempt, ext)
		}
		candidate := filepath.Join(dir, candidateName)
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}

		completed := false
		defer func() {
			_ = file.Close()
			if !completed {
				_ = os.Remove(candidate)
			}
		}()
		if _, err := file.Write(data); err != nil {
			return "", err
		}
		if err := file.Sync(); err != nil {
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return "", err
		}
		completed = true
		return candidate, nil
	}
	return "", fmt.Errorf("一意なバックアップファイル名を確保できませんでした")
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
	reader := csv.NewReader(strings.NewReader(content))
	headers, err := reader.Read()
	if err != nil {
		return 0, fmt.Errorf("CSVヘッダー読み取りエラー: %w", err)
	}
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
	}

	// ヘッダーのインデックスを特定
	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[strings.TrimSpace(h)] = i
	}
	versionIndex, versionedCSV := headerMap[csvVersionHeader]

	requiredHeaders := []string{"account", "date", "item", "type", "amount"}
	for _, h := range requiredHeaders {
		if _, ok := headerMap[h]; !ok {
			return 0, fmt.Errorf("必須ヘッダーが不足: %s", h)
		}
	}
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}

	// 永続化前に全行を検証し、後半行の不正でreplace済みデータを失わないようにする。
	type importRow struct {
		account string
		date    time.Time
		item    string
		txType  string
		amount  int64
		memo    string
	}
	var parsedRows []importRow
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("CSV行読み取りエラー (行%d): %w", len(parsedRows)+2, err)
		}

		rowNumber := len(parsedRows) + 2
		field := func(name string) (string, error) {
			idx := headerMap[name]
			if idx >= len(record) {
				return "", fmt.Errorf("%s列が不足しています (行%d)", name, rowNumber)
			}
			return record[idx], nil
		}

		if versionedCSV {
			if versionIndex >= len(record) {
				return 0, fmt.Errorf("CSVバージョン列が不足しています (行%d)", rowNumber)
			}
			if version := strings.TrimSpace(record[versionIndex]); version != csvVersion2 {
				return 0, fmt.Errorf("未対応のCSVバージョンです (行%d): %q", rowNumber, version)
			}
		}

		account, err := field("account")
		if err != nil {
			return 0, err
		}
		dateStr, err := field("date")
		if err != nil {
			return 0, err
		}
		item, err := field("item")
		if err != nil {
			return 0, err
		}
		txType, err := field("type")
		if err != nil {
			return 0, err
		}
		amountStr, err := field("amount")
		if err != nil {
			return 0, err
		}

		memo := ""
		if idx, ok := headerMap["memo"]; ok && idx < len(record) {
			memo = record[idx]
		}
		if versionedCSV {
			for name, value := range map[string]*string{
				"account": &account,
				"date":    &dateStr,
				"item":    &item,
				"type":    &txType,
				"memo":    &memo,
			} {
				decoded, decodeErr := decodeCSVTextCellV2(*value)
				if decodeErr != nil {
					return 0, fmt.Errorf("%s列のCSVエスケープが不正です (行%d): %w", name, rowNumber, decodeErr)
				}
				*value = decoded
			}
		} else {
			account = strings.TrimSpace(account)
			item = strings.TrimSpace(item)
			memo = strings.TrimSpace(memo)
		}
		dateStr = strings.TrimSpace(dateStr)
		txType = strings.ToLower(strings.TrimSpace(txType))
		amountStr = strings.TrimSpace(amountStr)

		if strings.TrimSpace(account) == "" {
			return 0, fmt.Errorf("口座名は必須です (行%d)", rowNumber)
		}
		if strings.TrimSpace(item) == "" {
			return 0, fmt.Errorf("項目は必須です (行%d)", rowNumber)
		}
		if txType != "income" && txType != "expense" {
			return 0, fmt.Errorf("種別はincomeまたはexpenseである必要があります (行%d)", rowNumber)
		}
		amount, err := strconv.ParseInt(amountStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("金額は正の整数である必要があります (行%d)", rowNumber)
		}
		if amount <= 0 {
			return 0, fmt.Errorf("金額は正の整数である必要があります (行%d)", rowNumber)
		}
		if err := validation.ValidateTransactionAmount(amount); err != nil {
			return 0, fmt.Errorf("金額が不正です (行%d): %w", rowNumber, err)
		}

		date, err := parseDateStrict(dateStr)
		if err != nil {
			return 0, fmt.Errorf("日付形式が正しくありません (行%d): %w", rowNumber, err)
		}

		parsedRows = append(parsedRows, importRow{account: account, date: date, item: item, txType: txType, amount: amount, memo: memo})
	}

	db := database.GetDB()
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

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
	affectedAccounts := make(map[string]struct{})
	for index, row := range parsedRows {
		if _, err := stmt.Exec(row.account, row.date, row.item, row.txType, row.amount, row.memo); err != nil {
			_ = stmt.Close()
			return 0, fmt.Errorf("CSVインポートエラー (行%d): %w", index+2, err)
		}
		affectedAccounts[row.account] = struct{}{}
	}
	if err := stmt.Close(); err != nil {
		return 0, fmt.Errorf("CSVステートメントクローズエラー: %w", err)
	}

	// INSERTと残高再計算を同じSQLトランザクションで完了させる。
	for account := range affectedAccounts {
		if err := recalculateBalanceIn(tx, account); err != nil {
			return 0, fmt.Errorf("残高再計算エラー (%s): %w", account, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("インポートコミットエラー: %w", err)
	}

	database.AutoSnapshot()
	return len(parsedRows), nil
}

// --- ヘルパー関数 ---

type sqlExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Prepare(query string) (*sql.Stmt, error)
}

// recalculateBalanceIn は指定されたDBまたはトランザクション内で口座残高を再計算する。
func recalculateBalanceIn(q sqlExecutor, account string) error {
	// 時系列順で取引データを取得
	rows, err := q.Query(
		"SELECT id, type, amount FROM transactions WHERE account = ? ORDER BY date, id",
		account,
	)
	if err != nil {
		return fmt.Errorf("残高再計算クエリエラー: %w", err)
	}
	defer rows.Close()

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
			runningBalance, err = validation.CheckedAddInt64(runningBalance, amount)
		} else {
			runningBalance, err = validation.CheckedSubInt64(runningBalance, amount)
		}
		if err != nil {
			return fmt.Errorf("残高計算オーバーフロー (id=%d): %w", id, err)
		}
		updates = append(updates, balanceUpdate{id: id, balance: runningBalance})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("残高再計算行取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("残高再計算行クローズエラー: %w", err)
	}

	if len(updates) == 0 {
		return nil
	}

	stmt, err := q.Prepare("UPDATE transactions SET balance = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.Exec(u.balance, u.id); err != nil {
			return fmt.Errorf("残高更新エラー (id=%d): %w", u.id, err)
		}
	}

	return nil
}

// recalculateBalance は口座内の全取引を自前のSQLトランザクションで再計算する。
func recalculateBalance(account string) error {
	db := database.GetDB()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	if err := recalculateBalanceIn(tx, account); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	return nil
}

// parseDate は複数の受け入れ可能なフォーマットを許容し、
// どれにも一致しない場合は現在時刻を返す。
func parseDate(dateStr string) time.Time {
	t, err := parseDateStrict(dateStr)
	if err != nil {
		return time.Now()
	}
	return t
}

func parseDateStrict(dateStr string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("対応していない日付形式です: %q", dateStr)
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
	if err := validation.ValidateTransactionAmount(req.Amount); err != nil {
		return err
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

// AddTransactionImage は取引に画像を追加する
func AddTransactionImage(transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	db := database.GetDB()
	prepared, err := prepareTransactionImages([]models.TransactionImageRequest{img})
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	if err := checkImageStorageQuota(tx, transactionID, prepared); err != nil {
		return nil, err
	}
	result, err := insertPreparedTransactionImage(tx, transactionID, prepared[0])
	if err != nil {
		return nil, fmt.Errorf("画像保存エラー: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("画像ID取得エラー: %w", err)
	}

	var createdAt string
	err = tx.QueryRow("SELECT created_at FROM transaction_images WHERE id = ?", id).Scan(&createdAt)
	if err != nil {
		return nil, fmt.Errorf("画像保存結果取得エラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("画像保存コミットエラー: %w", err)
	}

	resp := &models.TransactionImageResponse{
		ID:        id,
		Filename:  prepared[0].filename,
		MimeType:  prepared[0].mimeType,
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
		response := models.TransactionImageResponse{
			ID:        id,
			Filename:  filename,
			MimeType:  mimeType,
			CreatedAt: createdAt,
		}
		prepared, validationErr := prepareDecodedTransactionImage(filename, mimeType, data)
		if validationErr != nil {
			// 旧バージョンで保存された不正BLOBをブラウザへ返さない。
			response.Invalid = true
		} else {
			response.MimeType = prepared.mimeType
			response.DataURL = fmt.Sprintf("data:%s;base64,%s", prepared.mimeType, base64.StdEncoding.EncodeToString(prepared.data))
		}
		images = append(images, response)
	}
	if images == nil {
		images = []models.TransactionImageResponse{}
	}
	return images, nil
}

// DeleteTransactionImage はWails互換用に画像IDを指定して削除する。
func DeleteTransactionImage(imageID int64) error {
	return deleteTransactionImage("id = ?", imageID)
}

// DeleteTransactionImageForTransaction はURL上の取引IDと画像の所属を照合して削除する。
func DeleteTransactionImageForTransaction(transactionID, imageID int64) error {
	return deleteTransactionImage("transaction_id = ? AND id = ?", transactionID, imageID)
}

func deleteTransactionImage(where string, args ...interface{}) error {
	db := database.GetDB()
	result, err := db.Exec("DELETE FROM transaction_images WHERE "+where, args...)
	if err != nil {
		return fmt.Errorf("画像削除エラー: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("画像削除結果確認エラー: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("画像が見つかりません")
	}
	database.AutoSnapshot()
	return nil
}

func insertPreparedTransactionImages(db sqlExecutor, transactionID int64, images []preparedTransactionImage) error {
	if len(images) == 0 {
		return nil
	}
	if err := checkImageStorageQuota(db, transactionID, images); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := insertPreparedTransactionImage(db, transactionID, image); err != nil {
			return fmt.Errorf("画像保存エラー: %w", err)
		}
	}
	return nil
}

func insertPreparedTransactionImage(db sqlExecutor, transactionID int64, image preparedTransactionImage) (sql.Result, error) {
	return db.Exec(
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, ?, ?, ?)",
		transactionID, image.filename, image.data, image.mimeType,
	)
}

func checkImageStorageQuota(db sqlExecutor, transactionID int64, images []preparedTransactionImage) error {
	var account string
	if err := db.QueryRow("SELECT account FROM transactions WHERE id = ?", transactionID).Scan(&account); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("取引が見つかりません")
		}
		return fmt.Errorf("画像の取引確認エラー: %w", err)
	}

	var additionalBytes int64
	for _, image := range images {
		additionalBytes += int64(len(image.data))
	}

	var transactionCount, transactionBytes int64
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(length(data)), 0) FROM transaction_images WHERE transaction_id = ?",
		transactionID,
	).Scan(&transactionCount, &transactionBytes); err != nil {
		return fmt.Errorf("取引画像使用量確認エラー: %w", err)
	}
	if transactionCount+int64(len(images)) > int64(models.MaxImagesPerTransaction) {
		return fmt.Errorf("画像は1取引につき%d件までです", models.MaxImagesPerTransaction)
	}
	if transactionBytes+additionalBytes > models.MaxImageBytesPerTransaction {
		return fmt.Errorf("画像データの合計は1取引につき%d MiBまでです", models.MaxImageBytesPerTransaction/(1024*1024))
	}

	var accountBytes int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(length(ti.data)), 0)
		FROM transaction_images ti
		JOIN transactions t ON t.id = ti.transaction_id
		WHERE t.account = ?`, account,
	).Scan(&accountBytes); err != nil {
		return fmt.Errorf("口座画像使用量確認エラー: %w", err)
	}
	if accountBytes+additionalBytes > models.MaxImageBytesPerAccount {
		return fmt.Errorf("口座「%s」の画像保存量は%d MiBまでです", account, models.MaxImageBytesPerAccount/(1024*1024))
	}

	var databaseBytes int64
	if err := db.QueryRow("SELECT COALESCE(SUM(length(data)), 0) FROM transaction_images").Scan(&databaseBytes); err != nil {
		return fmt.Errorf("画像DB使用量確認エラー: %w", err)
	}
	if databaseBytes+additionalBytes > models.MaxImageBytesDatabase {
		return fmt.Errorf("DB全体の画像保存量は%d MiBまでです", models.MaxImageBytesDatabase/(1024*1024))
	}
	return nil
}

// GetImageStorageUsage は現在の画像保存量と上限を返す。
func GetImageStorageUsage() (*models.ImageStorageUsage, error) {
	db := database.GetDB()
	usage := &models.ImageStorageUsage{
		MaxImageBytes:           models.MaxImageBytes,
		MaxImagePixels:          models.MaxImagePixels,
		MaxImagesPerTransaction: models.MaxImagesPerTransaction,
		MaxBytesPerTransaction:  models.MaxImageBytesPerTransaction,
		MaxBytesPerAccount:      models.MaxImageBytesPerAccount,
		MaxBytesDatabase:        models.MaxImageBytesDatabase,
		Accounts:                []models.AccountImageStorageUsage{},
	}
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(length(data)), 0) FROM transaction_images",
	).Scan(&usage.ImageCount, &usage.Bytes); err != nil {
		return nil, fmt.Errorf("画像使用量取得エラー: %w", err)
	}

	rows, err := db.Query(`
		SELECT t.account, COUNT(*), COALESCE(SUM(length(ti.data)), 0)
		FROM transaction_images ti
		JOIN transactions t ON t.id = ti.transaction_id
		GROUP BY t.account
		ORDER BY t.account`)
	if err != nil {
		return nil, fmt.Errorf("口座別画像使用量取得エラー: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var account models.AccountImageStorageUsage
		if err := rows.Scan(&account.Account, &account.ImageCount, &account.Bytes); err != nil {
			return nil, fmt.Errorf("口座別画像使用量スキャンエラー: %w", err)
		}
		usage.Accounts = append(usage.Accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("口座別画像使用量取得エラー: %w", err)
	}
	return usage, nil
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
// フィルタ条件はLEFT JOINのON句に配置し、全タグを保持した上で
// 子タグの金額を親タグに集約する。
func GetTagSummary(txType string, startDate, endDate string) ([]models.TagSummary, error) {
	return getTagSummaryFiltered(txType, startDate, endDate, "", nil)
}

// getTagSummaryFiltered はAI分析を含む呼び出し元の全フィルターを適用し、
// 条件に一致した取引群についてタグ別集計を返す。
func getTagSummaryFiltered(txType, startDate, endDate, account string, tagIDs []int64) ([]models.TagSummary, error) {
	db := database.GetDB()

	// フィルタ条件をON句に含めてLEFT JOINを維持する
	// WHERE句に入れるとLEFT JOINがINNER JOIN相当になり、
	// 子タグにのみ紐付いた取引の親タグが結果から除外されてしまう
	joinConditions := []string{"tt.transaction_id = tr.id"}
	args := []interface{}{}

	if txType != "" {
		joinConditions = append(joinConditions, "tr.type = ?")
		args = append(args, txType)
	}
	if startDate != "" {
		joinConditions = append(joinConditions, "tr.date >= ?")
		args = append(args, startDate)
	}
	if endDate != "" {
		joinConditions = append(joinConditions, "tr.date <= ?")
		args = append(args, endDate+" 23:59:59")
	}
	if account != "" {
		joinConditions = append(joinConditions, "tr.account = ?")
		args = append(args, account)
	}
	if len(tagIDs) > 0 {
		placeholders := make([]string, len(tagIDs))
		for i, id := range tagIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		joinConditions = append(joinConditions, fmt.Sprintf(
			"tr.id IN (SELECT transaction_id FROM transaction_tags WHERE tag_id IN (%s))",
			strings.Join(placeholders, ","),
		))
	}

	query := `SELECT t.id, t.name, t.level, t.parent_id,
		COALESCE(SUM(tr.amount), 0) as total_amount,
		COUNT(tr.id) as tx_count
		FROM tags t
		LEFT JOIN transaction_tags tt ON t.id = tt.tag_id
		LEFT JOIN transactions tr ON ` + strings.Join(joinConditions, " AND ") + `
		GROUP BY t.id
		ORDER BY total_amount DESC`

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

	for rows.Next() {
		var td tagData
		if err := rows.Scan(&td.id, &td.name, &td.level, &td.parentID, &td.amount, &td.count); err != nil {
			return nil, err
		}
		allData = append(allData, td)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("タグ集計行取得エラー: %w", err)
	}

	// ツリー構造を構築し、子タグの金額を親タグに集約する
	var buildSummary func(parentID *int64) ([]models.TagSummary, error)
	buildSummary = func(parentID *int64) ([]models.TagSummary, error) {
		var summaries []models.TagSummary
		for _, td := range allData {
			match := false
			if parentID == nil && !td.parentID.Valid {
				match = true
			} else if parentID != nil && td.parentID.Valid && td.parentID.Int64 == *parentID {
				match = true
			}
			if match {
				children, err := buildSummary(&td.id)
				if err != nil {
					return nil, err
				}
				// 子タグの金額・件数を親に集約
				amount := td.amount
				count := td.count
				for _, child := range children {
					amount, err = validation.CheckedAddInt64(amount, child.Amount)
					if err != nil {
						return nil, fmt.Errorf("タグ金額集計オーバーフロー (tag_id=%d): %w", td.id, err)
					}
					count += child.Count
				}
				s := models.TagSummary{
					TagID:    td.id,
					TagName:  td.name,
					Amount:   amount,
					Count:    count,
					Children: children,
				}
				summaries = append(summaries, s)
			}
		}
		// 金額が0のタグを除外
		var filtered []models.TagSummary
		for _, s := range summaries {
			if s.Amount > 0 {
				filtered = append(filtered, s)
			}
		}
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Amount > filtered[j].Amount
		})
		return filtered, nil
	}

	result, err := buildSummary(nil)
	if err != nil {
		return nil, err
	}

	// トップレベルの合計金額からratioを算出
	var totalAmount int64
	for _, s := range result {
		totalAmount, err = validation.CheckedAddInt64(totalAmount, s.Amount)
		if err != nil {
			return nil, fmt.Errorf("タグ合計オーバーフロー: %w", err)
		}
	}
	var setRatios func([]models.TagSummary)
	setRatios = func(summaries []models.TagSummary) {
		for i := range summaries {
			if totalAmount > 0 {
				summaries[i].Ratio = float64(summaries[i].Amount) / float64(totalAmount)
			}
			setRatios(summaries[i].Children)
		}
	}
	setRatios(result)

	if result == nil {
		result = []models.TagSummary{}
	}
	return result, nil
}

// --- AI分析 (Agent.md §6.3) ---

const (
	defaultAIAnalysisLimit = 100
	maxAIAnalysisLimit     = 500
	maxAIAnalysisTagIDs    = 20
)

type aiAnalysisCursor struct {
	Date string `json:"date"`
	ID   int64  `json:"id"`
}

// AnalyzeTransactions はAIエージェント向けの取引分析を行う
func AnalyzeTransactions(req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	db := database.GetDB()
	where, args, err := buildAIAnalysisFilter(req)
	if err != nil {
		return nil, err
	}

	resp := &models.AnalysisResponse{}
	aggregateQuery := `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN type = 'income' THEN amount ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN type = 'expense' THEN amount ELSE 0 END), 0)
		FROM transactions` + where
	if err := db.QueryRow(aggregateQuery, args...).Scan(&resp.Count, &resp.TotalIncome, &resp.TotalExpense); err != nil {
		return nil, fmt.Errorf("分析集計エラー: %w", err)
	}
	resp.NetAmount, err = validation.CheckedSubInt64(resp.TotalIncome, resp.TotalExpense)
	if err != nil {
		return nil, fmt.Errorf("分析純額オーバーフロー: %w", err)
	}

	// タグ別集計にも取引一覧と同じフィルターを適用する。
	tagSummaries, err := getTagSummaryFiltered(req.Type, req.StartDate, req.EndDate, req.Account, req.TagIDs)
	if err != nil {
		return nil, fmt.Errorf("タグ別分析エラー: %w", err)
	}
	if req.MaxTagSummaries > 0 {
		var total int
		countTagSummaries(tagSummaries, &total)
		if total > req.MaxTagSummaries {
			remaining := req.MaxTagSummaries
			tagSummaries = truncateTagSummaries(tagSummaries, &remaining)
			resp.TagSummariesTruncated = true
		}
	}
	resp.TagSummaries = tagSummaries

	if !req.IncludeTransactions {
		return resp, nil
	}

	limit := req.Limit
	if limit == 0 {
		limit = defaultAIAnalysisLimit
	}
	if limit < 1 || limit > maxAIAnalysisLimit {
		return nil, fmt.Errorf("分析明細のlimitは1から%dまでです", maxAIAnalysisLimit)
	}

	detailWhere := where
	detailArgs := append([]interface{}{}, args...)
	if req.Cursor != "" {
		cursor, err := decodeAIAnalysisCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		detailWhere += " AND (datetime(date) < ? OR (datetime(date) = ? AND id < ?))"
		detailArgs = append(detailArgs, cursor.Date, cursor.Date, cursor.ID)
	}
	detailArgs = append(detailArgs, limit+1)
	rows, err := db.Query(`SELECT id, account, datetime(date), item, type, amount, memo
		FROM transactions`+detailWhere+`
		ORDER BY datetime(date) DESC, id DESC
		LIMIT ?`, detailArgs...)
	if err != nil {
		return nil, fmt.Errorf("分析明細クエリエラー: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var detail models.AITransactionDetail
		var memo string
		if err := rows.Scan(&detail.ID, &detail.Account, &detail.Date, &detail.Item, &detail.Type, &detail.Amount, &memo); err != nil {
			return nil, fmt.Errorf("分析明細スキャンエラー: %w", err)
		}
		if req.IncludeMemo {
			detail.Memo = memo
		}
		resp.Transactions = append(resp.Transactions, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("分析明細取得エラー: %w", err)
	}
	if len(resp.Transactions) > limit {
		lastReturned := resp.Transactions[limit-1]
		cursor, err := encodeAIAnalysisCursor(lastReturned.Date, lastReturned.ID)
		if err != nil {
			return nil, err
		}
		resp.NextCursor = cursor
		resp.Transactions = resp.Transactions[:limit]
	}
	resp.ReturnedCount = len(resp.Transactions)

	return resp, nil
}

func countTagSummaries(summaries []models.TagSummary, total *int) {
	for i := range summaries {
		(*total)++
		countTagSummaries(summaries[i].Children, total)
	}
}

func truncateTagSummaries(summaries []models.TagSummary, remaining *int) []models.TagSummary {
	if *remaining <= 0 {
		return nil
	}
	result := make([]models.TagSummary, 0, len(summaries))
	for _, summary := range summaries {
		if *remaining <= 0 {
			break
		}
		(*remaining)--
		summary.Children = truncateTagSummaries(summary.Children, remaining)
		result = append(result, summary)
	}
	return result
}

func buildAIAnalysisFilter(req models.AnalysisRequest) (string, []interface{}, error) {
	where := " WHERE 1=1"
	args := []interface{}{}
	if req.Account != "" {
		where += " AND account = ?"
		args = append(args, req.Account)
	}
	if req.Type != "" {
		where += " AND type = ?"
		args = append(args, req.Type)
	}
	if req.StartDate != "" {
		where += " AND date >= ?"
		args = append(args, req.StartDate)
	}
	if req.EndDate != "" {
		where += " AND date <= ?"
		args = append(args, req.EndDate+" 23:59:59")
	}
	if len(req.TagIDs) > maxAIAnalysisTagIDs {
		return "", nil, fmt.Errorf("タグIDは%d件までです", maxAIAnalysisTagIDs)
	}
	if len(req.TagIDs) > 0 {
		placeholders := make([]string, len(req.TagIDs))
		for i, id := range req.TagIDs {
			if id <= 0 {
				return "", nil, fmt.Errorf("タグIDは正の整数で指定してください")
			}
			placeholders[i] = "?"
			args = append(args, id)
		}
		where += fmt.Sprintf(" AND id IN (SELECT transaction_id FROM transaction_tags WHERE tag_id IN (%s))", strings.Join(placeholders, ","))
	}
	return where, args, nil
}

func encodeAIAnalysisCursor(date string, id int64) (string, error) {
	payload, err := json.Marshal(aiAnalysisCursor{Date: date, ID: id})
	if err != nil {
		return "", fmt.Errorf("分析カーソル作成エラー: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeAIAnalysisCursor(raw string) (aiAnalysisCursor, error) {
	if len(raw) > 512 {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	var cursor aiAnalysisCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	if cursor.ID <= 0 {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	if _, err := time.Parse("2006-01-02 15:04:05", cursor.Date); err != nil {
		return aiAnalysisCursor{}, fmt.Errorf("分析カーソルが無効です")
	}
	return cursor, nil
}

// --- 取引紐付け（リンク）機能 (Agent.md §6.2) ---

// GetTransactionLinks は指定した取引に紐付いた取引の一覧を返す（親・子の双方向）
func GetTransactionLinks(transactionID int64) ([]models.LinkedTransactionResponse, error) {
	db := database.GetDB()
	query := `
		SELECT t.id, t.account, t.date, t.item, t.type, t.amount, t.memo
		FROM transactions t
		WHERE t.id IN (
			SELECT child_id FROM transaction_links WHERE parent_id = ?
			UNION
			SELECT parent_id FROM transaction_links WHERE child_id = ?
		)
		ORDER BY t.date DESC, t.id DESC
	`
	rows, err := db.Query(query, transactionID, transactionID)
	if err != nil {
		return nil, fmt.Errorf("紐付け取引取得エラー: %w", err)
	}
	defer rows.Close()

	var results []models.LinkedTransactionResponse
	for rows.Next() {
		var r models.LinkedTransactionResponse
		var dateTime time.Time
		if err := rows.Scan(&r.ID, &r.FundItem, &dateTime, &r.Item, &r.Type, &r.Amount, &r.Memo); err != nil {
			return nil, fmt.Errorf("紐付け取引スキャンエラー: %w", err)
		}
		if dateTime.Hour() == 0 && dateTime.Minute() == 0 && dateTime.Second() == 0 {
			r.Date = dateTime.Format("2006-01-02")
		} else {
			r.Date = dateTime.Format("2006-01-02 15:04:05")
		}
		results = append(results, r)
	}
	if results == nil {
		results = []models.LinkedTransactionResponse{}
	}
	return results, nil
}

// AddTransactionLink は取引同士を紐付ける
func AddTransactionLink(parentID, childID int64) error {
	if parentID == childID {
		return fmt.Errorf("同一の取引同士は紐付けできません")
	}
	db := database.GetDB()
	if err := validateCardWithdrawalLink(parentID, childID); err != nil {
		return err
	}
	// 正規化: 小さいIDをparent_id、大きいIDをchild_idにする（重複防止）
	p, c := parentID, childID
	if p > c {
		p, c = c, p
	}
	_, err := db.Exec("INSERT OR IGNORE INTO transaction_links (parent_id, child_id) VALUES (?, ?)", p, c)
	if err != nil {
		return fmt.Errorf("紐付け追加エラー: %w", err)
	}
	database.AutoSnapshot()
	return nil
}

func validateCardWithdrawalLink(transactionID, linkedID int64) error {
	db := database.GetDB()
	accounts := make(map[int64]string, 2)
	rows, err := db.Query("SELECT id, account FROM transactions WHERE id IN (?, ?)", transactionID, linkedID)
	if err != nil {
		return fmt.Errorf("紐付け対象取得エラー: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var account string
		if err := rows.Scan(&id, &account); err != nil {
			return fmt.Errorf("紐付け対象スキャンエラー: %w", err)
		}
		accounts[id] = account
	}
	if len(accounts) != 2 {
		return fmt.Errorf("紐付け対象の取引が見つかりません")
	}

	accountA := strings.TrimSpace(accounts[transactionID])
	accountB := strings.TrimSpace(accounts[linkedID])
	if isCardWithdrawalLinkAccounts(accountA, accountB) {
		return nil
	}
	return fmt.Errorf("紐付けはクレジットカード項目と銀行口座項目の取引間でのみ追加できます")
}

func pruneInvalidTransactionLinks() error {
	db := database.GetDB()
	rows, err := db.Query(`
		SELECT l.parent_id, p.account, l.child_id, c.account
		FROM transaction_links l
		JOIN transactions p ON p.id = l.parent_id
		JOIN transactions c ON c.id = l.child_id
	`)
	if err != nil {
		return fmt.Errorf("紐付け取得エラー: %w", err)
	}
	defer rows.Close()

	var invalidPairs [][2]int64
	for rows.Next() {
		var parentID, childID int64
		var parentAccount, childAccount string
		if err := rows.Scan(&parentID, &parentAccount, &childID, &childAccount); err != nil {
			return fmt.Errorf("紐付けスキャンエラー: %w", err)
		}
		if !isCardWithdrawalLinkAccounts(parentAccount, childAccount) {
			invalidPairs = append(invalidPairs, [2]int64{parentID, childID})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("紐付け行取得エラー: %w", err)
	}

	for _, pair := range invalidPairs {
		if _, err := db.Exec("DELETE FROM transaction_links WHERE parent_id = ? AND child_id = ?", pair[0], pair[1]); err != nil {
			return fmt.Errorf("不正な紐付け削除エラー: %w", err)
		}
	}
	return nil
}

func isCardWithdrawalLinkAccounts(accountA, accountB string) bool {
	creditCardItems, _ := GetCreditCardSettings()
	bankAccountItems, _ := GetBankAccountSettings()
	creditCards := stringSet(creditCardItems)
	bankAccounts := stringSet(bankAccountItems)

	accountA = strings.TrimSpace(accountA)
	accountB = strings.TrimSpace(accountB)
	return (creditCards[accountA] && bankAccounts[accountB]) || (bankAccounts[accountA] && creditCards[accountB])
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			set[item] = true
		}
	}
	return set
}

// RemoveTransactionLink は取引の紐付けを解除する
func RemoveTransactionLink(transactionID, linkedID int64) error {
	db := database.GetDB()
	// 正規化された方向で削除を試行（両方向チェック）
	result, err := db.Exec(
		"DELETE FROM transaction_links WHERE (parent_id = ? AND child_id = ?) OR (parent_id = ? AND child_id = ?)",
		transactionID, linkedID, linkedID, transactionID,
	)
	if err != nil {
		return fmt.Errorf("紐付け削除エラー: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("指定された紐付けは存在しません")
	}
	database.AutoSnapshot()
	return nil
}
