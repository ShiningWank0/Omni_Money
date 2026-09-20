package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/validation"
)

// ErrCSVPreviewConflict reports that the pinned preview no longer matches the
// ledger state (or the submitted CSV), so the operator must re-run the preview
// before applying. API layers map it to HTTP 409.
var ErrCSVPreviewConflict = errors.New("CSV importの対象データが変更されました。プレビューを取り直してください")

// CSVImportPins carries the digests a client received from a preview and must
// present at apply time. Apply recomputes both digests and fails closed with
// ErrCSVPreviewConflict on any mismatch, so the preview→apply pair is atomic
// from the operator's point of view without server-side preview state.
type CSVImportPins struct {
	SourceDigest string
	TargetDigest string
}

// CSVImportDigests echoes the digests computed for a specific request. Apply
// fills them whenever pins were supplied; preview always fills them.
type CSVImportDigests struct {
	SourceDigest string
	TargetDigest string
}

// CSVReplaceImpact enumerates exactly the rows a v3 replace would delete, so
// the destructive scope is visible before confirmation.
type CSVReplaceImpact struct {
	Transactions     int64 `json:"transactions"`
	Images           int64 `json:"images"`
	Tags             int64 `json:"tags"`
	TransactionTags  int64 `json:"transaction_tags"`
	TransactionLinks int64 `json:"transaction_links"`
	LedgerSettings   int64 `json:"ledger_settings"`
}

// CSVImportPreview is the dry-run report for one CSV payload. Classification
// uses the content tuple (account, date, item, type, amount) plus memo:
// duplicate = full tuple already stored, conflict = same natural key with a
// different memo, new = no natural-key match. Import IDs are regenerated on
// import, so IDs never participate in the comparison.
type CSVImportPreview struct {
	Mode           string            `json:"mode"`
	SourceDigest   string            `json:"source_digest"`
	TargetDigest   string            `json:"target_digest"`
	NewCount       int               `json:"new_count"`
	DuplicateCount int               `json:"duplicate_count"`
	ConflictCount  int               `json:"conflict_count"`
	ReplaceImpact  *CSVReplaceImpact `json:"replace_impact,omitempty"`
}

type digestQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validDigestHex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidCSVImportDigest reports whether value is a well-formed preview digest.
func ValidCSVImportDigest(value string) bool {
	return validDigestHex(value)
}

// digestingReader computes the source digest while the parser consumes the
// stream, so the digest always covers exactly the bytes the import would
// parse without materializing a second copy of a 512 MiB archive.
type digestingReader struct {
	input  io.Reader
	digest hash.Hash
}

func (d *digestingReader) Read(p []byte) (int, error) {
	n, err := d.input.Read(p)
	if n > 0 {
		_, _ = d.digest.Write(p[:n])
	}
	return n, err
}

func hashLengthPrefixed(digest hash.Hash, fields ...string) {
	var length [8]byte
	for _, field := range fields {
		// Same unambiguous length-prefixed field stream as updateCSVV3Digest.
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(field))
	}
}

// ledgerContentDigest pins the deletion surface of a replace and the pre-image
// of an append: every table replace wipes, hashed in a canonical row order.
func ledgerContentDigest(ctx context.Context, db digestQueryer) (string, error) {
	digest := sha256.New()
	queries := []string{
		"SELECT id, account, CAST(date AS TEXT), item, type, amount, balance, memo FROM transactions ORDER BY id",
		"SELECT id, name, parent_id, level, legacy_duplicate FROM tags ORDER BY id",
		"SELECT transaction_id, tag_id FROM transaction_tags ORDER BY transaction_id, tag_id",
		"SELECT parent_id, child_id FROM transaction_links ORDER BY parent_id, child_id",
		"SELECT id, transaction_id, filename, data, mime_type, CAST(created_at AS TEXT) FROM transaction_images ORDER BY id",
		"SELECT id, transaction_id, filename, data, mime_type, CAST(created_at AS TEXT) FROM transaction_image_archive ORDER BY id",
		"SELECT transaction_id, amount FROM transaction_archive_amounts ORDER BY transaction_id",
		"SELECT key, value FROM settings WHERE key IN ('credit_card_items', 'bank_account_items') ORDER BY key",
		"SELECT credential_id, idempotency_key_sha256, request_sha256, transaction_id, response_account, response_date, created_at FROM ai_transaction_idempotency ORDER BY credential_id, idempotency_key_sha256",
		"SELECT credential_id, utc_date, successful_creates FROM ai_daily_transaction_usage ORDER BY credential_id, utc_date",
	}
	for _, query := range queries {
		hashLengthPrefixed(digest, query)
		if err := hashDigestRows(ctx, db, digest, query); err != nil {
			return "", fmt.Errorf("target digest生成エラー: %w", err)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// hashDigestRows streams each row's values through the length-prefixed digest.
func hashDigestRows(ctx context.Context, db digestQueryer, digest hash.Hash, query string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		values := make([]any, len(columns))
		scan := make([]any, len(columns))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return err
		}
		fields := make([]string, len(columns))
		for i, value := range values {
			field, err := digestCellValue(value)
			if err != nil {
				return err
			}
			fields[i] = fmt.Sprintf("%T:%s", value, field)
		}
		hashLengthPrefixed(digest, fields...)
	}
	return rows.Err()
}

// digestCellValue renders one scanned SQL value as the canonical digest field.
// The sqlite driver surfaces DATE-declared columns as time.Time in generic
// scans, so those must be normalized exactly like the classification keys.
func digestCellValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		return strconv.FormatInt(int64(typed), 10), nil
	case []byte:
		return string(typed), nil
	case string:
		return typed, nil
	case time.Time:
		return normalizeCSVPreviewDate(typed.Format("2006-01-02 15:04:05")), nil
	default:
		return "", fmt.Errorf("target digestで予期しない型の列を検出しました")
	}
}

// normalizeCSVPreviewDate collapses the accepted ledger date representations
// onto one canonical key so v3 strings, legacy time.Time values and UI-written
// rows compare equal when they denote the same point in time.
func normalizeCSVPreviewDate(value string) string {
	trimmed := strings.TrimSpace(value)
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02T15:04:05Z07:00",
		time.RFC3339, "2006-01-02 15:04", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			if parsed.Hour() == 0 && parsed.Minute() == 0 && parsed.Second() == 0 {
				return parsed.Format("2006-01-02")
			}
			return parsed.Format("2006-01-02 15:04:05")
		}
	}
	return trimmed
}

type csvPreviewKey struct {
	account, date, item, txType string
	amount                      int64
}

type csvPreviewFullKey struct {
	key  csvPreviewKey
	memo string
}

func csvStoredPreviewAmount(amount int64, archiveAmount bool) int64 {
	if archiveAmount {
		return validation.MaxTransactionAmount
	}
	return amount
}

// classifyCSVPreviewRows counts rows against the stored ledger content.
func classifyCSVPreviewRows(rows []csvPreviewFullKey, existingFull map[csvPreviewFullKey]struct{}, existingNatural map[csvPreviewKey]struct{}) (newCount, duplicateCount, conflictCount int) {
	for _, row := range rows {
		if _, ok := existingFull[row]; ok {
			duplicateCount++
			continue
		}
		if _, ok := existingNatural[row.key]; ok {
			conflictCount++
			continue
		}
		newCount++
	}
	return newCount, duplicateCount, conflictCount
}

func loadStoredTransactionKeys(ctx context.Context, db digestQueryer) (map[csvPreviewFullKey]struct{}, map[csvPreviewKey]struct{}, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT account, date, item, type, amount, COALESCE(memo, '') FROM transactions")
	if err != nil {
		return nil, nil, fmt.Errorf("既存取引の読み取りに失敗しました: %w", err)
	}
	defer rows.Close()
	existingFull := make(map[csvPreviewFullKey]struct{})
	existingNatural := make(map[csvPreviewKey]struct{})
	for rows.Next() {
		var account, item, txType, memo string
		var amount int64
		var rawDate any
		if err := rows.Scan(&account, &rawDate, &item, &txType, &amount, &memo); err != nil {
			return nil, nil, fmt.Errorf("既存取引の読み取りに失敗しました: %w", err)
		}
		rawDateValue, err := digestCellValue(rawDate)
		if err != nil {
			return nil, nil, fmt.Errorf("既存取引の読み取りに失敗しました: %w", err)
		}
		key := csvPreviewKey{
			account: account,
			date:    normalizeCSVPreviewDate(rawDateValue),
			item:    item,
			txType:  txType,
			amount:  amount,
		}
		existingNatural[key] = struct{}{}
		existingFull[csvPreviewFullKey{key: key, memo: memo}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("既存取引の読み取りに失敗しました: %w", err)
	}
	return existingFull, existingNatural, nil
}

func replaceImpactCounts(ctx context.Context, db digestQueryer) (*CSVReplaceImpact, error) {
	impact := &CSVReplaceImpact{}
	single := func(target *int64, query string, args ...any) error {
		row := db.QueryRowContext(ctx, query, args...)
		if err := row.Scan(target); err != nil {
			return err
		}
		return nil
	}
	if err := single(&impact.Transactions, "SELECT COUNT(*) FROM transactions"); err != nil {
		return nil, err
	}
	if err := single(&impact.Images, "SELECT (SELECT COUNT(*) FROM transaction_images) + (SELECT COUNT(*) FROM transaction_image_archive)"); err != nil {
		return nil, err
	}
	if err := single(&impact.Tags, "SELECT COUNT(*) FROM tags"); err != nil {
		return nil, err
	}
	if err := single(&impact.TransactionTags, "SELECT COUNT(*) FROM transaction_tags"); err != nil {
		return nil, err
	}
	if err := single(&impact.TransactionLinks, "SELECT COUNT(*) FROM transaction_links"); err != nil {
		return nil, err
	}
	if err := single(&impact.LedgerSettings,
		"SELECT COUNT(*) FROM settings WHERE key IN ('credit_card_items', 'bank_account_items')"); err != nil {
		return nil, err
	}
	return impact, nil
}

// verifyCSVPins recomputes the target digest inside the write transaction and
// fails closed when it no longer matches the pinned preview. The source digest
// is verified by the streaming caller before the transaction opens.
func verifyCSVPins(ctx context.Context, tx *sql.Tx, pins *CSVImportPins, digests *CSVImportDigests) error {
	if pins == nil {
		return nil
	}
	targetDigest, err := ledgerContentDigest(ctx, tx)
	if err != nil {
		return err
	}
	if digests != nil {
		digests.TargetDigest = targetDigest
	}
	if pins.TargetDigest != targetDigest {
		return ErrCSVPreviewConflict
	}
	return nil
}

// PreviewCSVImportReaderContext dry-runs one CSV payload with the same parsers
// and validation the apply path uses. Nothing is written: classification and
// the replace deletion surface are computed against the current ledger, and
// both digests needed to pin a later apply are returned.
func (s *Service) PreviewCSVImportReaderContext(ctx context.Context, input io.Reader, mode string) (*CSVImportPreview, error) {
	if input == nil {
		return nil, fmt.Errorf("CSV入力がありません")
	}
	if mode != "append" && mode != "replace" {
		return nil, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var release func()
	if !HasCSVImportReservation(ctx) {
		var ok bool
		release, ok = TryAcquireCSVOperationSlot()
		if !ok {
			return nil, fmt.Errorf("CSV入出力が混雑しています。しばらくしてから再試行してください")
		}
		defer release()
	}
	sourceDigest := sha256.New()
	digesting := &digestingReader{input: input, digest: sourceDigest}
	buffered := bufio.NewReader(digesting)
	firstLine, err := readCSVHeaderLine(buffered)
	if err != nil {
		return nil, err
	}
	source := io.MultiReader(strings.NewReader(firstLine), buffered)
	headerReader := csv.NewReader(strings.NewReader(firstLine))
	headerReader.FieldsPerRecord = -1
	header, headerErr := headerReader.Read()
	hasV3 := false
	if headerErr == nil {
		if len(header) > 0 {
			header[0] = strings.TrimPrefix(header[0], "\ufeff")
		}
		hasV3 = isCSVV3Header(header)
	}
	stream := &csvTotalLimitReader{input: source, remaining: MaxCSVImportBytes}
	if hasV3 {
		parsed, parseErr := s.parseCSVV3Reader(ctx, stream, false)
		if parseErr != nil {
			return nil, parseErr
		}
		if cleanupErr := parsed.cleanup(); cleanupErr != nil {
			return nil, fmt.Errorf("CSV画像一時領域のcleanupに失敗しました: %w", cleanupErr)
		}
		if !parsed.hasManifest {
			return nil, fmt.Errorf("CSV v3 importには完全性manifestが必要です")
		}
		rows := make([]csvPreviewFullKey, 0, len(parsed.transactions))
		for _, row := range parsed.transactions {
			rows = append(rows, csvPreviewFullKey{
				key: csvPreviewKey{
					account: row.account,
					date:    normalizeCSVPreviewDate(row.date),
					item:    row.item,
					txType:  row.txType,
					amount:  csvStoredPreviewAmount(row.amount, row.archiveAmount),
				},
				memo: row.memo,
			})
		}
		return s.buildCSVPreview(ctx, rows, mode, hex.EncodeToString(sourceDigest.Sum(nil)))
	}
	if mode == "replace" {
		return nil, ErrCSVReplaceRequiresV3
	}
	rows, err := parseLegacyCSVRows(ctx, stream)
	if err != nil {
		return nil, err
	}
	previewRows := make([]csvPreviewFullKey, 0, len(rows))
	for _, row := range rows {
		previewRows = append(previewRows, csvPreviewFullKey{
			key: csvPreviewKey{
				account: row.account,
				date:    normalizeCSVPreviewDate(row.date.Format("2006-01-02 15:04:05")),
				item:    row.item,
				txType:  row.txType,
				amount:  csvStoredPreviewAmount(row.amount, row.archiveAmount),
			},
			memo: row.memo,
		})
	}
	return s.buildCSVPreview(ctx, previewRows, mode, hex.EncodeToString(sourceDigest.Sum(nil)))
}

// Classification, impact and pin must describe one database snapshot.
func (s *Service) buildCSVPreview(ctx context.Context, rows []csvPreviewFullKey, mode, sourceDigest string) (*CSVImportPreview, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	existingFull, existingNatural, err := loadStoredTransactionKeys(ctx, tx)
	if err != nil {
		return nil, err
	}
	newCount, duplicateCount, conflictCount := classifyCSVPreviewRows(rows, existingFull, existingNatural)
	targetDigest, err := ledgerContentDigest(ctx, tx)
	if err != nil {
		return nil, err
	}
	preview := &CSVImportPreview{
		Mode: mode, SourceDigest: sourceDigest, TargetDigest: targetDigest,
		NewCount: newCount, DuplicateCount: duplicateCount, ConflictCount: conflictCount,
	}
	if mode == "replace" {
		preview.ReplaceImpact, err = replaceImpactCounts(ctx, tx)
		if err != nil {
			return nil, fmt.Errorf("replace影響の集計に失敗しました: %w", err)
		}
	}
	return preview, nil
}
