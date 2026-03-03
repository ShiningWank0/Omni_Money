// Package models はデータベースの構造定義（ORMモデル）を提供する
package models

import "time"

// Transaction は取引データの構造体
type Transaction struct {
	ID      int64     `json:"id"`
	Account string    `json:"account"`
	Date    time.Time `json:"date"`
	Item    string    `json:"item"`
	Type    string    `json:"type"` // "income" or "expense"
	Amount  int64     `json:"amount"`
	Balance int64     `json:"balance"`
	Memo    string    `json:"memo"` // 新規追加
}

// TransactionLink は取引紐付け情報の構造体
type TransactionLink struct {
	ParentID int64 `json:"parent_id"`
	ChildID  int64 `json:"child_id"`
}

// Setting は設定情報の構造体（キー・バリュー形式）
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TransactionRequest は取引追加・更新リクエストの構造体
type TransactionRequest struct {
	Account string `json:"account"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	Item    string `json:"item"`
	Type    string `json:"type"`
	Amount  int64  `json:"amount"`
	Memo    string `json:"memo"`
}

// TransactionResponse はフロントエンドに返す取引データ
type TransactionResponse struct {
	ID       int64  `json:"id"`
	FundItem string `json:"fundItem"`
	Account  string `json:"account"`
	Date     string `json:"date"`
	Item     string `json:"item"`
	Type     string `json:"type"`
	Amount   int64  `json:"amount"`
	Balance  int64  `json:"balance"`
	Memo     string `json:"memo"`
}

// BalanceHistoryResponse は残高推移データのレスポンス
type BalanceHistoryResponse struct {
	Accounts []string           `json:"accounts"`
	Dates    []string           `json:"dates"`
	Balances map[string][]int64 `json:"balances"`
}

// ToResponse はTransactionをTransactionResponseに変換する
func (t *Transaction) ToResponse() TransactionResponse {
	dateStr := t.Date.Format("2006-01-02 15:04:05")
	// 時刻が00:00:00の場合は日付のみ
	if t.Date.Hour() == 0 && t.Date.Minute() == 0 && t.Date.Second() == 0 {
		dateStr = t.Date.Format("2006-01-02")
	}

	return TransactionResponse{
		ID:       t.ID,
		FundItem: t.Account,
		Account:  t.Account,
		Date:     dateStr,
		Item:     t.Item,
		Type:     t.Type,
		Amount:   t.Amount,
		Balance:  t.Balance,
		Memo:     t.Memo,
	}
}
