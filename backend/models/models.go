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
	Memo    string    `json:"memo"`
}

// TransactionLink は取引紐付け情報の構造体
type TransactionLink struct {
	ParentID int64 `json:"parent_id"`
	ChildID  int64 `json:"child_id"`
}

// TransactionImage は取引画像の構造体（Agent.md §6.5）
type TransactionImage struct {
	ID            int64     `json:"id"`
	TransactionID int64     `json:"transaction_id"`
	Filename      string    `json:"filename"`
	Data          []byte    `json:"-"` // JSONには含めない（大きいため）
	MimeType      string    `json:"mime_type"`
	CreatedAt     time.Time `json:"created_at"`
}

// TransactionImageRequest は画像アップロードリクエストの構造体
type TransactionImageRequest struct {
	Filename string `json:"filename"`
	Data     string `json:"data"`      // Base64エンコードされた画像データ
	MimeType string `json:"mime_type"` // 省略時はファイル名から推定
}

// TransactionImageResponse は画像データのレスポンス構造体
type TransactionImageResponse struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	CreatedAt string `json:"created_at"`
	DataURL   string `json:"data_url,omitempty"` // data:mime;base64,... の形式
}

// Tag はタグの構造体（Agent.md §6.6: 3階層タグシステム）
type Tag struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"` // NULLの場合はトップレベル
	Level    int    `json:"level"`     // 1: タグ, 2: サブタグ, 3: サブサブタグ
	Children []Tag  `json:"children,omitempty"`
}

// TagSummary はタグ別集計データ（円グラフ用）
type TagSummary struct {
	TagID    int64        `json:"tag_id"`
	TagName  string       `json:"tag_name"`
	Amount   int64        `json:"amount"`
	Count    int          `json:"count"`
	Ratio    float64      `json:"ratio"` // 割合（0.0〜1.0）
	Children []TagSummary `json:"children,omitempty"`
}

// Setting は設定情報の構造体（キー・バリュー形式）
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TransactionRequest は取引追加・更新リクエストの構造体
type TransactionRequest struct {
	Account string                    `json:"account"`
	Date    string                    `json:"date"`
	Time    string                    `json:"time"`
	Item    string                    `json:"item"`
	Type    string                    `json:"type"`
	Amount  int64                     `json:"amount"`
	Memo    string                    `json:"memo"`
	Images  []TransactionImageRequest `json:"images,omitempty"` // 画像添付（Base64）
	Tags    []int64                   `json:"tags,omitempty"`   // タグID一覧
}

// TransactionResponse はフロントエンドに返す取引データ
type TransactionResponse struct {
	ID       int64                      `json:"id"`
	FundItem string                     `json:"fundItem"`
	Account  string                     `json:"account"`
	Date     string                     `json:"date"`
	Item     string                     `json:"item"`
	Type     string                     `json:"type"`
	Amount   int64                      `json:"amount"`
	Balance  int64                      `json:"balance"`
	Memo     string                     `json:"memo"`
	Images   []TransactionImageResponse `json:"images,omitempty"`
	Tags     []Tag                      `json:"tags,omitempty"`
}

// BalanceHistoryResponse は残高推移データのレスポンス
type BalanceHistoryResponse struct {
	Accounts []string           `json:"accounts"`
	Dates    []string           `json:"dates"`
	Balances map[string][]int64 `json:"balances"`
}

// AnalysisRequest はAI分析リクエストの構造体（Agent.md §6.3）
type AnalysisRequest struct {
	StartDate string  `json:"start_date,omitempty"` // YYYY-MM-DD
	EndDate   string  `json:"end_date,omitempty"`   // YYYY-MM-DD
	Account   string  `json:"account,omitempty"`
	TagIDs    []int64 `json:"tag_ids,omitempty"`
	Type      string  `json:"type,omitempty"` // "income" or "expense" or ""（両方）
}

// AnalysisResponse はAI分析レスポンスの構造体
type AnalysisResponse struct {
	TotalIncome  int64                 `json:"total_income"`
	TotalExpense int64                 `json:"total_expense"`
	NetAmount    int64                 `json:"net_amount"`
	Count        int                   `json:"count"`
	TagSummaries []TagSummary          `json:"tag_summaries,omitempty"`
	Transactions []TransactionResponse `json:"transactions,omitempty"`
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
