package core

// This file implements the normalized, lossless CSV v3 format.  CSV remains
// the transport (so it can be archived and inspected with ordinary tools), but
// each row is explicitly typed.  That avoids duplicating transactions when a
// transaction has several images/tags and lets import rebuild all foreign-key
// relationships after SQLite assigns new IDs.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/models"
	"omni_money/backend/validation"
)

const (
	// MaxCSVImportBytes bounds the complete UTF-8 CSV payload before parsing.
	// A 384 MiB wire payload leaves headroom under the server's 1 GiB memory
	// budget for the decoded database image quota and bounded parsed metadata.
	MaxCSVImportBytes int64 = 384 * 1024 * 1024
	// MaxCSVImportWireBytes is the fixed JSON request wire cap. It deliberately
	// is not a claim about worst-case JSON escaping expansion.
	MaxCSVImportWireBytes   int64 = MaxCSVImportBytes + 8*1024*1024
	maxCSVExportBytes       int64 = MaxCSVImportBytes
	maxCSVRows                    = 1_000_000
	maxCSVFieldBytes              = 8 * 1024 * 1024
	maxCSVSettingKeyBytes         = 256
	maxCSVSettingValueBytes       = 2 * 1024 * 1024
	maxCSVTagNameBytes            = 255
	maxCSVSettingItemBytes        = 255
	maxCSVAccountBytes            = 256
	maxCSVItemBytes               = 512
	maxCSVMemoBytes               = 4096
	maxCSVParsedTextBytes   int64 = 64 * 1024 * 1024
)

// Keep direct/Desktop callers to one full CSV parse at a time. HTTP requests
// have a separate gate in middleware because their JSON body is allocated
// before the service is invoked.
var csvImportSlots = make(chan struct{}, 1)

type csvImportReservationContextKey struct{}

// TryAcquireCSVImportSlot reserves the single process-wide full-import slot.
// HTTP middleware uses this before JSON decoding; direct/Desktop callers use
// Service.ImportCSV, which acquires the same slot around the whole operation.
func TryAcquireCSVImportSlot() (func(), bool) {
	select {
	case csvImportSlots <- struct{}{}:
		return func() { <-csvImportSlots }, true
	default:
		return nil, false
	}
}

// WithCSVImportReservation marks an HTTP request whose body is admitted under
// TryAcquireCSVImportSlot. It prevents the service handler from acquiring the
// same non-reentrant slot a second time.
func WithCSVImportReservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, csvImportReservationContextKey{}, true)
}

func HasCSVImportReservation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	reserved, _ := ctx.Value(csvImportReservationContextKey{}).(bool)
	return reserved
}

var csvV3Headers = []string{
	csvVersionHeader, "record_type", "id", "transaction_id", "parent_id", "child_id", "tag_id",
	"account", "date", "item", "type", "amount", "balance", "memo", "filename", "mime_type",
	"data_base64", "tag_name", "tag_parent_id", "tag_level", "setting_key", "setting_value", "created_at",
}

type csvLimitedStringWriter struct {
	b     strings.Builder
	limit int64
}

func (w *csvLimitedStringWriter) Write(p []byte) (int, error) {
	if int64(w.b.Len()+len(p)) > w.limit {
		return 0, fmt.Errorf("CSV出力が上限%d bytesを超えました", w.limit)
	}
	return w.b.Write(p)
}

func (w *csvLimitedStringWriter) String() string { return w.b.String() }

func (s *Service) hasCSVExtendedData() (bool, error) {
	db, err := s.database()
	if err != nil {
		return false, err
	}
	var count int64
	err = db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM transaction_images) +
		(SELECT COUNT(*) FROM tags) +
		(SELECT COUNT(*) FROM transaction_tags) +
		(SELECT COUNT(*) FROM transaction_links) +
		(SELECT COUNT(*) FROM settings)`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("CSV拡張データ確認エラー: %w", err)
	}
	return count > 0, nil
}

func csvV3Record(values map[string]string) []string {
	record := make([]string, len(csvV3Headers))
	for i, header := range csvV3Headers {
		record[i] = values[header]
	}
	return record
}

func csvV3Text(value string) string { return encodeCSVTextCell(value) }

func validateCSVV3TagName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("タグ名は前後の空白を除いた空でない値にしてください")
	}
	if !utf8.ValidString(name) || len([]byte(name)) > maxCSVTagNameBytes {
		return fmt.Errorf("タグ名はUTF-8で%dバイト以内にしてください", maxCSVTagNameBytes)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("タグ名にパス区切り文字を含めることはできません")
	}
	if strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	}) {
		return fmt.Errorf("タグ名に制御文字を含めることはできません")
	}
	return nil
}

func validateCSVV3Setting(key, value string) error {
	if key != "credit_card_items" && key != "bank_account_items" {
		return fmt.Errorf("未対応のledger設定キーです: %s", key)
	}
	if len([]byte(key)) == 0 || len([]byte(key)) > maxCSVSettingKeyBytes || !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl) {
		return fmt.Errorf("設定キーが不正です")
	}
	if len([]byte(value)) > maxCSVSettingValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("設定値が大きすぎるかUTF-8ではありません")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed[0] != '[' {
		return fmt.Errorf("設定値は文字列配列JSONで指定してください")
	}
	var items []string
	decoder := json.NewDecoder(strings.NewReader(value))
	if err := decoder.Decode(&items); err != nil {
		return fmt.Errorf("設定値は文字列配列JSONで指定してください")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("設定値JSONに余分なデータがあります")
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == "" || len([]byte(item)) > maxCSVSettingItemBytes || !utf8.ValidString(item) || strings.ContainsFunc(item, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) {
			return fmt.Errorf("設定値の項目が不正です")
		}
		if _, ok := seen[item]; ok {
			return fmt.Errorf("設定値の項目が重複しています")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func validateCSVV3CreatedAt(value string) error {
	if value == "" {
		return nil
	}
	if _, err := parseDateStrict(value); err != nil {
		return fmt.Errorf("created_atの日時形式が不正です")
	}
	return nil
}

func (s *Service) backupToCSVV3() (string, error) {
	db, err := s.database()
	if err != nil {
		return "", err
	}
	output := &csvLimitedStringWriter{limit: maxCSVExportBytes}
	writer := csv.NewWriter(output)
	if err := writer.Write(csvV3Headers); err != nil {
		return "", fmt.Errorf("CSV v3ヘッダー書き出しエラー: %w", err)
	}
	write := func(values map[string]string) error {
		if err := writer.Write(csvV3Record(values)); err != nil {
			return fmt.Errorf("CSV v3行書き出しエラー: %w", err)
		}
		return nil
	}

	rows, err := db.Query(`SELECT id, account, date, item, type, amount, balance, memo
		FROM transactions ORDER BY date, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3取引取得エラー: %w", err)
	}
	for rows.Next() {
		var id, amount, balance int64
		var account, date, item, txType, memo string
		if err := rows.Scan(&id, &account, &date, &item, &txType, &amount, &balance, &memo); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3取引スキャンエラー: %w", err)
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "transaction", "id": strconv.FormatInt(id, 10),
			"account": csvV3Text(account), "date": csvV3Text(date), "item": csvV3Text(item),
			"type": csvV3Text(txType), "amount": strconv.FormatInt(amount, 10),
			"balance": strconv.FormatInt(balance, 10), "memo": csvV3Text(memo),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3取引取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3取引クローズエラー: %w", err)
	}

	rows, err = db.Query(`SELECT id, transaction_id, filename, mime_type, data, created_at
		FROM transaction_images ORDER BY transaction_id, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3画像取得エラー: %w", err)
	}
	var exportedImageBytes int64
	for rows.Next() {
		var id, transactionID int64
		var filename, mimeType, createdAt string
		var data []byte
		if err := rows.Scan(&id, &transactionID, &filename, &mimeType, &data, &createdAt); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像スキャンエラー: %w", err)
		}
		if err := validateCSVV3CreatedAt(createdAt); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像created_atが不正です (id %d): %w", id, err)
		}
		prepared, err := prepareDecodedTransactionImageContext(context.Background(), filename, mimeType, data)
		if err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像が不正です (id %d): %w", id, err)
		}
		if int64(len(prepared.data)) > models.MaxImageBytesDatabase-exportedImageBytes {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像の合計サイズが上限を超えました")
		}
		exportedImageBytes += int64(len(prepared.data))
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "image", "id": strconv.FormatInt(id, 10),
			"transaction_id": strconv.FormatInt(transactionID, 10), "filename": csvV3Text(prepared.filename),
			"mime_type": csvV3Text(prepared.mimeType), "data_base64": base64.StdEncoding.EncodeToString(prepared.data),
			"created_at": csvV3Text(createdAt),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3画像取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3画像クローズエラー: %w", err)
	}

	rows, err = db.Query(`SELECT id, name, parent_id, level FROM tags ORDER BY level, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3タグ取得エラー: %w", err)
	}
	for rows.Next() {
		var id, level int64
		var name string
		var parentID sql.NullInt64
		if err := rows.Scan(&id, &name, &parentID, &level); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3タグスキャンエラー: %w", err)
		}
		parent := ""
		if parentID.Valid {
			parent = strconv.FormatInt(parentID.Int64, 10)
		}
		if err := validateCSVV3TagName(name); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3タグ名が不正です (id %d): %w", id, err)
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "tag", "id": strconv.FormatInt(id, 10),
			"tag_name": csvV3Text(name), "tag_parent_id": parent, "tag_level": strconv.FormatInt(level, 10),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3タグ取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3タグクローズエラー: %w", err)
	}

	rows, err = db.Query(`SELECT transaction_id, tag_id FROM transaction_tags ORDER BY transaction_id, tag_id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3タグ紐付け取得エラー: %w", err)
	}
	for rows.Next() {
		var transactionID, tagID int64
		if err := rows.Scan(&transactionID, &tagID); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3タグ紐付けスキャンエラー: %w", err)
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "transaction_tag",
			"transaction_id": strconv.FormatInt(transactionID, 10), "tag_id": strconv.FormatInt(tagID, 10),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3タグ紐付け取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3タグ紐付けクローズエラー: %w", err)
	}

	rows, err = db.Query(`SELECT parent_id, child_id FROM transaction_links ORDER BY parent_id, child_id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3取引リンク取得エラー: %w", err)
	}
	for rows.Next() {
		var parentID, childID int64
		if err := rows.Scan(&parentID, &childID); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3取引リンクスキャンエラー: %w", err)
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "transaction_link",
			"parent_id": strconv.FormatInt(parentID, 10), "child_id": strconv.FormatInt(childID, 10),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3取引リンク取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3取引リンククローズエラー: %w", err)
	}

	rows, err = db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return "", fmt.Errorf("CSV v3設定取得エラー: %w", err)
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3設定スキャンエラー: %w", err)
		}
		if err := validateCSVV3Setting(key, value); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3設定が不正です (%s): %w", key, err)
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "setting",
			"setting_key": csvV3Text(key), "setting_value": csvV3Text(value),
		}); err != nil {
			_ = rows.Close()
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3設定取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3設定クローズエラー: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("CSV v3書き出しエラー: %w", err)
	}
	return output.String(), nil
}

type csvV3Import struct {
	transactions      []csvV3Transaction
	images            []csvV3Image
	tags              []csvV3Tag
	tagLinks          [][2]int64
	transactionLinks  [][2]int64
	settings          map[string]string
	decodedImageBytes int64
	parsedTextBytes   int64
}

type csvV3Transaction struct {
	id                                int64
	account, date, item, txType, memo string
	amount, balance                   int64
}

type csvV3Image struct {
	id, transactionID             int64
	filename, mimeType, createdAt string
	data                          []byte
}

type csvV3Tag struct {
	id, parentID int64
	name         string
	level        int
}

func csvV3HeaderMap(headers []string) (map[string]int, error) {
	m := make(map[string]int, len(headers))
	for i, header := range headers {
		header = strings.TrimSpace(header)
		if header == "" {
			return nil, fmt.Errorf("CSV v3ヘッダーが空です")
		}
		if _, exists := m[header]; exists {
			return nil, fmt.Errorf("CSV v3ヘッダーが重複しています: %s", header)
		}
		m[header] = i
	}
	for _, required := range []string{csvVersionHeader, "record_type"} {
		if _, ok := m[required]; !ok {
			return nil, fmt.Errorf("CSV v3必須ヘッダーが不足しています: %s", required)
		}
	}
	known := make(map[string]struct{}, len(csvV3Headers))
	for _, header := range csvV3Headers {
		known[header] = struct{}{}
	}
	for header := range m {
		if _, ok := known[header]; !ok {
			return nil, fmt.Errorf("CSV v3未対応ヘッダーです: %s", header)
		}
	}
	return m, nil
}

func csvV3Get(record []string, headers map[string]int, name string) (string, error) {
	idx, ok := headers[name]
	if !ok {
		return "", nil
	}
	if idx >= len(record) {
		return "", fmt.Errorf("%s列が不足しています", name)
	}
	if len(record[idx]) > maxCSVFieldBytes {
		return "", fmt.Errorf("%s列が大きすぎます", name)
	}
	return record[idx], nil
}

func csvV3DecodedText(record []string, headers map[string]int, name string, required bool) (string, error) {
	raw, err := csvV3Get(record, headers, name)
	if err != nil {
		return "", err
	}
	if _, ok := headers[name]; !ok {
		if required {
			return "", fmt.Errorf("%s列が不足しています", name)
		}
		return "", nil
	}
	decoded, err := decodeCSVTextCellV2(raw)
	if err != nil {
		return "", fmt.Errorf("%s列のCSVエスケープが不正です: %w", name, err)
	}
	return decoded, nil
}

func validateCSVV3TextSize(label, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%sは必須です", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%sはUTF-8で指定してください", label)
	}
	if len([]byte(value)) > maxBytes {
		return fmt.Errorf("%sは%dバイト以内にしてください", label, maxBytes)
	}
	return nil
}

func (p *csvV3Import) addParsedText(values ...string) error {
	var additional int64
	for _, value := range values {
		additional += int64(len([]byte(value)))
	}
	if additional > maxCSVParsedTextBytes-p.parsedTextBytes {
		return fmt.Errorf("CSV v3の解析済みテキスト合計が上限を超えました")
	}
	p.parsedTextBytes += additional
	return nil
}

func csvV3Int(record []string, headers map[string]int, name string, required bool, positive bool) (int64, error) {
	raw, err := csvV3Get(record, headers, name)
	if err != nil {
		return 0, err
	}
	if _, ok := headers[name]; !ok {
		if required {
			return 0, fmt.Errorf("%s列が不足しています", name)
		}
		return 0, nil
	}
	if strings.TrimSpace(raw) == "" {
		if required {
			return 0, fmt.Errorf("%s列が空です", name)
		}
		return 0, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || (positive && value <= 0) {
		return 0, fmt.Errorf("%s列が不正です", name)
	}
	return value, nil
}

func (s *Service) parseCSVV3(content string) (csvV3Import, error) {
	if int64(len(content)) > MaxCSVImportBytes {
		return csvV3Import{}, fmt.Errorf("CSV入力が上限%d bytesを超えました", MaxCSVImportBytes)
	}
	reader := csv.NewReader(strings.NewReader(content))
	reader.FieldsPerRecord = -1
	headers, err := reader.Read()
	if err != nil {
		return csvV3Import{}, fmt.Errorf("CSV v3ヘッダー読み取りエラー: %w", err)
	}
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
	}
	headerMap, err := csvV3HeaderMap(headers)
	if err != nil {
		return csvV3Import{}, err
	}
	parsed := csvV3Import{settings: make(map[string]string)}
	transactionIDs := make(map[int64]struct{})
	tagIDs := make(map[int64]struct{})
	tagsByID := make(map[int64]csvV3Tag)
	tagNames := make(map[string]struct{})
	imageIDs := make(map[int64]struct{})
	seenTagLinks := make(map[[2]int64]struct{})
	seenTransactionLinks := make(map[[2]int64]struct{})
	rowNumber := 1
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		rowNumber++
		if readErr != nil {
			return csvV3Import{}, fmt.Errorf("CSV v3行読み取りエラー (行%d): %w", rowNumber, readErr)
		}
		if len(record) != len(headers) {
			return csvV3Import{}, fmt.Errorf("CSV v3列数がヘッダーと一致しません (行%d)", rowNumber)
		}
		if rowNumber > maxCSVRows+1 {
			return csvV3Import{}, fmt.Errorf("CSV v3行数が上限%dを超えました", maxCSVRows)
		}
		version, err := csvV3Get(record, headerMap, csvVersionHeader)
		if err != nil {
			return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
		}
		if version != csvVersion3 {
			return csvV3Import{}, fmt.Errorf("未対応のCSV v3バージョンです (行%d): %q", rowNumber, version)
		}
		recordType, err := csvV3DecodedText(record, headerMap, "record_type", true)
		if err != nil {
			return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
		}
		if recordType == "" {
			return csvV3Import{}, fmt.Errorf("record_typeが空です (行%d)", rowNumber)
		}
		switch recordType {
		case "transaction":
			id, err := csvV3Int(record, headerMap, "id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("取引id (行%d): %w", rowNumber, err)
			}
			if _, exists := transactionIDs[id]; exists {
				return csvV3Import{}, fmt.Errorf("取引idが重複しています (行%d): %d", rowNumber, id)
			}
			transactionIDs[id] = struct{}{}
			account, err := csvV3DecodedText(record, headerMap, "account", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			date, err := csvV3DecodedText(record, headerMap, "date", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			item, err := csvV3DecodedText(record, headerMap, "item", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			txType, err := csvV3DecodedText(record, headerMap, "type", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			memo, err := csvV3DecodedText(record, headerMap, "memo", false)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			account = strings.TrimSpace(account)
			item = strings.TrimSpace(item)
			date = strings.TrimSpace(date)
			txType = strings.ToLower(strings.TrimSpace(txType))
			if account == "" {
				return csvV3Import{}, fmt.Errorf("口座名は必須です (行%d)", rowNumber)
			}
			if item == "" {
				return csvV3Import{}, fmt.Errorf("項目は必須です (行%d)", rowNumber)
			}
			if err := validateCSVV3TextSize("口座名", account, maxCSVAccountBytes, true); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := validateCSVV3TextSize("項目", item, maxCSVItemBytes, true); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := validateCSVV3TextSize("メモ", memo, maxCSVMemoBytes, false); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := parsed.addParsedText(account, date, item, txType, memo); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if txType != "income" && txType != "expense" {
				return csvV3Import{}, fmt.Errorf("種別はincomeまたはexpenseである必要があります (行%d)", rowNumber)
			}
			_, err = parseDateStrict(date)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("日付形式が正しくありません (行%d): %w", rowNumber, err)
			}
			amount, err := csvV3Int(record, headerMap, "amount", true, true)
			if err != nil || validation.ValidateTransactionAmount(amount) != nil {
				if err == nil {
					err = validation.ValidateTransactionAmount(amount)
				}
				return csvV3Import{}, fmt.Errorf("金額が不正です (行%d): %w", rowNumber, err)
			}
			balance, err := csvV3Int(record, headerMap, "balance", false, false)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("残高 (行%d): %w", rowNumber, err)
			}
			parsed.transactions = append(parsed.transactions, csvV3Transaction{id: id, account: account, date: date, item: item, txType: txType, amount: amount, memo: memo, balance: balance})
		case "image":
			id, err := csvV3Int(record, headerMap, "id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("画像id (行%d): %w", rowNumber, err)
			}
			if _, exists := imageIDs[id]; exists {
				return csvV3Import{}, fmt.Errorf("画像idが重複しています (行%d): %d", rowNumber, id)
			}
			imageIDs[id] = struct{}{}
			txID, err := csvV3Int(record, headerMap, "transaction_id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("画像transaction_id (行%d): %w", rowNumber, err)
			}
			filename, err := csvV3DecodedText(record, headerMap, "filename", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			mimeType, err := csvV3DecodedText(record, headerMap, "mime_type", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			encoded, err := csvV3Get(record, headerMap, "data_base64")
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if encoded == "" || strings.ContainsFunc(encoded, unicode.IsSpace) {
				return csvV3Import{}, fmt.Errorf("画像Base64が不正です (行%d)", rowNumber)
			}
			if len(encoded) > base64.StdEncoding.EncodedLen(int(models.MaxImageBytes)) {
				return csvV3Import{}, fmt.Errorf("画像Base64が大きすぎます (行%d)", rowNumber)
			}
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || len(data) == 0 {
				return csvV3Import{}, fmt.Errorf("画像Base64が不正です (行%d)", rowNumber)
			}
			if int64(len(data)) > models.MaxImageBytesDatabase-parsed.decodedImageBytes {
				return csvV3Import{}, fmt.Errorf("CSV v3画像の合計サイズが上限を超えました (行%d)", rowNumber)
			}
			createdAt, err := csvV3DecodedText(record, headerMap, "created_at", false)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := validateCSVV3CreatedAt(createdAt); err != nil {
				return csvV3Import{}, fmt.Errorf("画像created_at (行%d): %w", rowNumber, err)
			}
			prepared, err := prepareDecodedTransactionImageContext(context.Background(), filename, mimeType, data)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("画像 (行%d): %w", rowNumber, err)
			}
			if int64(len(prepared.data)) > models.MaxImageBytesDatabase-parsed.decodedImageBytes {
				return csvV3Import{}, fmt.Errorf("CSV v3画像の合計サイズが上限を超えました (行%d)", rowNumber)
			}
			if err := parsed.addParsedText(prepared.filename, prepared.mimeType, createdAt); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			parsed.decodedImageBytes += int64(len(prepared.data))
			parsed.images = append(parsed.images, csvV3Image{id: id, transactionID: txID, filename: prepared.filename, mimeType: prepared.mimeType, createdAt: createdAt, data: prepared.data})
		case "tag":
			id, err := csvV3Int(record, headerMap, "id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("タグid (行%d): %w", rowNumber, err)
			}
			if _, exists := tagIDs[id]; exists {
				return csvV3Import{}, fmt.Errorf("タグidが重複しています (行%d): %d", rowNumber, id)
			}
			tagIDs[id] = struct{}{}
			name, err := csvV3DecodedText(record, headerMap, "tag_name", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := validateCSVV3TagName(name); err != nil {
				return csvV3Import{}, fmt.Errorf("タグ名が不正です (行%d): %w", rowNumber, err)
			}
			level, err := csvV3Int(record, headerMap, "tag_level", true, true)
			if err != nil || level < 1 || level > 3 {
				return csvV3Import{}, fmt.Errorf("タグ階層が不正です (行%d)", rowNumber)
			}
			parent, err := csvV3Int(record, headerMap, "tag_parent_id", false, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("タグ親id (行%d): %w", rowNumber, err)
			}
			if level == 1 && parent != 0 {
				return csvV3Import{}, fmt.Errorf("最上位タグに親は指定できません (行%d)", rowNumber)
			}
			if level > 1 && parent == 0 {
				return csvV3Import{}, fmt.Errorf("下位タグには親が必要です (行%d)", rowNumber)
			}
			row := csvV3Tag{id: id, parentID: parent, name: name, level: int(level)}
			if parent == 0 {
				if _, exists := tagNames[name]; exists {
					return csvV3Import{}, fmt.Errorf("同じ階層のタグ名が重複しています (行%d)", rowNumber)
				}
				tagNames[name] = struct{}{}
			} else {
				key := fmt.Sprintf("%d\x00%s", parent, name)
				if _, exists := tagNames[key]; exists {
					return csvV3Import{}, fmt.Errorf("同じ親のタグ名が重複しています (行%d)", rowNumber)
				}
				tagNames[key] = struct{}{}
			}
			if err := parsed.addParsedText(name); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			tagsByID[id] = row
			parsed.tags = append(parsed.tags, row)
		case "transaction_tag":
			txID, err := csvV3Int(record, headerMap, "transaction_id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("transaction_tag取引id (行%d): %w", rowNumber, err)
			}
			tagID, err := csvV3Int(record, headerMap, "tag_id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("transaction_tagタグid (行%d): %w", rowNumber, err)
			}
			pair := [2]int64{txID, tagID}
			if _, exists := seenTagLinks[pair]; exists {
				return csvV3Import{}, fmt.Errorf("タグ紐付けが重複しています (行%d)", rowNumber)
			}
			seenTagLinks[pair] = struct{}{}
			parsed.tagLinks = append(parsed.tagLinks, pair)
		case "transaction_link":
			parent, err := csvV3Int(record, headerMap, "parent_id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("リンクparent_id (行%d): %w", rowNumber, err)
			}
			child, err := csvV3Int(record, headerMap, "child_id", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("リンクchild_id (行%d): %w", rowNumber, err)
			}
			if parent == child {
				return csvV3Import{}, fmt.Errorf("同一の取引同士はリンクできません (行%d)", rowNumber)
			}
			if parent > child {
				parent, child = child, parent
			}
			pair := [2]int64{parent, child}
			if _, exists := seenTransactionLinks[pair]; exists {
				return csvV3Import{}, fmt.Errorf("取引リンクが重複しています (行%d)", rowNumber)
			}
			seenTransactionLinks[pair] = struct{}{}
			parsed.transactionLinks = append(parsed.transactionLinks, pair)
		case "setting":
			key, err := csvV3DecodedText(record, headerMap, "setting_key", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			value, err := csvV3DecodedText(record, headerMap, "setting_value", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := validateCSVV3Setting(key, value); err != nil {
				return csvV3Import{}, fmt.Errorf("設定が不正です (行%d): %w", rowNumber, err)
			}
			if err := parsed.addParsedText(key, value); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if _, exists := parsed.settings[key]; exists {
				return csvV3Import{}, fmt.Errorf("設定キーが重複しています (行%d): %s", rowNumber, key)
			}
			parsed.settings[key] = value
		default:
			return csvV3Import{}, fmt.Errorf("未対応のrecord_typeです (行%d): %q", rowNumber, recordType)
		}
	}
	for _, row := range parsed.tags {
		if row.parentID == 0 {
			if row.level != 1 {
				return csvV3Import{}, fmt.Errorf("タグ親なしの階層が不正です: %d", row.id)
			}
			continue
		}
		parent, ok := tagsByID[row.parentID]
		if !ok {
			return csvV3Import{}, fmt.Errorf("タグ親が見つかりません: %d", row.parentID)
		}
		if parent.level+1 != row.level {
			return csvV3Import{}, fmt.Errorf("タグ親の階層が不正です: %d", row.id)
		}
	}
	for _, row := range parsed.images {
		if _, ok := transactionIDs[row.transactionID]; !ok {
			return csvV3Import{}, fmt.Errorf("画像の取引が見つかりません: %d", row.transactionID)
		}
	}
	for _, pair := range parsed.tagLinks {
		if _, ok := transactionIDs[pair[0]]; !ok {
			return csvV3Import{}, fmt.Errorf("タグ紐付けの取引が見つかりません: %d", pair[0])
		}
		if _, ok := tagIDs[pair[1]]; !ok {
			return csvV3Import{}, fmt.Errorf("タグ紐付けのタグが見つかりません: %d", pair[1])
		}
	}
	for _, pair := range parsed.transactionLinks {
		if _, ok := transactionIDs[pair[0]]; !ok {
			return csvV3Import{}, fmt.Errorf("取引リンクの参照先が見つかりません: %d", pair[0])
		}
		if _, ok := transactionIDs[pair[1]]; !ok {
			return csvV3Import{}, fmt.Errorf("取引リンクの参照先が見つかりません: %d", pair[1])
		}
	}
	if len(parsed.transactions) == 0 && (len(parsed.images) > 0 || len(parsed.tagLinks) > 0 || len(parsed.transactionLinks) > 0) {
		return csvV3Import{}, fmt.Errorf("関連レコードには取引が必要です")
	}
	return parsed, nil
}

func loadCSVV3Settings(tx *sql.Tx, incoming map[string]string, replace bool) (map[string]string, error) {
	settings := make(map[string]string)
	if !replace {
		rows, err := tx.Query("SELECT key, value FROM settings")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				_ = rows.Close()
				return nil, err
			}
			settings[key] = value
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	for key, value := range incoming {
		settings[key] = value
	}
	return settings, nil
}

func csvV3AccountSettings(settings map[string]string, key string) map[string]bool {
	set := make(map[string]bool)
	var raw []string
	if value, ok := settings[key]; ok {
		// These two settings have historically been JSON arrays. Invalid legacy
		// values intentionally produce an empty set, matching the normal getter.
		if err := json.Unmarshal([]byte(value), &raw); err == nil {
			for _, item := range raw {
				item = strings.TrimSpace(item)
				if item != "" {
					set[item] = true
				}
			}
		}
	}
	return set
}

func (s *Service) importCSVV3(content, mode string) (int, error) {
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	parsed, err := s.parseCSVV3(content)
	if err != nil {
		return 0, err
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("CSV v3 transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	if mode == "replace" {
		for _, table := range []string{"transaction_links", "transaction_tags", "transaction_images", "transactions", "tags", "settings", "ai_transaction_idempotency", "ai_daily_transaction_usage"} {
			if _, err := tx.Exec("DELETE FROM " + table); err != nil {
				return 0, fmt.Errorf("CSV v3既存データ削除エラー (%s): %w", table, err)
			}
		}
	}
	transactionMap := make(map[int64]int64, len(parsed.transactions))
	accounts := make(map[string]struct{})
	for _, row := range parsed.transactions {
		result, err := tx.Exec("INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)", row.account, row.date, row.item, row.txType, row.amount, row.memo)
		if err != nil {
			return 0, fmt.Errorf("CSV v3取引登録エラー: %w", err)
		}
		newID, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		transactionMap[row.id] = newID
		accounts[row.account] = struct{}{}
	}
	settings, err := loadCSVV3Settings(tx, parsed.settings, mode == "replace")
	if err != nil {
		return 0, fmt.Errorf("CSV v3設定取得エラー: %w", err)
	}
	if len(parsed.settings) > 0 {
		for key, value := range parsed.settings {
			if _, err := tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
				return 0, fmt.Errorf("CSV v3設定登録エラー: %w", err)
			}
		}
	}
	tagMap := make(map[int64]int64, len(parsed.tags))
	for level := 1; level <= 3; level++ {
		for _, row := range parsed.tags {
			if row.level != level {
				continue
			}
			newParent := interface{}(nil)
			if row.parentID != 0 {
				mapped, ok := tagMap[row.parentID]
				if !ok {
					return 0, fmt.Errorf("CSV v3タグ親が見つかりません: %d", row.parentID)
				}
				newParent = mapped
			}
			var existing int64
			var lookupErr error
			if row.parentID == 0 {
				lookupErr = tx.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id IS NULL", row.name).Scan(&existing)
			} else {
				lookupErr = tx.QueryRow("SELECT id FROM tags WHERE name = ? AND parent_id = ?", row.name, newParent).Scan(&existing)
			}
			if lookupErr == nil {
				tagMap[row.id] = existing
				continue
			}
			if lookupErr != sql.ErrNoRows {
				return 0, fmt.Errorf("CSV v3タグ重複確認エラー: %w", lookupErr)
			}
			result, err := tx.Exec("INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)", row.name, newParent, row.level)
			if err != nil {
				return 0, fmt.Errorf("CSV v3タグ登録エラー: %w", err)
			}
			newID, err := result.LastInsertId()
			if err != nil {
				return 0, err
			}
			tagMap[row.id] = newID
		}
	}
	for _, row := range parsed.images {
		newTxID, ok := transactionMap[row.transactionID]
		if !ok {
			return 0, fmt.Errorf("CSV v3画像の取引が見つかりません: %d", row.transactionID)
		}
		if err := checkImageStorageQuota(tx, newTxID, []preparedTransactionImage{{filename: row.filename, mimeType: row.mimeType, data: row.data}}); err != nil {
			return 0, fmt.Errorf("CSV v3画像クォータ: %w", err)
		}
		if row.createdAt == "" {
			_, err = tx.Exec("INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, ?, ?, ?)", newTxID, row.filename, row.data, row.mimeType)
		} else {
			_, err = tx.Exec("INSERT INTO transaction_images (transaction_id, filename, data, mime_type, created_at) VALUES (?, ?, ?, ?, ?)", newTxID, row.filename, row.data, row.mimeType, row.createdAt)
		}
		if err != nil {
			return 0, fmt.Errorf("CSV v3画像登録エラー: %w", err)
		}
	}
	for _, pair := range parsed.tagLinks {
		newTxID, ok1 := transactionMap[pair[0]]
		newTagID, ok2 := tagMap[pair[1]]
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("CSV v3タグ紐付けの参照先が見つかりません")
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", newTxID, newTagID); err != nil {
			return 0, fmt.Errorf("CSV v3タグ紐付け登録エラー: %w", err)
		}
	}
	creditCards := csvV3AccountSettings(settings, "credit_card_items")
	bankAccounts := csvV3AccountSettings(settings, "bank_account_items")
	for _, pair := range parsed.transactionLinks {
		parent, ok1 := transactionMap[pair[0]]
		child, ok2 := transactionMap[pair[1]]
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("CSV v3リンクの参照先が見つかりません")
		}
		var accountA, accountB string
		if err := tx.QueryRow("SELECT account FROM transactions WHERE id = ?", parent).Scan(&accountA); err != nil {
			return 0, err
		}
		if err := tx.QueryRow("SELECT account FROM transactions WHERE id = ?", child).Scan(&accountB); err != nil {
			return 0, err
		}
		if !((creditCards[accountA] && bankAccounts[accountB]) || (bankAccounts[accountA] && creditCards[accountB])) {
			return 0, fmt.Errorf("CSV v3リンクはクレジットカード項目と銀行口座項目の取引間でのみ追加できます")
		}
		if _, err := tx.Exec("INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)", parent, child); err != nil {
			return 0, fmt.Errorf("CSV v3リンク登録エラー: %w", err)
		}
	}
	for account := range accounts {
		if err := recalculateBalanceIn(tx, account); err != nil {
			return 0, fmt.Errorf("CSV v3残高再計算エラー: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("CSV v3コミットエラー: %w", err)
	}
	s.autoSnapshot()
	return len(parsed.transactions), nil
}

// ImportCSVWithReservation is for request handlers that already acquired the
// shared CSV slot before reading/decoding a large request body.
func (s *Service) ImportCSVWithReservation(content, mode string) (int, error) {
	return s.importCSV(content, mode)
}
