// Package core はアプリケーションの主要な論理処理（ビジネスロジック）を提供する
package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/fileprivacy"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

const (
	csvVersionHeader = "omni_money_csv_version"
	csvVersion1      = "1"
	csvVersion2      = "2"
	csvVersion3      = "3"
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
func (s *Service) GetAccounts() ([]string, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
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
func (s *Service) GetItems(account string) ([]string, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}

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
func (s *Service) GetTransactions(account string, search string) ([]models.TransactionResponse, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}

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
	query := "SELECT id, account, date, item, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), balance, memo FROM transactions" + whereClause
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
	// #nosec G202 -- whereClause is assembled only from fixed SQL fragments above; all user values remain bound placeholders.
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
func (s *Service) AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	return s.AddTransactionContext(context.Background(), req)
}

func (s *Service) AddTransactionContext(ctx context.Context, req models.TransactionRequest) (*models.TransactionResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	prepared, err := prepareTransactionInsertContext(ctx, req)
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
	resp.Tags, _ = s.GetTransactionTags(resp.ID)
	s.autoSnapshot()
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
	return prepareTransactionInsertContext(context.Background(), req)
}

func prepareTransactionInsertContext(ctx context.Context, req models.TransactionRequest) (preparedTransactionInsert, error) {
	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return preparedTransactionInsert{}, err
	}
	if err := validateTransactionData(req); err != nil {
		return preparedTransactionInsert{}, err
	}
	preparedImages, err := prepareTransactionImagesContext(ctx, req.Images)
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
	if err := validateTagIDsIn(tx, req.Tags); err != nil {
		return nil, err
	}
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
		"SELECT id, account, date, item, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&inserted.ID, &inserted.Account, &dateStr, &inserted.Item, &inserted.Type, &inserted.Amount, &inserted.Balance, &inserted.Memo); err != nil {
		return nil, fmt.Errorf("追加後データ取得エラー: %w", err)
	}
	inserted.Date = parseDate(dateStr)
	response := inserted.ToResponse()
	return &response, nil
}

// UpdateTransaction は既存の取引を更新する
func (s *Service) UpdateTransaction(id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	return s.UpdateTransactionContext(context.Background(), id, req)
}

func (s *Service) UpdateTransactionContext(ctx context.Context, id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	date, err := parseTransactionDate(req.Date, req.Time)
	if err != nil {
		return nil, err
	}

	if err := validateTransactionData(req); err != nil {
		return nil, err
	}
	preparedImages, err := prepareTransactionImagesContext(ctx, req.Images)
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()
	if err := validateTagIDsIn(tx, req.Tags); err != nil {
		return nil, err
	}

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
	// A user edit is an ordinary current write. It deliberately replaces any
	// archive-only amount provenance instead of leaving a hidden historical
	// value that would override the validated request.
	if _, err := tx.Exec("DELETE FROM transaction_archive_amounts WHERE transaction_id = ?", id); err != nil {
		return nil, fmt.Errorf("archive金額解除エラー: %w", err)
	}
	if err := insertPreparedTransactionImages(tx, id, preparedImages); err != nil {
		return nil, err
	}
	if oldAccount != req.Account && len(preparedImages) == 0 {
		if err := checkImageAccountMoveQuota(tx, id, oldAccount, req.Account); err != nil {
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
		"SELECT id, account, date, item, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), balance, memo FROM transactions WHERE id = ?", id,
	).Scan(&t.ID, &t.Account, &dateStr, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo)
	if err != nil {
		return nil, fmt.Errorf("更新後データ取得エラー: %w", err)
	}
	settings, err := loadLedgerSettingsIn(tx)
	if err != nil {
		return nil, fmt.Errorf("設定取得エラー: %w", err)
	}
	if err := pruneInvalidTransactionLinksIn(tx, settings); err != nil {
		return nil, fmt.Errorf("紐付け整合性チェックエラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("トランザクションコミットエラー: %w", err)
	}
	t.Date = parseDate(dateStr)
	resp := t.ToResponse()
	resp.Tags, _ = s.GetTransactionTags(int64(t.ID))
	s.autoSnapshot()
	return &resp, nil
}

// validateTagIDsIn checks every requested reference before a transaction
// mutation. This prevents INSERT OR IGNORE from silently dropping a bad tag
// and makes Add/Update transaction writes agree with AddTransactionTags.
func validateTagIDsIn(q interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, tagIDs []int64) error {
	seen := make(map[int64]struct{}, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			return fmt.Errorf("無効なタグIDです: %d", tagID)
		}
		if _, ok := seen[tagID]; ok {
			continue
		}
		seen[tagID] = struct{}{}
		var exists int
		if err := q.QueryRow("SELECT 1 FROM tags WHERE id = ?", tagID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("タグが見つかりません: %d", tagID)
			}
			return fmt.Errorf("タグ存在確認エラー: %w", err)
		}
	}
	return nil
}

// DeleteTransaction は取引を削除する
func (s *Service) DeleteTransaction(id int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
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
	s.autoSnapshot()
	return nil
}

// GetBalanceHistory は残高推移データを返す
func (s *Service) GetBalanceHistory() (*models.BalanceHistoryResponse, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
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
func (s *Service) GetBalanceHistoryFiltered(fundItems []string) (*models.BalanceHistoryResponse, error) {
	if len(fundItems) == 0 {
		return &models.BalanceHistoryResponse{
			Accounts:      []string{},
			Dates:         []string{},
			Balances:      map[string][]int64{},
			BalancesExact: map[string][]string{},
		}, nil
	}

	db, err := s.database()
	if err != nil {
		return nil, err
	}

	// クレジットカード設定を取得
	creditCardItems, _ := s.GetCreditCardSettings()
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

	// #nosec G201 -- only generated "?" placeholders are interpolated; every
	// account value remains a bound query argument.
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
func (s *Service) GetCreditCardSettings() ([]string, error) {
	return s.getStringSliceSetting("credit_card_items")
}

// SaveCreditCardSettings はクレジットカード設定を保存する
func (s *Service) SaveCreditCardSettings(items []string) error {
	if err := s.saveStringSliceSetting("credit_card_items", items); err != nil {
		return fmt.Errorf("クレジットカード設定保存エラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

// GetBankAccountSettings はカード引き落とし元の銀行口座設定を取得する
func (s *Service) GetBankAccountSettings() ([]string, error) {
	return s.getStringSliceSetting("bank_account_items")
}

// SaveBankAccountSettings はカード引き落とし元の銀行口座設定を保存する
func (s *Service) SaveBankAccountSettings(items []string) error {
	if err := s.saveStringSliceSetting("bank_account_items", items); err != nil {
		return fmt.Errorf("銀行口座設定保存エラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

func (s *Service) getStringSliceSetting(key string) ([]string, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	var value string
	err = db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	// Reads must understand both the current canonical form and values written
	// by the pre-v3 API. The archive mode is deliberately lossless (including
	// duplicate/empty entries) so restoring an old ledger cannot silently
	// delete its transaction links.
	parsed, parseErr := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingArchive, maxCSVSettingValueBytes, validation.MaxSettingItems)
	if parseErr != nil {
		// A malformed historical setting is not trusted for link validation; the
		// normal write path rejects it and callers treat it as an empty set.
		return []string{}, nil
	}
	return parsed.Items, nil
}

func (s *Service) saveStringSliceSetting(key string, items []string) error {
	if key != "credit_card_items" && key != "bank_account_items" {
		return fmt.Errorf("未対応のledger設定キーです")
	}
	if err := validation.ValidateLedgerSettingItems(items); err != nil {
		return err
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	data, err := validation.MarshalLedgerSettingItems(items)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("設定transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, data); err != nil {
		return err
	}
	settings, err := loadLedgerSettingsIn(tx)
	if err != nil {
		return fmt.Errorf("設定取得エラー: %w", err)
	}
	if err := pruneInvalidTransactionLinksIn(tx, settings); err != nil {
		return fmt.Errorf("紐付け整合性チェックエラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("設定transactionコミットエラー: %w", err)
	}
	return nil
}

// BackupToCSV exports the complete ledger in the normalized v3 format.
// The default backup contract must always be able to restore the complete
// ledger, including extension data, because legacy/v2 replace is intentionally
// rejected.  Call BackupToCSVV2 only for an explicitly requested, append-only
// compatibility export for old clients.
func (s *Service) BackupToCSV() (string, error) {
	return s.BackupToCSVContext(context.Background())
}

// BackupToCSVContext takes one read transaction for every export query. This
// gives callers a coherent snapshot even while another request adds or removes
// transactions, images, tags, or links.
func (s *Service) BackupToCSVContext(ctx context.Context) (string, error) {
	var output strings.Builder
	boundedOutput := &csvLimitedStringWriter{dst: &output, limit: MaxCSVStringImportBytes}
	if err := s.backupToCSVContextWriter(ctx, boundedOutput); err != nil {
		return "", err
	}
	return output.String(), nil
}

// BackupToCSVStreamContext is the bounded-memory export path used by HTTP and
// file exports. The writer receives CSV bytes directly; no archive-sized Go
// string is constructed.
func (s *Service) BackupToCSVStreamContext(ctx context.Context, output io.Writer) error {
	if output == nil {
		return fmt.Errorf("CSV出力先がありません")
	}
	return s.backupToCSVContextWriter(ctx, output)
}

func (s *Service) backupToCSVContextWriter(ctx context.Context, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var release func()
	if !HasCSVOperationReservation(ctx) {
		var ok bool
		release, ok = TryAcquireCSVOperationSlot()
		if !ok {
			return fmt.Errorf("CSV入出力が混雑しています。しばらくしてから再試行してください")
		}
		defer release()
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("CSV read transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	_, err = backupToCSVV3In(ctx, tx, output)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("CSV read transactionコミットエラー: %w", err)
	}
	return nil
}

// BackupToCSVFull always emits the normalized v3 format, including empty
// extension sections.  It is useful to callers that want a stable schema even
// before the first image/tag/link/setting is created.
func (s *Service) BackupToCSVFull() (string, error) {
	return s.BackupToCSVFullContext(context.Background())
}

func (s *Service) BackupToCSVFullContext(ctx context.Context) (string, error) {
	var output strings.Builder
	boundedOutput := &csvLimitedStringWriter{dst: &output, limit: MaxCSVStringImportBytes}
	if err := s.backupToCSVFullStreamContext(ctx, boundedOutput); err != nil {
		return "", err
	}
	return output.String(), nil
}

func (s *Service) BackupToCSVFullStreamContext(ctx context.Context, output io.Writer) error {
	return s.backupToCSVFullStreamContext(ctx, output)
}

func (s *Service) backupToCSVFullStreamContext(ctx context.Context, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var release func()
	if !HasCSVOperationReservation(ctx) {
		var ok bool
		release, ok = TryAcquireCSVOperationSlot()
		if !ok {
			return fmt.Errorf("CSV入出力が混雑しています。しばらくしてから再試行してください")
		}
		defer release()
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("CSV read transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	if _, err := backupToCSVV3In(ctx, tx, output); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("CSV read transactionコミットエラー: %w", err)
	}
	return nil
}

// BackupToCSVV2 is an explicit legacy compatibility export. It emits only the
// historical transactions table and is therefore suitable for append imports
// into old clients, but must not be used as a full-ledger backup.
func (s *Service) BackupToCSVV2() (string, error) {
	return s.backupToCSVV2()
}

// backupToCSVV2 preserves the historical transactions-only export contract.
// Keep this separate from the v3 writer so callers cannot accidentally select
// v2 through the default full-backup path.
func (s *Service) backupToCSVV2() (string, error) {
	var builder strings.Builder
	boundedOutput := &csvLimitedStringWriter{dst: &builder, limit: MaxCSVStringImportBytes}
	if err := s.backupToCSVV2ContextWriter(context.Background(), boundedOutput); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func (s *Service) backupToCSVV2ContextWriter(ctx context.Context, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var release func()
	if !HasCSVOperationReservation(ctx) {
		var ok bool
		release, ok = TryAcquireCSVOperationSlot()
		if !ok {
			return fmt.Errorf("CSV入出力が混雑しています。しばらくしてから再試行してください")
		}
		defer release()
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("CSV v2 read transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	if err := backupToCSVV2In(ctx, tx, output); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("CSV v2 read transactionコミットエラー: %w", err)
	}
	return nil
}

type csvContextQueryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func backupToCSVV2In(ctx context.Context, q csvContextQueryer, dst io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	output := &csvLimitedStringWriter{dst: dst, limit: maxCSVExportBytes}
	writer := csv.NewWriter(output)
	if err := writer.Write([]string{"id", "account", "date", "item", "type", "amount", "balance", "memo", csvVersionHeader}); err != nil {
		return fmt.Errorf("CSVヘッダー書き出しエラー: %w", err)
	}
	exportedRows := 0
	rows, err := q.QueryContext(ctx,
		"SELECT id, account, date, item, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), balance, memo FROM transactions ORDER BY date, id",
	)
	if err != nil {
		return fmt.Errorf("バックアップ用データ取得エラー: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if exportedRows >= maxCSVRows {
			return fmt.Errorf("CSV v2行数が上限%dを超えるため、復元不能なバックアップを作成できません", maxCSVRows)
		}
		var id, amount, balance int64
		var account, dateStr, item, txType, memo string
		if err := rows.Scan(&id, &account, &dateStr, &item, &txType, &amount, &balance, &memo); err != nil {
			return fmt.Errorf("バックアップスキャンエラー: %w", err)
		}
		if err := writer.Write([]string{
			fmt.Sprintf("%d", id), encodeCSVTextCell(account), encodeCSVTextCell(dateStr),
			encodeCSVTextCell(item), encodeCSVTextCell(txType), fmt.Sprintf("%d", amount),
			fmt.Sprintf("%d", balance), encodeCSVTextCell(memo), csvVersion2,
		}); err != nil {
			return fmt.Errorf("CSV行書き出しエラー: %w", err)
		}
		exportedRows++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("バックアップ行取得エラー: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("CSV書き出しエラー: %w", err)
	}
	return nil
}

// BackupToCSVFile はCSVバックアップファイルをユーザーのダウンロードフォルダに保存する
func (s *Service) BackupToCSVFile() (string, error) {
	downloadsDir, err := getDownloadsDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(downloadsDir, 0700); err != nil {
		return "", fmt.Errorf("ダウンロードフォルダ作成エラー: %w", err)
	}
	return s.BackupToCSVDirectory(downloadsDir)
}

// BackupToCSVDirectory writes a plaintext CSV directly into a directory the
// user selected. The UI must warn that the destination needs storage-level
// encryption; this method cannot attest an arbitrary mounted volume.
func (s *Service) BackupToCSVDirectory(destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", fmt.Errorf("CSV保存先が選択されていません")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("CSV保存先の解決エラー: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("CSV保存先の確認エラー: %w", err)
	}
	root, err := openVerifiedDirectoryRoot(resolved)
	if err != nil {
		return "", fmt.Errorf("CSV保存先の確認エラー: %w", err)
	}
	defer root.Close()

	filename := fmt.Sprintf("transactions_backup_%s.csv", time.Now().Format("2006-01-02"))

	// BOMを付与してExcel互換にする。CSV本体はread transactionから
	// ファイルへ直接streamし、アーカイブ全体をGo stringへ複製しない。
	filePath, err := writeUniquePrivateStreamAt(root, resolved, filename, func(file io.Writer) error {
		if _, err := io.WriteString(file, "\xEF\xBB\xBF"); err != nil {
			return err
		}
		return s.BackupToCSVStreamContext(context.Background(), file)
	})
	if err != nil {
		return "", fmt.Errorf("CSVファイル書き出しエラー: %w", err)
	}

	return filePath, nil
}

// writeUniquePrivateFile は既存ファイルやsymlinkを上書きせず、所有者だけが
// 読み書きできる新規ファイルへ内容を保存する。
func writeUniquePrivateFile(dir, filename string, data []byte) (string, error) {
	root, err := openVerifiedDirectoryRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return writeUniquePrivateFileAt(root, dir, filename, data)
}

func writeUniquePrivateFileAt(root *os.Root, dir, filename string, data []byte) (string, error) {
	return writeUniquePrivateStreamAt(root, dir, filename, func(file io.Writer) error {
		_, err := io.Copy(file, bytes.NewReader(data))
		return err
	})
}

func writeUniquePrivateStreamAt(root *os.Root, dir, filename string, write func(io.Writer) error) (string, error) {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for attempt := 0; attempt < 100; attempt++ {
		candidateName := filename
		if attempt > 0 {
			candidateName = fmt.Sprintf("%s_%d%s", base, attempt, ext)
		}
		file, err := fileprivacy.CreateExclusive(root, dir, candidateName)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}

		removePartial := func() {
			createdInfo, statErr := file.Stat()
			_ = file.Close()
			if statErr != nil {
				return
			}
			currentInfo, statErr := root.Lstat(candidateName)
			if statErr == nil && os.SameFile(createdInfo, currentInfo) {
				_ = root.Remove(candidateName)
			}
		}
		// Tighten platform permissions before any plaintext bytes are written.
		if err := fileprivacy.Harden(file); err != nil {
			removePartial()
			return "", err
		}
		if err := write(file); err != nil {
			removePartial()
			return "", err
		}
		if err := file.Sync(); err != nil {
			removePartial()
			return "", err
		}
		createdInfo, err := file.Stat()
		if err != nil {
			removePartial()
			return "", err
		}
		if err := file.Close(); err != nil {
			currentInfo, statErr := root.Lstat(candidateName)
			if statErr == nil && os.SameFile(createdInfo, currentInfo) {
				_ = root.Remove(candidateName)
			}
			return "", err
		}
		return filepath.Join(dir, candidateName), nil
	}
	return "", fmt.Errorf("一意なバックアップファイル名を確保できませんでした")
}

// openVerifiedDirectoryRoot pins the checked directory to an OS handle. The
// identity comparison closes the gap where another process replaces the path
// between Lstat and OpenRoot.
func openVerifiedDirectoryRoot(dir string) (*os.Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, fmt.Errorf("保存先は実在するディレクトリを選択してください")
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(before, after) {
		_ = root.Close()
		return nil, fmt.Errorf("保存先が選択後に変更されました")
	}
	return root, nil
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
// 完全なreplaceは関連データを表現できるCSV v3のみで受け付ける。
// v1/v2は既存クライアント互換のappend専用として扱う。
func (s *Service) ImportCSV(content string, mode string) (int, error) {
	return s.ImportCSVContext(context.Background(), content, mode)
}

func (s *Service) ImportCSVContext(ctx context.Context, content string, mode string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Parsing a full v3 export can temporarily hold the raw CSV, decoded JSON
	// strings, and decoded image blobs at once. Keep direct callers subject to
	// the same bounded process-wide concurrency policy as the HTTP endpoint.
	release, ok := TryAcquireCSVImportSlot()
	if !ok {
		return 0, fmt.Errorf("CSVインポートが混雑しています。しばらくしてから再試行してください")
	}
	defer release()
	return s.importCSVContext(ctx, content, mode)
}

func (s *Service) importCSV(content string, mode string) (int, error) {
	return s.importCSVContext(context.Background(), content, mode)
}

func (s *Service) importCSVContext(ctx context.Context, content string, mode string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	if int64(len(content)) > MaxCSVStringImportBytes {
		return 0, fmt.Errorf("文字列CSV入力が上限%d bytesを超えました", MaxCSVStringImportBytes)
	}
	// v3 is a normalized, typed row format. Only its official full header may
	// select the typed parser: a subset cannot describe images/tags/links or
	// settings and must never be allowed to enter destructive replace mode.
	probeInput := &csvFieldLimitReader{ctx: ctx, input: strings.NewReader(content), maxFieldBytes: maxCSVGuardFieldBytes, fieldStart: true}
	probe := csv.NewReader(probeInput)
	probe.FieldsPerRecord = -1
	if headers, probeErr := probe.Read(); probeErr == nil {
		if len(headers) > 0 {
			headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
		}
		isV3 := isCSVV3Header(headers)
		if isV3 {
			parsed, parseErr := s.parseCSVV3Reader(ctx, strings.NewReader(content), false)
			if parseErr != nil {
				return 0, parseErr
			}
			count, importErr := s.importCSVV3Parsed(ctx, &parsed, mode)
			if cleanupErr := parsed.cleanup(); cleanupErr != nil {
				return 0, errors.Join(importErr, fmt.Errorf("CSV画像一時領域のcleanupに失敗しました: %w", cleanupErr))
			}
			return count, importErr
		}
	}
	// Legacy/v1/v2 files cannot describe the extension data that a full
	// replacement must remove. Reject before opening the database so a caller
	// cannot get a partial or transaction-only replacement by format accident.
	if mode == "replace" {
		return 0, ErrCSVReplaceRequiresV3
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	readerInput := &csvFieldLimitReader{ctx: ctx, input: strings.NewReader(content), maxFieldBytes: maxCSVGuardFieldBytes, fieldStart: true}
	reader := csv.NewReader(readerInput)
	headers, err := reader.Read()
	if err != nil {
		return 0, fmt.Errorf("CSVヘッダー読み取りエラー: %w", err)
	}
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
	}
	for _, header := range headers {
		if !utf8.ValidString(header) {
			return 0, fmt.Errorf("CSVヘッダーがUTF-8ではありません")
		}
	}

	headerMap := make(map[string]int, len(headers))
	for i, h := range headers {
		name := strings.TrimSpace(h)
		if name == "" {
			return 0, fmt.Errorf("CSVヘッダーが空です")
		}
		if _, exists := headerMap[name]; exists {
			return 0, fmt.Errorf("CSVヘッダーが重複しています: %s", name)
		}
		headerMap[name] = i
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

	type importRow struct {
		account       string
		date          time.Time
		item          string
		txType        string
		amount        int64
		memo          string
		archiveAmount bool
	}
	var parsedRows []importRow
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("CSV行読み取りエラー (行%d): %w", len(parsedRows)+2, err)
		}
		for _, value := range record {
			if !utf8.ValidString(value) {
				return 0, fmt.Errorf("CSVに不正なUTF-8があります (行%d)", len(parsedRows)+2)
			}
		}

		rowNumber := len(parsedRows) + 2
		if len(parsedRows) >= maxCSVRows {
			return 0, fmt.Errorf("CSV行数が上限%dを超えました", maxCSVRows)
		}
		for _, value := range record {
			if len(value) > maxCSVFieldBytes {
				return 0, fmt.Errorf("CSV列が大きすぎます (行%d)", rowNumber)
			}
		}
		field := func(name string) (string, error) {
			idx := headerMap[name]
			if idx >= len(record) {
				return "", fmt.Errorf("%s列が不足しています (行%d)", name, rowNumber)
			}
			return record[idx], nil
		}

		rowVersion := ""
		if versionedCSV {
			if versionIndex >= len(record) {
				return 0, fmt.Errorf("CSVバージョン列が不足しています (行%d)", rowNumber)
			}
			rowVersion = strings.TrimSpace(record[versionIndex])
			if rowVersion != csvVersion1 && rowVersion != csvVersion2 {
				return 0, fmt.Errorf("未対応のCSVバージョンです (行%d): %q", rowNumber, rowVersion)
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

		if rowVersion == csvVersion2 {
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

		// Unversioned v1, explicit v1, and v2 are historical archive inputs. They may
		// contain values beyond current new-write limits (for example a 300-byte
		// account), so validate against the bounded archive ceiling. v1 retains
		// its historical trim/apostrophe behavior and treats formula-like prefixes
		// as literal text; unsafe controls remain rejected before persistence.
		textValidator := validation.ValidateArchivedLedgerText
		for _, field := range []struct {
			label    string
			value    string
			required bool
		}{
			{"口座名", account, true}, {"項目", item, true}, {"メモ", memo, false},
		} {
			if err := textValidator(field.label, field.value, maxCSVFieldBytes, field.required); err != nil {
				return 0, fmt.Errorf("%sが不正です (行%d): %w", field.label, rowNumber, err)
			}
		}
		if txType != "income" && txType != "expense" {
			return 0, fmt.Errorf("種別はincomeまたはexpenseである必要があります (行%d)", rowNumber)
		}
		amount, err := strconv.ParseInt(amountStr, 10, 64)
		if err != nil || amount <= 0 {
			return 0, fmt.Errorf("金額は正の整数である必要があります (行%d)", rowNumber)
		}

		date, err := parseDateStrict(dateStr)
		if err != nil {
			return 0, fmt.Errorf("日付形式が正しくありません (行%d): %w", rowNumber, err)
		}
		parsedRows = append(parsedRows, importRow{account: account, date: date, item: item, txType: txType, amount: amount, memo: memo, archiveAmount: amount > validation.MaxTransactionAmount})
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)")
	if err != nil {
		return 0, fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	affectedAccounts := make(map[string]struct{})
	for index, row := range parsedRows {
		if err := ctx.Err(); err != nil {
			_ = stmt.Close()
			return 0, err
		}
		storedAmount := row.amount
		if row.archiveAmount {
			storedAmount = validation.MaxTransactionAmount
		}
		result, err := stmt.ExecContext(ctx, row.account, row.date, row.item, row.txType, storedAmount, row.memo)
		if err != nil {
			_ = stmt.Close()
			return 0, fmt.Errorf("CSVインポートエラー (行%d): %w", index+2, err)
		}
		if row.archiveAmount {
			id, idErr := result.LastInsertId()
			if idErr != nil {
				_ = stmt.Close()
				return 0, fmt.Errorf("CSV legacy取引ID取得エラー (行%d): %w", index+2, idErr)
			}
			if _, archiveErr := tx.ExecContext(ctx, "INSERT INTO transaction_archive_amounts (transaction_id, amount) VALUES (?, ?)", id, row.amount); archiveErr != nil {
				_ = stmt.Close()
				return 0, fmt.Errorf("CSV legacy金額保存エラー (行%d): %w", index+2, archiveErr)
			}
		}
		affectedAccounts[row.account] = struct{}{}
	}
	if err := stmt.Close(); err != nil {
		return 0, fmt.Errorf("CSVステートメントクローズエラー: %w", err)
	}

	for account := range affectedAccounts {
		if err := recalculateBalanceInContext(ctx, tx, account); err != nil {
			return 0, fmt.Errorf("残高再計算エラー (%s): %w", account, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("インポートコミットエラー: %w", err)
	}

	s.autoSnapshot()
	return len(parsedRows), nil
}

// --- ヘルパー関数 ---

type sqlExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Prepare(query string) (*sql.Stmt, error)
}

type sqlContextExecutor interface {
	sqlExecutor
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
	PrepareContext(context.Context, string) (*sql.Stmt, error)
}

func recalculateBalanceInContext(ctx context.Context, q sqlContextExecutor, account string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := q.QueryContext(ctx,
		"SELECT id, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount) FROM transactions WHERE account = ? ORDER BY date, id", account)
	if err != nil {
		return fmt.Errorf("残高再計算クエリエラー: %w", err)
	}
	defer rows.Close()
	type balanceUpdate struct {
		id      int64
		balance int64
	}
	updates := make([]balanceUpdate, 0)
	var runningBalance int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		return fmt.Errorf("残高再計算行取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	stmt, err := q.PrepareContext(ctx, "UPDATE transactions SET balance = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	defer stmt.Close()
	for _, update := range updates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, update.balance, update.id); err != nil {
			return fmt.Errorf("残高更新エラー (id=%d): %w", update.id, err)
		}
	}
	return nil
}

// recalculateBalanceIn は指定されたDBまたはトランザクション内で口座残高を再計算する。
func recalculateBalanceIn(q sqlExecutor, account string) error {
	// 時系列順で取引データを取得
	rows, err := q.Query(
		"SELECT id, type, COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount) FROM transactions WHERE account = ? ORDER BY date, id",
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
func (s *Service) recalculateBalance(account string) error {
	db, err := s.database()
	if err != nil {
		return err
	}
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
	if err := validation.ValidateLedgerText("口座名", req.Account, validation.MaxAccountBytes, true); err != nil {
		return err
	}
	if err := validation.ValidateLedgerText("項目", req.Item, validation.MaxItemBytes, true); err != nil {
		return err
	}
	if err := validation.ValidateLedgerText("メモ", req.Memo, validation.MaxMemoBytes, false); err != nil {
		return err
	}
	if err := validation.ValidateLedgerText("種別", req.Type, 32, true); err != nil {
		return err
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
	balancesExact := make(map[string][]string)
	for _, acc := range accounts {
		balances[acc] = make([]int64, len(dates))
		balancesExact[acc] = make([]string, len(dates))
		var lastBalance int64
		for i, date := range dates {
			if b, ok := accountBalances[acc][date]; ok {
				lastBalance = b
			}
			balances[acc][i] = lastBalance
			balancesExact[acc][i] = models.ExactInt64(lastBalance)
		}
	}

	return &models.BalanceHistoryResponse{
		Accounts:      accounts,
		Dates:         dates,
		Balances:      balances,
		BalancesExact: balancesExact,
	}, nil
}

// --- 画像管理 (Agent.md §6.5) ---

// AddTransactionImage は取引に画像を追加する
func (s *Service) AddTransactionImage(transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	return s.AddTransactionImageContext(context.Background(), transactionID, img)
}

func (s *Service) AddTransactionImageContext(ctx context.Context, transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	prepared, err := prepareTransactionImagesContext(ctx, []models.TransactionImageRequest{img})
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
	s.autoSnapshot()
	return resp, nil
}

// GetTransactionImages は取引の画像一覧を返す
func (s *Service) GetTransactionImages(transactionID int64) ([]models.TransactionImageResponse, error) {
	return s.GetTransactionImagesContext(context.Background(), transactionID)
}

func (s *Service) GetTransactionImagesContext(ctx context.Context, transactionID int64) ([]models.TransactionImageResponse, error) {
	images := make([]models.TransactionImageResponse, 0, models.MaxImagesPerTransaction)
	cursor := ""
	for {
		page, err := s.GetTransactionImagesPageContext(ctx, transactionID, cursor, maxTransactionImagePageSize)
		if err != nil {
			return nil, err
		}
		images = append(images, page.Images...)
		if page.NextCursor == "" {
			return images, nil
		}
		if len(images) >= models.MaxImagesPerTransaction {
			return nil, fmt.Errorf("画像一覧が%d件を超えています。pagination APIを使用してください", models.MaxImagesPerTransaction)
		}
		cursor = page.NextCursor
	}
}

const (
	defaultTransactionImagePageSize = 2
	maxTransactionImagePageSize     = 2
	maxTransactionImageCursor       = models.MaxArchivedImagesDatabase + models.MaxImagesPerTransaction
)

func (s *Service) GetTransactionImagesPage(transactionID int64, cursor string, limit int) (*models.TransactionImagePage, error) {
	return s.GetTransactionImagesPageContext(context.Background(), transactionID, cursor, limit)
}

func (s *Service) GetTransactionImagesPageContext(ctx context.Context, transactionID int64, cursor string, limit int) (*models.TransactionImagePage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if transactionID <= 0 {
		return nil, fmt.Errorf("無効な取引IDです")
	}
	if limit == 0 {
		limit = defaultTransactionImagePageSize
	}
	if limit < 1 || limit > maxTransactionImagePageSize {
		return nil, fmt.Errorf("画像一覧のlimitは1から%dまでです", maxTransactionImagePageSize)
	}
	offset := int64(0)
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || parsed < 0 || parsed > int64(maxTransactionImageCursor) {
			return nil, fmt.Errorf("画像一覧のcursorが無効です")
		}
		offset = parsed
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT response_id, filename, data, mime_type, created_at FROM (
		 SELECT id AS response_id, id AS sort_id, 0 AS source, filename, data, mime_type, created_at
		 FROM transaction_images WHERE transaction_id = ?
		 UNION ALL
		 SELECT -id AS response_id, id AS sort_id, 1 AS source, filename, data, mime_type, created_at
		 FROM transaction_image_archive WHERE transaction_id = ?
		) ORDER BY created_at, source, sort_id LIMIT ? OFFSET ?`,
		transactionID, transactionID, limit+1, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("画像一覧取得エラー: %w", err)
	}
	defer rows.Close()

	images := make([]models.TransactionImageResponse, 0, limit)
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
		prepared, validationErr := prepareDecodedTransactionImageContext(ctx, filename, mimeType, data)
		if validationErr != nil {
			// 旧バージョンで保存された不正BLOBをブラウザへ返さない。
			response.Invalid = true
		} else {
			response.MimeType = prepared.mimeType
			response.DataURL = fmt.Sprintf("data:%s;base64,%s", prepared.mimeType, base64.StdEncoding.EncodeToString(prepared.data))
		}
		images = append(images, response)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("画像一覧取得エラー: %w", err)
	}
	nextCursor := ""
	if len(images) > limit {
		images = images[:limit]
		nextCursor = strconv.FormatInt(offset+int64(limit), 10)
	}
	return &models.TransactionImagePage{Images: images, NextCursor: nextCursor}, nil
}

// DeleteTransactionImage はWails互換用に画像IDを指定して削除する。
func (s *Service) DeleteTransactionImage(imageID int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	if imageID < 0 {
		if imageID == math.MinInt64 {
			return fmt.Errorf("画像が見つかりません")
		}
		imageID = -imageID
		result, err := db.Exec("DELETE FROM transaction_image_archive WHERE id = ?", imageID)
		return s.finishTransactionImageDelete(result, err)
	}
	result, err := db.Exec("DELETE FROM transaction_images WHERE id = ?", imageID)
	return s.finishTransactionImageDelete(result, err)
}

// DeleteTransactionImageForTransaction はURL上の取引IDと画像の所属を照合して削除する。
func (s *Service) DeleteTransactionImageForTransaction(transactionID, imageID int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	if imageID < 0 {
		if imageID == math.MinInt64 {
			return fmt.Errorf("画像が見つかりません")
		}
		imageID = -imageID
		result, err := db.Exec(
			"DELETE FROM transaction_image_archive WHERE transaction_id = ? AND id = ?",
			transactionID, imageID,
		)
		return s.finishTransactionImageDelete(result, err)
	}
	result, err := db.Exec(
		"DELETE FROM transaction_images WHERE transaction_id = ? AND id = ?",
		transactionID, imageID,
	)
	return s.finishTransactionImageDelete(result, err)
}

func (s *Service) finishTransactionImageDelete(result sql.Result, err error) error {
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
	s.autoSnapshot()
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

	// A pre-quota transaction may legitimately contain more than today's
	// per-transaction count/byte limits. Moving it between accounts with no new
	// images must not strand the transaction. New image bytes still enforce the
	// current per-transaction contract.
	if len(images) > 0 {
		var transactionCount, transactionBytes int64
		if err := db.QueryRow(
			`SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM (
				SELECT length(data) AS bytes FROM transaction_images WHERE transaction_id = ?
				UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive WHERE transaction_id = ?
			)`, transactionID, transactionID,
		).Scan(&transactionCount, &transactionBytes); err != nil {
			return fmt.Errorf("取引画像使用量確認エラー: %w", err)
		}
		if transactionCount+int64(len(images)) > int64(models.MaxImagesPerTransaction) {
			return fmt.Errorf("画像は1取引につき%d件までです", models.MaxImagesPerTransaction)
		}
		if transactionBytes+additionalBytes > models.MaxImageBytesPerTransaction {
			return fmt.Errorf("画像データの合計は1取引につき%d MiBまでです", models.MaxImageBytesPerTransaction/(1024*1024))
		}
	}

	var accountBytes int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM (
		SELECT length(ti.data) AS bytes FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id WHERE t.account = ?
		UNION ALL SELECT length(ai.data) AS bytes FROM transaction_image_archive ai JOIN transactions t ON t.id = ai.transaction_id WHERE t.account = ?
	)`, account, account,
	).Scan(&accountBytes); err != nil {
		return fmt.Errorf("口座画像使用量確認エラー: %w", err)
	}
	if accountBytes+additionalBytes > models.MaxImageBytesPerAccount {
		return fmt.Errorf("口座「%s」の画像保存量は%d MiBまでです", account, models.MaxImageBytesPerAccount/(1024*1024))
	}

	var databaseBytes int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM (
		SELECT length(data) AS bytes FROM transaction_images
		UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive
	)`).Scan(&databaseBytes); err != nil {
		return fmt.Errorf("画像DB使用量確認エラー: %w", err)
	}
	if databaseBytes+additionalBytes > models.MaxImageBytesDatabase {
		return fmt.Errorf("DB全体の画像保存量は%d MiBまでです", models.MaxImageBytesDatabase/(1024*1024))
	}
	return nil
}

// checkImageAccountMoveQuota applies quota to growth, not to grandfathered
// bytes that an account-only edit merely relocates. The database total never
// changes. For account quota, compare aggregate overage before/after the move:
// moving an existing over-limit transaction to an empty account keeps the
// same overage and is allowed, while concentrating it into an already-used
// destination (increasing total overage) is rejected.
func checkImageAccountMoveQuota(db sqlExecutor, transactionID int64, oldAccount, newAccount string) error {
	if oldAccount == newAccount {
		return nil
	}
	var transactionBytes int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM (
		SELECT length(data) AS bytes FROM transaction_images WHERE transaction_id = ?
		UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive WHERE transaction_id = ?
	)`, transactionID, transactionID).Scan(&transactionBytes); err != nil {
		return fmt.Errorf("取引画像使用量確認エラー: %w", err)
	}
	accountBytes := func(account string) (int64, error) {
		var bytes int64
		err := db.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM (
			SELECT length(ti.data) AS bytes FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id WHERE t.account = ?
			UNION ALL SELECT length(ai.data) AS bytes FROM transaction_image_archive ai JOIN transactions t ON t.id = ai.transaction_id WHERE t.account = ?
		)`, account, account).Scan(&bytes)
		return bytes, err
	}
	oldAfter, err := accountBytes(oldAccount)
	if err != nil {
		return fmt.Errorf("移動元口座画像使用量確認エラー: %w", err)
	}
	newAfter, err := accountBytes(newAccount)
	if err != nil {
		return fmt.Errorf("移動先口座画像使用量確認エラー: %w", err)
	}
	if transactionBytes < 0 || oldAfter > math.MaxInt64-transactionBytes || newAfter < transactionBytes {
		return fmt.Errorf("口座画像使用量の差分が不正です")
	}
	oldBefore := oldAfter + transactionBytes
	newBefore := newAfter - transactionBytes
	overage := func(bytes int64) int64 {
		if bytes <= models.MaxImageBytesPerAccount {
			return 0
		}
		return bytes - models.MaxImageBytesPerAccount
	}
	beforeOld, beforeNew := overage(oldBefore), overage(newBefore)
	afterOld, afterNew := overage(oldAfter), overage(newAfter)
	if beforeOld > math.MaxInt64-beforeNew || afterOld > math.MaxInt64-afterNew {
		return fmt.Errorf("口座画像使用量の差分が大きすぎます")
	}
	if afterOld+afterNew > beforeOld+beforeNew {
		return fmt.Errorf("口座「%s」への移動で画像保存量の超過が増加します", newAccount)
	}
	return nil
}

// GetImageStorageUsage は現在の画像保存量と上限を返す。
func (s *Service) GetImageStorageUsage() (*models.ImageStorageUsage, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
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
		`SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM (
			SELECT length(data) AS bytes FROM transaction_images
			UNION ALL SELECT length(data) AS bytes FROM transaction_image_archive
		)`,
	).Scan(&usage.ImageCount, &usage.Bytes); err != nil {
		return nil, fmt.Errorf("画像使用量取得エラー: %w", err)
	}

	rows, err := db.Query(`SELECT account, COUNT(*), COALESCE(SUM(bytes), 0) FROM (
		SELECT t.account AS account, length(ti.data) AS bytes FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id
		UNION ALL SELECT t.account AS account, length(ai.data) AS bytes FROM transaction_image_archive ai JOIN transactions t ON t.id = ai.transaction_id
	) GROUP BY account ORDER BY account`)
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
func (s *Service) CreateTag(name string, parentID *int64) (*models.Tag, error) {
	name, err := validation.ValidateTagName(name)
	if err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("タグtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	level := 1
	if parentID != nil {
		var parentLevel int
		err := tx.QueryRow("SELECT level FROM tags WHERE id = ?", *parentID).Scan(&parentLevel)
		if err != nil {
			return nil, fmt.Errorf("親タグが見つかりません: %w", err)
		}
		if err := validation.ValidateTagHierarchy(parentLevel+1, &parentLevel); err != nil {
			return nil, err
		}
		level = parentLevel + 1
	} else if err := validation.ValidateTagHierarchy(level, nil); err != nil {
		return nil, err
	}
	if parentID == nil {
		var roots int
		if err := tx.QueryRow("SELECT COUNT(*) FROM tags WHERE name = ? AND parent_id IS NULL", name).Scan(&roots); err != nil {
			return nil, fmt.Errorf("rootタグ重複確認エラー: %w", err)
		}
		if roots != 0 {
			return nil, fmt.Errorf("同名のrootタグは作成できません")
		}
	}

	result, err := tx.Exec(
		"INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)",
		name, parentID, level,
	)
	if err != nil {
		return nil, fmt.Errorf("タグ作成エラー: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("タグID取得エラー: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("タグtransaction確定エラー: %w", err)
	}
	tag := &models.Tag{
		ID:       id,
		Name:     name,
		ParentID: parentID,
		Level:    level,
	}
	s.autoSnapshot()
	return tag, nil
}

// GetTags はタグ一覧をツリー構造で返す
func (s *Service) GetTags() ([]models.Tag, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
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

// GetTagDeleteImpact reports the cascade boundary before a destructive tag
// operation. The recursive query includes the selected tag when counting
// affected transactions, while DescendantCount reports child tags only.
func (s *Service) GetTagDeleteImpact(id int64) (*models.TagDeleteImpact, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	impact := &models.TagDeleteImpact{TagID: id}
	err = db.QueryRow(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM tags WHERE id = ?
			UNION ALL
			SELECT child.id FROM tags child JOIN descendants parent ON child.parent_id = parent.id
		)
		SELECT root.name,
		       (SELECT COUNT(*) FROM descendants) - 1,
		       (SELECT COUNT(DISTINCT tt.transaction_id)
		          FROM transaction_tags tt JOIN descendants d ON d.id = tt.tag_id)
		FROM tags root WHERE root.id = ?`, id, id,
	).Scan(&impact.TagName, &impact.DescendantCount, &impact.TransactionCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("タグが見つかりません: %w", err)
		}
		return nil, fmt.Errorf("タグ削除影響の取得エラー: %w", err)
	}
	return impact, nil
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
func (s *Service) UpdateTag(id int64, name string) error {
	name, err := validation.ValidateTagName(name)
	if err != nil {
		return err
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("タグtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	var parentID sql.NullInt64
	var oldName string
	if err := tx.QueryRow("SELECT parent_id, name FROM tags WHERE id = ?", id).Scan(&parentID, &oldName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("タグが見つかりません: %w", err)
		}
		return fmt.Errorf("タグ親取得エラー: %w", err)
	}
	if !parentID.Valid {
		var roots int
		if err := tx.QueryRow("SELECT COUNT(*) FROM tags WHERE name = ? AND parent_id IS NULL AND id <> ?", name, id).Scan(&roots); err != nil {
			return fmt.Errorf("rootタグ重複確認エラー: %w", err)
		}
		if roots != 0 {
			return fmt.Errorf("同名のrootタグは作成できません")
		}
	}
	result, err := tx.Exec("UPDATE tags SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("タグ更新エラー: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("タグ更新結果確認エラー: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("タグが見つかりません: %w", sql.ErrNoRows)
	}
	if !parentID.Valid {
		if err := normalizeLegacyRootTagMarkers(tx, oldName); err != nil {
			return fmt.Errorf("旧rootタグ整合性更新エラー: %w", err)
		}
		if err := normalizeLegacyRootTagMarkers(tx, name); err != nil {
			return fmt.Errorf("新rootタグ整合性更新エラー: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("タグtransactionコミットエラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

// CreateTagByPath は「/」区切りのパスからタグを階層的に作成する
// 例: "推し活/超かぐや姫！" → 「推し活」(L1) → 「超かぐや姫！」(L2) を作成
func (s *Service) CreateTagByPath(path string) (*models.Tag, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	parts := strings.Split(path, "/")
	var segments []string
	for _, p := range parts {
		canonical, validateErr := validation.ValidateTagName(p)
		if validateErr != nil {
			return nil, validateErr
		}
		segments = append(segments, canonical)
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("タグ名が空です")
	}
	if err := validation.ValidateTagLevel(len(segments)); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 5; attempt++ {
		tag, txErr := createTagPathIn(db, segments)
		if txErr == nil {
			s.autoSnapshot()
			return tag, nil
		}
		if !isSQLiteBusyError(txErr) || attempt == 4 {
			return nil, txErr
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return nil, fmt.Errorf("タグ作成を完了できませんでした")
}

func createTagPathIn(db *sql.DB, segments []string) (*models.Tag, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("タグtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	var parentID *int64
	var tag *models.Tag
	for i, name := range segments {
		level := i + 1
		parentLevel := i
		if i == 0 {
			if err := validation.ValidateTagHierarchy(level, nil); err != nil {
				return nil, err
			}
		} else if err := validation.ValidateTagHierarchy(level, &parentLevel); err != nil {
			return nil, err
		}
		var existingID int64
		if parentID == nil {
			var rootCount int
			if countErr := tx.QueryRow("SELECT COUNT(*) FROM tags WHERE name = ? AND parent_id IS NULL", name).Scan(&rootCount); countErr != nil {
				return nil, fmt.Errorf("rootタグ重複確認エラー: %w", countErr)
			}
			if rootCount > 1 {
				return nil, fmt.Errorf("同名のrootタグが複数存在するため通常のタグ操作を中止しました")
			}
			var legacyDuplicate int
			err = tx.QueryRow("SELECT id, legacy_duplicate FROM tags WHERE name = ? AND parent_id IS NULL ORDER BY id LIMIT 1", name).Scan(&existingID, &legacyDuplicate)
			if err == nil && legacyDuplicate != 0 {
				// A lone archived duplicate is the same logical root. Promote it
				// before reuse instead of creating a second root row.
				if _, updateErr := tx.Exec("UPDATE tags SET legacy_duplicate = 0 WHERE id = ?", existingID); updateErr != nil {
					return nil, fmt.Errorf("legacy rootタグ正規化エラー: %w", updateErr)
				}
			}
		} else {
			err = tx.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id = ?", name, *parentID).Scan(&existingID)
		}
		if err == nil {
			tag = &models.Tag{ID: existingID, Name: name, ParentID: parentID, Level: level}
			pid := existingID
			parentID = &pid
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("タグ検索エラー: %w", err)
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO tags (name, parent_id, level) VALUES (?, ?, ?)", name, parentID, level); err != nil {
			return nil, fmt.Errorf("タグ作成エラー: %w", err)
		}
		// The INSERT may have been ignored because another connection won the
		// race; fetch the authoritative row from this transaction.
		if parentID == nil {
			err = tx.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id IS NULL AND legacy_duplicate = 0", name).Scan(&existingID)
		} else {
			err = tx.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id = ?", name, *parentID).Scan(&existingID)
		}
		if err != nil {
			return nil, fmt.Errorf("タグ作成後の検索エラー: %w", err)
		}
		tag = &models.Tag{ID: existingID, Name: name, ParentID: parentID, Level: level}
		pid := existingID
		parentID = &pid
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("タグtransaction確定エラー: %w", err)
	}
	return tag, nil
}

// normalizeLegacyRootTagMarkers restores the schema invariant after a tag
// rename or deletion. At most one root of a name is the ordinary marker=0 row;
// additional historical rows remain marker=1 and are never merged.
func normalizeLegacyRootTagMarkers(tx *sql.Tx, name string) error {
	if name == "" {
		return nil
	}
	if _, err := tx.Exec("UPDATE tags SET legacy_duplicate = 1 WHERE parent_id IS NULL AND name = ?", name); err != nil {
		return err
	}
	var firstID int64
	err := tx.QueryRow("SELECT id FROM tags WHERE parent_id IS NULL AND name = ? ORDER BY id LIMIT 1", name).Scan(&firstID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE tags SET legacy_duplicate = 0 WHERE id = ?", firstID)
	return err
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "busy")
}

// DeleteTag はタグを削除する（子タグも連鎖削除）
func (s *Service) DeleteTag(id int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("タグtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	var parentID sql.NullInt64
	var name string
	if err := tx.QueryRow("SELECT parent_id, name FROM tags WHERE id = ?", id).Scan(&parentID, &name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("タグが見つかりません: %w", err)
		}
		return fmt.Errorf("タグ取得エラー: %w", err)
	}
	result, err := tx.Exec("DELETE FROM tags WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("タグ削除エラー: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("タグ削除結果確認エラー: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("タグが見つかりません: %w", sql.ErrNoRows)
	}
	if !parentID.Valid {
		if err := normalizeLegacyRootTagMarkers(tx, name); err != nil {
			return fmt.Errorf("rootタグ整合性更新エラー: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("タグtransactionコミットエラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

// GetTransactionTags は取引に紐付いたタグを返す
func (s *Service) GetTransactionTags(transactionID int64) ([]models.Tag, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
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
func (s *Service) AddTransactionTags(transactionID int64, tagIDs []int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("タグtransaction開始エラー: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT 1 FROM transactions WHERE id = ?", transactionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("取引が見つかりません: %w", err)
		}
		return fmt.Errorf("取引存在確認エラー: %w", err)
	}
	seen := make(map[int64]struct{}, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			return fmt.Errorf("無効なタグIDです: %d", tagID)
		}
		if _, ok := seen[tagID]; ok {
			continue
		}
		seen[tagID] = struct{}{}
		if err := tx.QueryRow("SELECT 1 FROM tags WHERE id = ?", tagID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("タグが見つかりません: %d", tagID)
			}
			return fmt.Errorf("タグ存在確認エラー: %w", err)
		}
	}
	for tagID := range seen {
		_, err := tx.Exec(
			"INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)",
			transactionID, tagID,
		)
		if err != nil {
			return fmt.Errorf("タグ追加エラー: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("タグtransaction確定エラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

// RemoveTransactionTag は取引からタグを削除する
func (s *Service) RemoveTransactionTag(transactionID, tagID int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	_, err = db.Exec(
		"DELETE FROM transaction_tags WHERE transaction_id = ? AND tag_id = ?",
		transactionID, tagID,
	)
	if err == nil {
		s.autoSnapshot()
	}
	return err
}

// GetTagSummary はタグ別集計データを返す（円グラフ用）
// フィルタ条件はLEFT JOINのON句に配置し、全タグを保持した上で
// 子タグの金額を親タグに集約する。
func (s *Service) GetTagSummary(txType string, startDate, endDate string) ([]models.TagSummary, error) {
	return s.getTagSummaryFiltered(txType, startDate, endDate, "", nil)
}

type tagSummaryOptions struct {
	maxScannedNodes      int
	maxMaterializedNodes int
}

type tagSummaryData struct {
	id       int64
	name     string
	level    int
	parentID sql.NullInt64
	amount   int64
	count    int
}

type tagSummaryNode struct {
	data         tagSummaryData
	amount       int64
	count        int
	childIndexes []int
}

type tagSummaryForest struct {
	nodes       []tagSummaryNode
	rootIndexes []int
}

// getTagSummaryFiltered はAI分析を含む呼び出し元の全フィルターを適用し、
// 条件に一致した取引群についてタグ別集計を返す。
func (s *Service) getTagSummaryFiltered(txType, startDate, endDate, account string, tagIDs []int64) ([]models.TagSummary, error) {
	summaries, _, err := s.getTagSummaryFilteredContext(
		context.Background(),
		txType,
		startDate,
		endDate,
		account,
		tagIDs,
		tagSummaryOptions{},
	)
	return summaries, err
}

func (s *Service) getTagSummaryFilteredContext(
	ctx context.Context,
	txType, startDate, endDate, account string,
	tagIDs []int64,
	options tagSummaryOptions,
) ([]models.TagSummary, bool, error) {
	db, err := s.database()
	if err != nil {
		return nil, false, err
	}

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

	// #nosec G202 -- joinConditions contains only fixed SQL fragments selected
	// by code; all user values remain bound parameters.
	query := `SELECT t.id, t.name, t.level, t.parent_id,
		COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = tr.id), tr.amount), tr.id
		FROM tags t
		LEFT JOIN transaction_tags tt ON t.id = tt.tag_id
		LEFT JOIN transactions tr ON ` + strings.Join(joinConditions, " AND ") + `
		ORDER BY t.id, tr.id`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("タグ集計エラー: %w", err)
	}
	defer rows.Close()

	var allData []tagSummaryData
	indexes := make(map[int64]int)

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, false, fmt.Errorf("タグ集計キャンセル: %w", err)
		}
		var id int64
		var name string
		var level int
		var parentID, amount, transactionID sql.NullInt64
		if err := rows.Scan(&id, &name, &level, &parentID, &amount, &transactionID); err != nil {
			return nil, false, err
		}
		index, exists := indexes[id]
		if !exists {
			if options.maxScannedNodes > 0 && len(allData) >= options.maxScannedNodes {
				return nil, false, fmt.Errorf("タグ集計対象が内部上限%d件を超えています", options.maxScannedNodes)
			}
			index = len(allData)
			indexes[id] = index
			allData = append(allData, tagSummaryData{id: id, name: name, level: level, parentID: parentID})
		}
		if transactionID.Valid {
			if !amount.Valid {
				return nil, false, fmt.Errorf("タグ集計の取引金額が不正です: transaction_id=%d", transactionID.Int64)
			}
			var addErr error
			allData[index].amount, addErr = validation.CheckedAddInt64(allData[index].amount, amount.Int64)
			if addErr != nil {
				return nil, false, fmt.Errorf("タグ直接金額集計オーバーフロー (tag_id=%d): %w", id, addErr)
			}
			allData[index].count++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("タグ集計行取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, false, fmt.Errorf("タグ集計行終了エラー: %w", err)
	}
	// Preserve the historical public ordering that SQL ORDER BY SUM provided,
	// while doing the overflow-sensitive accumulation in checked Go arithmetic.
	sort.SliceStable(allData, func(i, j int) bool { return allData[i].amount > allData[j].amount })

	forest, err := buildTagSummaryForest(ctx, allData)
	if err != nil {
		return nil, false, err
	}
	totalAmount, err := forest.rollup(ctx)
	if err != nil {
		return nil, false, err
	}
	result, truncated, err := forest.materialize(ctx, totalAmount, options.maxMaterializedNodes)
	if err != nil {
		return nil, false, err
	}
	if result == nil {
		result = []models.TagSummary{}
	}
	return result, truncated, nil
}

func buildTagSummaryForest(ctx context.Context, data []tagSummaryData) (*tagSummaryForest, error) {
	forest := &tagSummaryForest{nodes: make([]tagSummaryNode, len(data))}
	idToIndex := make(map[int64]int, len(data))
	for i := range data {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("タグ構造検証キャンセル: %w", err)
		}
		if _, exists := idToIndex[data[i].id]; exists {
			return nil, fmt.Errorf("タグ構造が不正です: 重複tag_id=%d", data[i].id)
		}
		idToIndex[data[i].id] = i
		forest.nodes[i].data = data[i]
	}

	// idToIndexはlookupにのみ使い、SQLのscan順でroot/子sliceへ追加する。
	for i := range forest.nodes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("タグ構造検証キャンセル: %w", err)
		}
		parentID := forest.nodes[i].data.parentID
		if !parentID.Valid {
			forest.rootIndexes = append(forest.rootIndexes, i)
			continue
		}
		parentIndex, exists := idToIndex[parentID.Int64]
		if !exists {
			return nil, fmt.Errorf(
				"タグ構造が不正です: orphan tag_id=%d parent_id=%d",
				forest.nodes[i].data.id,
				parentID.Int64,
			)
		}
		forest.nodes[parentIndex].childIndexes = append(forest.nodes[parentIndex].childIndexes, i)
	}

	// cycle検証前にlevel範囲とroot levelを反復検証する。
	for i := range forest.nodes {
		node := &forest.nodes[i]
		if node.data.level < 1 || node.data.level > 3 {
			return nil, fmt.Errorf(
				"タグ構造が不正です: tag_id=%d のlevel=%dは1から3の範囲外です",
				node.data.id,
				node.data.level,
			)
		}
		if !node.data.parentID.Valid {
			if node.data.level != 1 {
				return nil, fmt.Errorf(
					"タグ構造が不正です: root tag_id=%d のlevel=%dです",
					node.data.id,
					node.data.level,
				)
			}
			continue
		}
	}

	// rootを起点にKahn法で反復走査し、長大な破損chainでもstackを消費しない。
	queue := append([]int(nil), forest.rootIndexes...)
	reachable := make([]bool, len(forest.nodes))
	reachableCount := 0
	for cursor := 0; cursor < len(queue); cursor++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("タグ構造検証キャンセル: %w", err)
		}
		index := queue[cursor]
		if reachable[index] {
			return nil, fmt.Errorf("タグ構造が不正です: tag_id=%dへの重複経路があります", forest.nodes[index].data.id)
		}
		reachable[index] = true
		reachableCount++
		queue = append(queue, forest.nodes[index].childIndexes...)
	}
	if reachableCount != len(forest.nodes) {
		for i := range forest.nodes {
			if !reachable[i] {
				return nil, fmt.Errorf("タグ構造が不正です: cycle tag_id=%d", forest.nodes[i].data.id)
			}
		}
		return nil, fmt.Errorf("タグ構造が不正です: rootから到達できないタグがあります")
	}

	// cycleがないことを確定した後に親子levelを検証し、後続再帰の深さを3以内に限定する。
	for i := range forest.nodes {
		node := &forest.nodes[i]
		if !node.data.parentID.Valid {
			continue
		}
		parentIndex := idToIndex[node.data.parentID.Int64]
		expectedLevel := forest.nodes[parentIndex].data.level + 1
		if node.data.level != expectedLevel {
			return nil, fmt.Errorf(
				"タグ構造が不正です: tag_id=%d のlevel=%d、parent_id=%d に対する期待level=%d",
				node.data.id,
				node.data.level,
				node.data.parentID.Int64,
				expectedLevel,
			)
		}
	}

	return forest, nil
}

func (forest *tagSummaryForest) rollup(ctx context.Context) (int64, error) {
	var rollupNode func(int) error
	rollupNode = func(index int) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("タグ金額集計キャンセル: %w", err)
		}
		node := &forest.nodes[index]
		var positiveChildren []int
		for _, childIndex := range node.childIndexes {
			if err := rollupNode(childIndex); err != nil {
				return err
			}
			if forest.nodes[childIndex].amount > 0 {
				positiveChildren = append(positiveChildren, childIndex)
			}
		}
		sort.Slice(positiveChildren, func(i, j int) bool {
			return forest.nodes[positiveChildren[i]].amount > forest.nodes[positiveChildren[j]].amount
		})

		amount := node.data.amount
		count := node.data.count
		for _, childIndex := range positiveChildren {
			var err error
			amount, err = validation.CheckedAddInt64(amount, forest.nodes[childIndex].amount)
			if err != nil {
				return fmt.Errorf("タグ金額集計オーバーフロー (tag_id=%d): %w", node.data.id, err)
			}
			count += forest.nodes[childIndex].count
		}
		node.amount = amount
		node.count = count
		node.childIndexes = positiveChildren
		return nil
	}

	var positiveRoots []int
	for _, rootIndex := range forest.rootIndexes {
		if err := rollupNode(rootIndex); err != nil {
			return 0, err
		}
		if forest.nodes[rootIndex].amount > 0 {
			positiveRoots = append(positiveRoots, rootIndex)
		}
	}
	sort.Slice(positiveRoots, func(i, j int) bool {
		return forest.nodes[positiveRoots[i]].amount > forest.nodes[positiveRoots[j]].amount
	})
	forest.rootIndexes = positiveRoots

	var totalAmount int64
	for _, rootIndex := range forest.rootIndexes {
		var err error
		totalAmount, err = validation.CheckedAddInt64(totalAmount, forest.nodes[rootIndex].amount)
		if err != nil {
			return 0, fmt.Errorf("タグ合計オーバーフロー: %w", err)
		}
	}
	return totalAmount, nil
}

func (forest *tagSummaryForest) materialize(
	ctx context.Context,
	totalAmount int64,
	maxNodes int,
) ([]models.TagSummary, bool, error) {
	var countVisible func([]int) (int, error)
	countVisible = func(indexes []int) (int, error) {
		count := 0
		for _, index := range indexes {
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("タグ応答構築キャンセル: %w", err)
			}
			childCount, err := countVisible(forest.nodes[index].childIndexes)
			if err != nil {
				return 0, err
			}
			count += 1 + childCount
		}
		return count, nil
	}

	truncated := false
	if maxNodes > 0 {
		visibleCount, err := countVisible(forest.rootIndexes)
		if err != nil {
			return nil, false, err
		}
		truncated = visibleCount > maxNodes
	}

	remaining := maxNodes
	var materializeIndexes func([]int) ([]models.TagSummary, error)
	materializeIndexes = func(indexes []int) ([]models.TagSummary, error) {
		var summaries []models.TagSummary
		for _, index := range indexes {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("タグ応答構築キャンセル: %w", err)
			}
			if maxNodes > 0 && remaining <= 0 {
				break
			}
			if maxNodes > 0 {
				remaining--
			}
			node := &forest.nodes[index]
			children, err := materializeIndexes(node.childIndexes)
			if err != nil {
				return nil, err
			}
			ratio := 0.0
			if totalAmount > 0 {
				ratio = float64(node.amount) / float64(totalAmount)
			}
			summaries = append(summaries, models.TagSummary{
				TagID:       node.data.id,
				TagName:     node.data.name,
				Amount:      node.amount,
				AmountExact: models.ExactInt64(node.amount),
				Count:       node.count,
				Ratio:       ratio,
				Children:    children,
			})
		}
		return summaries, nil
	}

	summaries, err := materializeIndexes(forest.rootIndexes)
	if err != nil {
		return nil, false, err
	}
	return summaries, truncated, nil
}

// --- AI分析 (Agent.md §6.3) ---

const (
	defaultAIAnalysisLimit     = 100
	maxAIAnalysisLimit         = 500
	maxAIAnalysisTagIDs        = 20
	maxAITagSummaryScanNodes   = 10_000
	maxAITagSummaryOutputNodes = 500
)

type aiAnalysisCursor struct {
	Date string `json:"date"`
	ID   int64  `json:"id"`
}

// AnalyzeTransactions はAIエージェント向けの取引分析を行う
func (s *Service) AnalyzeTransactions(req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	return s.AnalyzeTransactionsContext(context.Background(), req)
}

// AnalyzeTransactionsContext はAI分析のDBクエリ、タグ集計、明細取得に
// 呼び出し元のキャンセルとdeadlineを伝播する。
func (s *Service) AnalyzeTransactionsContext(ctx context.Context, req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	where, args, err := buildAIAnalysisFilter(req)
	if err != nil {
		return nil, err
	}

	resp := &models.AnalysisResponse{}
	// #nosec G202 -- where contains only fixed predicates assembled by
	// buildAIAnalysisFilter; every request value remains a bound argument.
	aggregateQuery := `SELECT type,
		COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount)
		FROM transactions` + where + ` ORDER BY id`
	aggregateRows, err := db.QueryContext(ctx, aggregateQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("分析集計エラー: %w", err)
	}
	for aggregateRows.Next() {
		if err := ctx.Err(); err != nil {
			_ = aggregateRows.Close()
			return nil, fmt.Errorf("分析集計キャンセル: %w", err)
		}
		var txType string
		var amount int64
		if err := aggregateRows.Scan(&txType, &amount); err != nil {
			_ = aggregateRows.Close()
			return nil, fmt.Errorf("分析集計スキャンエラー: %w", err)
		}
		resp.Count++
		var addErr error
		switch txType {
		case "income":
			resp.TotalIncome, addErr = validation.CheckedAddInt64(resp.TotalIncome, amount)
		case "expense":
			resp.TotalExpense, addErr = validation.CheckedAddInt64(resp.TotalExpense, amount)
		default:
			addErr = fmt.Errorf("不明な取引種別です: %s", txType)
		}
		if addErr != nil {
			_ = aggregateRows.Close()
			return nil, fmt.Errorf("分析集計オーバーフロー: %w", addErr)
		}
	}
	if err := aggregateRows.Err(); err != nil {
		_ = aggregateRows.Close()
		return nil, fmt.Errorf("分析集計エラー: %w", err)
	}
	if err := aggregateRows.Close(); err != nil {
		return nil, fmt.Errorf("分析集計行終了エラー: %w", err)
	}
	resp.NetAmount, err = validation.CheckedSubInt64(resp.TotalIncome, resp.TotalExpense)
	if err != nil {
		return nil, fmt.Errorf("分析純額オーバーフロー: %w", err)
	}
	resp.TotalIncomeExact = models.ExactInt64(resp.TotalIncome)
	resp.TotalExpenseExact = models.ExactInt64(resp.TotalExpense)
	resp.NetAmountExact = models.ExactInt64(resp.NetAmount)

	// タグ別集計にも取引一覧と同じフィルターを適用する。
	// 全nodeのroll-up後に応答だけをbudget内へ切り詰めるため、親集計値は変わらない。
	maxMaterializedNodes := req.MaxTagSummaries
	if maxMaterializedNodes > maxAITagSummaryOutputNodes {
		maxMaterializedNodes = maxAITagSummaryOutputNodes
	}
	tagSummaries, tagSummariesTruncated, err := s.getTagSummaryFilteredContext(
		ctx,
		req.Type,
		req.StartDate,
		req.EndDate,
		req.Account,
		req.TagIDs,
		tagSummaryOptions{
			maxScannedNodes:      maxAITagSummaryScanNodes,
			maxMaterializedNodes: maxMaterializedNodes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("タグ別分析エラー: %w", err)
	}
	resp.TagSummaries = tagSummaries
	resp.TagSummariesTruncated = tagSummariesTruncated

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
	// #nosec G202 -- detailWhere contains only fixed SQL predicates selected by
	// validated filters; every request value remains a bound placeholder.
	rows, err := db.QueryContext(ctx, `SELECT id, account, datetime(date), item, type,
		COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), memo
		FROM transactions`+detailWhere+`
		ORDER BY datetime(date) DESC, id DESC
		LIMIT ?`, detailArgs...)
	if err != nil {
		return nil, fmt.Errorf("分析明細クエリエラー: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("分析明細取得キャンセル: %w", err)
		}
		var detail models.AITransactionDetail
		var memo string
		if err := rows.Scan(&detail.ID, &detail.Account, &detail.Date, &detail.Item, &detail.Type, &detail.Amount, &memo); err != nil {
			return nil, fmt.Errorf("分析明細スキャンエラー: %w", err)
		}
		detail.AmountExact = models.ExactInt64(detail.Amount)
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
		// #nosec G202 -- interpolation contains only generated "?" placeholders.
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
func (s *Service) GetTransactionLinks(transactionID int64) ([]models.LinkedTransactionResponse, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	query := `
		SELECT t.id, t.account, t.date, t.item, t.type,
			COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = t.id), t.amount), t.memo
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
		r.AmountExact = models.ExactInt64(r.Amount)
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
func (s *Service) AddTransactionLink(parentID, childID int64) error {
	if parentID == childID {
		return fmt.Errorf("同一の取引同士は紐付けできません")
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	if err := s.validateCardWithdrawalLink(parentID, childID); err != nil {
		return err
	}
	// 正規化: 小さいIDをparent_id、大きいIDをchild_idにする（重複防止）
	p, c := parentID, childID
	if p > c {
		p, c = c, p
	}
	_, err = db.Exec("INSERT OR IGNORE INTO transaction_links (parent_id, child_id) VALUES (?, ?)", p, c)
	if err != nil {
		return fmt.Errorf("紐付け追加エラー: %w", err)
	}
	s.autoSnapshot()
	return nil
}

func (s *Service) validateCardWithdrawalLink(transactionID, linkedID int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
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
	if s.isCardWithdrawalLinkAccounts(accountA, accountB) {
		return nil
	}
	return fmt.Errorf("紐付けはクレジットカード項目と銀行口座項目の取引間でのみ追加できます")
}

// loadLedgerSettingsIn reads only settings which participate in the
// card-withdrawal link policy. Other settings are deliberately opaque to this
// feature and are never exported, deleted, or interpreted here.
func loadLedgerSettingsIn(q sqlExecutor) (map[string]string, error) {
	settings := make(map[string]string, 2)
	rows, err := q.Query("SELECT key, value FROM settings WHERE key IN ('credit_card_items', 'bank_account_items')")
	if err != nil {
		return nil, fmt.Errorf("ledger設定取得エラー: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return settings, nil
}

func loadLedgerSettingsContext(ctx context.Context, q sqlContextExecutor) (map[string]string, error) {
	settings := make(map[string]string, 2)
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM settings WHERE key IN ('credit_card_items', 'bank_account_items')")
	if err != nil {
		return nil, fmt.Errorf("ledger設定取得エラー: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return settings, nil
}

func pruneInvalidTransactionLinksIn(q sqlExecutor, settings map[string]string) error {
	creditCards := stringSetFromSetting(settings["credit_card_items"])
	bankAccounts := stringSetFromSetting(settings["bank_account_items"])
	rows, err := q.Query(`
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
		if !isCardWithdrawalLinkAccountsWithSettings(parentAccount, childAccount, creditCards, bankAccounts) {
			invalidPairs = append(invalidPairs, [2]int64{parentID, childID})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("紐付け行取得エラー: %w", err)
	}

	for _, pair := range invalidPairs {
		if _, err := q.Exec("DELETE FROM transaction_links WHERE parent_id = ? AND child_id = ?", pair[0], pair[1]); err != nil {
			return fmt.Errorf("不正な紐付け削除エラー: %w", err)
		}
	}
	return nil
}

func pruneInvalidTransactionLinksContext(ctx context.Context, q sqlContextExecutor, settings map[string]string) error {
	creditCards := stringSetFromSetting(settings["credit_card_items"])
	bankAccounts := stringSetFromSetting(settings["bank_account_items"])
	rows, err := q.QueryContext(ctx, `
		SELECT l.parent_id, p.account, l.child_id, c.account
		FROM transaction_links l
		JOIN transactions p ON p.id = l.parent_id
		JOIN transactions c ON c.id = l.child_id`)
	if err != nil {
		return fmt.Errorf("紐付け取得エラー: %w", err)
	}
	defer rows.Close()
	var invalidPairs [][2]int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var parentID, childID int64
		var parentAccount, childAccount string
		if err := rows.Scan(&parentID, &parentAccount, &childID, &childAccount); err != nil {
			return fmt.Errorf("紐付けスキャンエラー: %w", err)
		}
		if !isCardWithdrawalLinkAccountsWithSettings(parentAccount, childAccount, creditCards, bankAccounts) {
			invalidPairs = append(invalidPairs, [2]int64{parentID, childID})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, pair := range invalidPairs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM transaction_links WHERE parent_id = ? AND child_id = ?", pair[0], pair[1]); err != nil {
			return fmt.Errorf("不正な紐付け削除エラー: %w", err)
		}
	}
	return nil
}

func (s *Service) isCardWithdrawalLinkAccounts(accountA, accountB string) bool {
	db, err := s.database()
	if err != nil {
		return false
	}
	settings, err := loadLedgerSettingsIn(db)
	if err != nil {
		return false
	}
	return isCardWithdrawalLinkAccountsWithSettings(accountA, accountB,
		stringSetFromSetting(settings["credit_card_items"]),
		stringSetFromSetting(settings["bank_account_items"]))
}

func isCardWithdrawalLinkAccountsWithSettings(accountA, accountB string, creditCards, bankAccounts map[string]bool) bool {
	// Settings written by older clients may contain surrounding whitespace.
	// Keep the stored archive value byte-for-byte, but use the historical
	// canonical account form consistently for link policy decisions.
	accountA = strings.TrimSpace(accountA)
	accountB = strings.TrimSpace(accountB)
	return (creditCards[accountA] && bankAccounts[accountB]) || (bankAccounts[accountA] && creditCards[accountB])
}

func stringSetFromSetting(value string) map[string]bool {
	parsed, err := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingArchive, maxCSVSettingValueBytes, validation.MaxSettingItems)
	if err != nil {
		return map[string]bool{}
	}
	return stringSet(parsed.Items)
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		if canonical := strings.TrimSpace(item); canonical != "" {
			set[canonical] = true
		}
	}
	return set
}

// RemoveTransactionLink は取引の紐付けを解除する
func (s *Service) RemoveTransactionLink(transactionID, linkedID int64) error {
	db, err := s.database()
	if err != nil {
		return err
	}
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
	s.autoSnapshot()
	return nil
}
