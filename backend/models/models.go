// Package models はデータベースの構造定義（ORMモデル）を提供する
package models

import (
	"strconv"
	"time"
)

const (
	// MaxImageBytes は画像1件のデコード後バイト数上限（5 MiB）。
	MaxImageBytes int64 = 5 * 1024 * 1024
	// MaxImagePixels は画像1件の最大画素数。デコード前に検査する。
	MaxImagePixels int64 = 20_000_000
	// MaxImagesPerTransaction は1取引に保存できる画像数。
	MaxImagesPerTransaction = 10
	// MaxImageBytesPerTransaction は1取引の画像データ合計上限（20 MiB）。
	MaxImageBytesPerTransaction int64 = 20 * 1024 * 1024
	// MaxImageBytesPerAccount は同名口座に保存できる画像データ合計上限（128 MiB）。
	MaxImageBytesPerAccount int64 = 128 * 1024 * 1024
	// MaxImageBytesDatabase はDB全体に保存できる画像データ合計上限（256 MiB）。
	MaxImageBytesDatabase int64 = 256 * 1024 * 1024
	// Archive-only bounds preserve pre-quota images without exposing an
	// unbounded multi-user parsing/storage surface.
	MaxArchivedImageBytes           int64 = 8 * 1024 * 1024
	MaxArchivedImagesPerTransaction       = 1_000
	MaxArchivedImagesDatabase             = 100_000
	MaxArchivedImageMetadataBytes         = 4 * 1024
)

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

// LinkedTransactionResponse は紐付け取引のレスポンス（簡易情報）
type LinkedTransactionResponse struct {
	ID          int64  `json:"id"`
	FundItem    string `json:"fundItem"`
	Date        string `json:"date"`
	Item        string `json:"item"`
	Type        string `json:"type"`
	Amount      int64  `json:"amount"`
	AmountExact string `json:"amount_exact"`
	Memo        string `json:"memo"`
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
	Invalid   bool   `json:"invalid,omitempty"`  // 旧DB内の不正画像はデータを返さず削除だけ許可
}

// TransactionImagePage bounds image materialization for server and Desktop
// clients. Cursor is an opaque decimal offset retained only for compatibility;
// callers must pass it back unchanged.
type TransactionImagePage struct {
	Images     []TransactionImageResponse `json:"images"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

// AccountImageStorageUsage は口座単位の画像保存量を表す。
type AccountImageStorageUsage struct {
	Account    string `json:"account"`
	ImageCount int64  `json:"image_count"`
	Bytes      int64  `json:"bytes"`
}

// ImageStorageUsage は画像保存量と強制中のクォータを返す監視用レスポンス。
type ImageStorageUsage struct {
	ImageCount              int64                      `json:"image_count"`
	Bytes                   int64                      `json:"bytes"`
	MaxImageBytes           int64                      `json:"max_image_bytes"`
	MaxImagePixels          int64                      `json:"max_image_pixels"`
	MaxImagesPerTransaction int                        `json:"max_images_per_transaction"`
	MaxBytesPerTransaction  int64                      `json:"max_bytes_per_transaction"`
	MaxBytesPerAccount      int64                      `json:"max_bytes_per_account"`
	MaxBytesDatabase        int64                      `json:"max_bytes_database"`
	Accounts                []AccountImageStorageUsage `json:"accounts"`
}

// Tag はタグの構造体（Agent.md §6.6: 3階層タグシステム）
type Tag struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"` // NULLの場合はトップレベル
	Level    int    `json:"level"`     // 1: タグ, 2: サブタグ, 3: サブサブタグ
	Children []Tag  `json:"children,omitempty"`
}

// TagDeleteImpact describes the rows that a cascading tag delete will affect.
// Counts include the selected tag's transaction links and exclude the tag
// itself from DescendantCount.
type TagDeleteImpact struct {
	TagID            int64  `json:"tag_id"`
	TagName          string `json:"tag_name"`
	DescendantCount  int64  `json:"descendant_count"`
	TransactionCount int64  `json:"transaction_count"`
}

// TagSummary はタグ別集計データ（円グラフ用）
type TagSummary struct {
	TagID       int64        `json:"tag_id"`
	TagName     string       `json:"tag_name"`
	Amount      int64        `json:"amount"`
	AmountExact string       `json:"amount_exact"`
	Count       int          `json:"count"`
	Ratio       float64      `json:"ratio"` // 割合（0.0〜1.0）
	Children    []TagSummary `json:"children,omitempty"`
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
	ID           int64                      `json:"id"`
	FundItem     string                     `json:"fundItem"`
	Account      string                     `json:"account"`
	Date         string                     `json:"date"`
	Item         string                     `json:"item"`
	Type         string                     `json:"type"`
	Amount       int64                      `json:"amount"`
	AmountExact  string                     `json:"amount_exact"`
	Balance      int64                      `json:"balance"`
	BalanceExact string                     `json:"balance_exact"`
	Memo         string                     `json:"memo"`
	Images       []TransactionImageResponse `json:"images,omitempty"`
	Tags         []Tag                      `json:"tags,omitempty"`
}

// BalanceHistoryResponse は残高推移データのレスポンス
type BalanceHistoryResponse struct {
	Accounts      []string            `json:"accounts"`
	Dates         []string            `json:"dates"`
	Balances      map[string][]int64  `json:"balances"`
	BalancesExact map[string][]string `json:"balances_exact"`
}

// AnalysisRequest はAI分析リクエストの構造体（Agent.md §6.3）
type AnalysisRequest struct {
	StartDate           string  `json:"start_date,omitempty"` // YYYY-MM-DD
	EndDate             string  `json:"end_date,omitempty"`   // YYYY-MM-DD
	Account             string  `json:"account,omitempty"`
	TagIDs              []int64 `json:"tag_ids,omitempty"`
	Type                string  `json:"type,omitempty"` // "income" or "expense" or ""（両方）
	IncludeTransactions bool    `json:"include_transactions,omitempty"`
	IncludeMemo         bool    `json:"include_memo,omitempty"`
	Limit               int     `json:"limit,omitempty"`
	Cursor              string  `json:"cursor,omitempty"`
	MaxTagSummaries     int     `json:"-"` // API credential boundary; never accepted from JSON.
}

// AITransactionDetail は明示権限とpagination付きでのみ返す最小明細。
type AITransactionDetail struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	Date        string `json:"date"`
	Item        string `json:"item"`
	Type        string `json:"type"`
	Amount      int64  `json:"amount"`
	AmountExact string `json:"amount_exact"`
	Memo        string `json:"memo,omitempty"`
}

// AnalysisResponse はAI分析レスポンスの構造体
type AnalysisResponse struct {
	TotalIncome           int64                 `json:"total_income"`
	TotalIncomeExact      string                `json:"total_income_exact"`
	TotalExpense          int64                 `json:"total_expense"`
	TotalExpenseExact     string                `json:"total_expense_exact"`
	NetAmount             int64                 `json:"net_amount"`
	NetAmountExact        string                `json:"net_amount_exact"`
	Count                 int                   `json:"count"`
	TagSummaries          []TagSummary          `json:"tag_summaries,omitempty"`
	TagSummariesTruncated bool                  `json:"tag_summaries_truncated,omitempty"`
	Transactions          []AITransactionDetail `json:"transactions,omitempty"`
	ReturnedCount         int                   `json:"returned_count"`
	NextCursor            string                `json:"next_cursor,omitempty"`
}

// ToResponse はTransactionをTransactionResponseに変換する
func (t *Transaction) ToResponse() TransactionResponse {
	dateStr := t.Date.Format("2006-01-02 15:04:05")
	// 時刻が00:00:00の場合は日付のみ
	if t.Date.Hour() == 0 && t.Date.Minute() == 0 && t.Date.Second() == 0 {
		dateStr = t.Date.Format("2006-01-02")
	}

	return TransactionResponse{
		ID:           t.ID,
		FundItem:     t.Account,
		Account:      t.Account,
		Date:         dateStr,
		Item:         t.Item,
		Type:         t.Type,
		Amount:       t.Amount,
		AmountExact:  strconv.FormatInt(t.Amount, 10),
		Balance:      t.Balance,
		BalanceExact: strconv.FormatInt(t.Balance, 10),
		Memo:         t.Memo,
	}
}

// ExactInt64 returns the lossless decimal JSON companion for JavaScript/Wails
// consumers while the existing numeric field remains for older clients.
func ExactInt64(value int64) string { return strconv.FormatInt(value, 10) }
