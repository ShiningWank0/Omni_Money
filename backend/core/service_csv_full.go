package core

// This file implements the normalized, lossless CSV v3 format.  CSV remains
// the transport (so it can be archived and inspected with ordinary tools), but
// each row is explicitly typed.  That avoids duplicating transactions when a
// transaction has several images/tags and lets import rebuild all foreign-key
// relationships after SQLite assigns new IDs.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/fileprivacy"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

const (
	// MaxCSVImportBytes bounds the complete UTF-8 CSV payload on the streaming
	// file/HTTP path.  This is deliberately larger than the 256 MiB decoded
	// image quota because Base64 expands a full image set to roughly 342 MiB,
	// with CSV metadata on top.  The raw archive is spooled to private disk and
	// decoded images are spooled one at a time, so this limit is not a heap
	// allocation promise.
	MaxCSVImportBytes int64 = 512 * 1024 * 1024
	// MaxCSVStringImportBytes bounds the legacy Wails/JSON string compatibility
	// path.  Full-size archives must use ImportCSVFileContext or raw HTTP CSV;
	// retaining a small string cap prevents a caller from asking a shared
	// process to hold the raw archive, JSON value, and parsed strings together.
	MaxCSVStringImportBytes int64 = 64 * 1024 * 1024
	// MaxCSVImportWireBytes is the fixed raw CSV request wire cap. It is kept
	// separate from the smaller JSON compatibility cap below.
	MaxCSVImportWireBytes int64 = MaxCSVImportBytes + 8*1024*1024
	// MaxCSVJSONWireBytes applies only to the bounded compatibility JSON
	// endpoint.  The JSON value itself is capped separately at
	// MaxCSVStringImportBytes; this small fixed allowance covers the envelope
	// and ordinary escaping without allowing an archive-sized JSON allocation.
	MaxCSVJSONWireBytes int64 = MaxCSVStringImportBytes + 1*1024*1024
	// Desktop file exports prepend a three-byte UTF-8 BOM. Keep the CSV body
	// three bytes below the import limit so the resulting file is self-importable.
	maxCSVExportBytes int64 = MaxCSVImportBytes - 3
	maxCSVRows              = 1_000_000
	maxCSVFieldBytes        = 8 * 1024 * 1024
	// Bound the complete raw and decoded size of one CSV record before
	// encoding/csv allocates its record buffer. A valid image/text field fits
	// below this after CSV quoting and v3 text framing, while 23 hostile fields
	// cannot multiply into an archive-sized heap allocation.
	maxCSVRecordBytes  = 32 * 1024 * 1024
	maxCSVRecordFields = 256
	// csvV3Text uses a reserved marker and Base64 only for values containing CR,
	// because encoding/csv normalizes quoted CRLF to LF. Allow the bounded wire
	// expansion here, then enforce the exact decoded value size below.
	maxCSVGuardFieldBytes         = maxCSVFieldBytes*2 + 128
	maxCSVHeaderBytes             = 64 * 1024
	maxCSVSettingKeyBytes         = 256
	maxCSVSettingValueBytes       = 2 * 1024 * 1024
	maxCSVParsedTextBytes   int64 = 64 * 1024 * 1024
	// image.Decode may materialize an RGBA buffer in addition to the encoded
	// bytes. Reserve this bounded working allowance before invoking image
	// decoders; it is released immediately after each image is validated.
	maxCSVImageDecodeScratchBytes int64 = models.MaxImagePixels*4 + models.MaxImageBytes
	// MaxCSVTempBudgetBytes is the process-wide weighted budget shared by upload
	// spools, import image files/working copies, and export archives. Every live
	// private-file or bounded decode allocation reserves its actual or
	// worst-case bytes before allocation; competing full-size operations fail
	// closed instead of exceeding the process-wide cap. Reservations are held
	// until the corresponding bytes stop being live, not merely until spooling
	// begins or ends.
	MaxCSVTempBudgetBytes int64 = 2 * MaxCSVImportWireBytes
)

const (
	csvV3ManifestRecordType = "manifest"
	csvV3ManifestKey        = "omni_money_csv_v3_manifest"
	csvV3ManifestFormat     = "omni-money-csv-v3"
)

// csvV3Manifest is deliberately a fixed-shape JSON value carried in the
// final typed CSV row. The manifest is not included in its own digest; all
// preceding rows are hashed in their canonical decoded-field representation.
// Counts make truncation at any row boundary fail before replace opens a DB
// transaction, while the digest detects tampering that preserves row counts.
type csvV3Manifest struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Counts  map[string]int64 `json:"counts"`
	Digest  string           `json:"digest"`
}

var csvV3ManifestRecordTypes = []string{
	"transaction", "transaction_legacy", "image", "tag", "tag_legacy",
	"transaction_tag", "transaction_link", "setting", "setting_legacy", csvV3ManifestRecordType,
}

func containsCSVV3ManifestRecordType(recordType string) bool {
	for _, allowed := range csvV3ManifestRecordTypes {
		if recordType == allowed && recordType != csvV3ManifestRecordType {
			return true
		}
	}
	return false
}

const legacyCSVReplaceError = "legacy/v1/v2 CSVではreplaceを利用できません。完全置換にはCSV v3を使用してください"

// ErrCSVReplaceRequiresV3 is a safe, typed compatibility error. API layers
// may expose its remediation without forwarding parser/DB details.
var ErrCSVReplaceRequiresV3 = errors.New(legacyCSVReplaceError)

// Imports and exports share one process-wide heavy operation slot. Both can
// hold large database/blob buffers and a read transaction at the same time.
var csvHeavySlots = make(chan struct{}, 1)

var csvTempBudget = struct {
	sync.Mutex
	used int64
}{}

type csvImportReservationContextKey struct{}
type csvOperationReservationContextKey struct{}
type csvTempReservationContextKey struct{}

// TryAcquireCSVOperationSlot reserves the shared process-wide heavy CSV slot.
func TryAcquireCSVOperationSlot() (func(), bool) {
	select {
	case csvHeavySlots <- struct{}{}:
		return func() { <-csvHeavySlots }, true
	default:
		return nil, false
	}
}

// TryAcquireCSVImportSlot is retained for middleware callers and reserves the
// same shared slot.
func TryAcquireCSVImportSlot() (func(), bool) {
	return TryAcquireCSVOperationSlot()
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

func WithCSVOperationReservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, csvOperationReservationContextKey{}, true)
}

func HasCSVOperationReservation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	reserved, _ := ctx.Value(csvOperationReservationContextKey{}).(bool)
	return reserved
}

// TryAcquireCSVTempBudget reserves private-disk capacity for a CSV operation.
// It is deliberately non-blocking: callers fail closed with a busy/storage
// response instead of queueing unbounded archives in the process.
func TryAcquireCSVTempBudget(bytes int64) (func(), bool) {
	if bytes <= 0 || bytes > MaxCSVTempBudgetBytes {
		return nil, false
	}
	csvTempBudget.Lock()
	if csvTempBudget.used > MaxCSVTempBudgetBytes-bytes {
		csvTempBudget.Unlock()
		return nil, false
	}
	csvTempBudget.used += bytes
	csvTempBudget.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			csvTempBudget.Lock()
			csvTempBudget.used -= bytes
			csvTempBudget.Unlock()
		})
	}, true
}

// ResizeCSVTempBudget changes an existing reservation without releasing it
// between allocations. This is used for Base64's conservative admission: the
// decoder's exact output may be up to two bytes smaller than DecodedLen, and
// those bytes must not make an otherwise valid archive fail or briefly become
// unaccounted memory.
func ResizeCSVTempBudget(reserved, actual int64) (func(), bool) {
	if reserved <= 0 || actual <= 0 || reserved > MaxCSVTempBudgetBytes || actual > MaxCSVTempBudgetBytes {
		return nil, false
	}
	csvTempBudget.Lock()
	if csvTempBudget.used < reserved {
		csvTempBudget.Unlock()
		return nil, false
	}
	delta := actual - reserved
	if delta > 0 && csvTempBudget.used > MaxCSVTempBudgetBytes-delta {
		csvTempBudget.Unlock()
		return nil, false
	}
	csvTempBudget.used += delta
	csvTempBudget.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			csvTempBudget.Lock()
			csvTempBudget.used -= actual
			csvTempBudget.Unlock()
		})
	}, true
}

// WithCSVTempReservation marks an already-admitted request so the reader
// entrypoint does not reserve the same private-disk budget a second time.
func WithCSVTempReservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, csvTempReservationContextKey{}, true)
}

func HasCSVTempReservation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	reserved, _ := ctx.Value(csvTempReservationContextKey{}).(bool)
	return reserved
}

var csvV3Headers = []string{
	csvVersionHeader, "record_type", "id", "transaction_id", "parent_id", "child_id", "tag_id",
	"account", "date", "item", "type", "amount", "balance", "memo", "filename", "mime_type",
	"data_base64", "tag_name", "tag_parent_id", "tag_level", "setting_key", "setting_value", "created_at",
}

type csvLimitedStringWriter struct {
	b     strings.Builder
	dst   io.Writer
	bytes int64
	limit int64
}

// csvFieldLimitReader validates CSV record boundaries before encoding/csv sees
// a byte slice. encoding/csv grows an internal field buffer before reporting
// a large-field error, so a normal Reader wrapper is too late for hostile
// quoted fields. This small state machine understands commas, quoted
// newlines, and doubled quotes while retaining csv.Reader for the actual CSV
// decoding and UTF-8 checks.
type csvFieldLimitReader struct {
	ctx                context.Context
	input              io.Reader
	maxFieldBytes      int
	rejectQuotedCR     bool
	fieldBytes         int
	recordBytes        int
	recordDecodedBytes int
	recordFields       int
	inQuotes           bool
	quotePending       bool
	fieldStart         bool
	pendingCR          bool
}

func (r *csvFieldLimitReader) Read(p []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, readErr := r.input.Read(p)
	if r.recordFields == 0 {
		r.recordFields = 1
	}
	for i := 0; i < n; i++ {
		if r.ctx != nil && i%4096 == 0 {
			if err := r.ctx.Err(); err != nil {
				return i, err
			}
		}
		if err := r.consume(p[i]); err != nil {
			return i, err
		}
	}
	if readErr == io.EOF && r.pendingCR {
		if r.inQuotes && r.rejectQuotedCR {
			return n, fmt.Errorf("CSV v3のquoted CRはlossless text encodingを使用してください")
		}
		r.pendingCR = false
		r.fieldBytes++
		if r.fieldBytes > r.maxFieldBytes {
			return n, fmt.Errorf("CSV列が大きすぎます")
		}
	}
	return n, readErr
}

func (r *csvFieldLimitReader) consume(b byte) error {
	r.recordBytes++
	if r.recordBytes > maxCSVRecordBytes {
		return fmt.Errorf("CSVレコードが大きすぎます")
	}
	add := func(decoded bool) error {
		if decoded {
			r.fieldBytes++
			r.recordDecodedBytes++
			if r.fieldBytes > r.maxFieldBytes {
				return fmt.Errorf("CSV列が大きすぎます")
			}
			if r.recordDecodedBytes > maxCSVRecordBytes {
				return fmt.Errorf("CSVレコードの解析後サイズが大きすぎます")
			}
		}
		return nil
	}
	if r.pendingCR {
		r.pendingCR = false
		if b == '\n' {
			if r.inQuotes {
				if r.rejectQuotedCR {
					return fmt.Errorf("CSV v3のquoted CRLFはlossless text encodingを使用してください")
				}
				// encoding/csv normalizes CRLF inside quoted fields to one LF.
				if err := add(true); err != nil {
					return err
				}
			} else {
				// Outside quotes CRLF is a record terminator, not field data.
				r.fieldBytes = 0
				r.fieldStart = true
				r.recordBytes = 0
				r.recordDecodedBytes = 0
				r.recordFields = 1
			}
			return nil
		}
		if r.inQuotes && r.rejectQuotedCR {
			return fmt.Errorf("CSV v3のquoted CRはlossless text encodingを使用してください")
		}
		if err := add(true); err != nil {
			return err
		}
	}
	if r.quotePending {
		r.quotePending = false
		if b == '"' {
			// Doubled quotes are one decoded byte and remain inside the field.
			return add(true)
		}
		r.inQuotes = false
		// The closing quote itself was not decoded. Process the following
		// separator/data byte as an ordinary unquoted byte.
	}
	if r.inQuotes {
		if b == '\r' {
			r.pendingCR = true
			return nil
		}
		if b == '"' {
			r.quotePending = true
			return nil
		}
		return add(true)
	}
	if r.fieldStart && b == '"' {
		r.inQuotes = true
		r.fieldStart = false
		return nil
	}
	if b == ',' || b == '\n' {
		if b == ',' {
			r.recordFields++
			if r.recordFields > maxCSVRecordFields {
				return fmt.Errorf("CSVレコードの列数が上限%dを超えました", maxCSVRecordFields)
			}
		}
		r.fieldBytes = 0
		r.fieldStart = true
		if b == '\n' {
			r.recordBytes = 0
			r.recordDecodedBytes = 0
			r.recordFields = 1
		}
		return nil
	}
	if b == '\r' {
		// Defer CR until the next byte so CRLF is treated exactly as
		// encoding/csv treats it, regardless of reader chunk boundaries.
		r.pendingCR = true
		return nil
	}
	r.fieldStart = false
	return add(true)
}

// csvTotalLimitReader enforces the complete input limit across the format
// probe and the replayed parser. It permits an exact-limit EOF, but reports a
// sentinel error as soon as one byte beyond the limit is observed.
type csvTotalLimitReader struct {
	input     io.Reader
	remaining int64
}

func (r *csvTotalLimitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var extra [1]byte
		n, err := r.input.Read(extra[:])
		if n > 0 {
			return 0, fmt.Errorf("CSV入力が上限%d bytesを超えました", MaxCSVImportBytes)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.input.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (w *csvLimitedStringWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.bytes {
		return 0, fmt.Errorf("CSV出力が上限%d bytesを超えました", w.limit)
	}
	if w.dst != nil {
		n, err := w.dst.Write(p)
		w.bytes += int64(n)
		if err != nil {
			return n, err
		}
		if n != len(p) {
			return n, io.ErrShortWrite
		}
		return n, nil
	}
	n, err := w.b.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *csvLimitedStringWriter) String() string { return w.b.String() }

func csvV3Record(values map[string]string) []string {
	record := make([]string, len(csvV3Headers))
	for i, header := range csvV3Headers {
		record[i] = values[header]
	}
	return record
}

func updateCSVV3Digest(digest hash.Hash, record []string) {
	var length [8]byte
	for _, field := range record {
		// A length-prefixed field stream is unambiguous even when values contain
		// delimiters, NULs (which normal ledger validation rejects), or newlines.
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(field))
	}
}

func newCSVV3Manifest(counts map[string]int64, digest hash.Hash) (string, error) {
	manifestCounts := make(map[string]int64, len(csvV3ManifestRecordTypes))
	for _, recordType := range csvV3ManifestRecordTypes {
		manifestCounts[recordType] = counts[recordType]
	}
	manifestCounts[csvV3ManifestRecordType] = 1
	manifest := csvV3Manifest{
		Format:  csvV3ManifestFormat,
		Version: 3,
		Counts:  manifestCounts,
		Digest:  hex.EncodeToString(digest.Sum(nil)),
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("CSV v3 manifest生成エラー: %w", err)
	}
	return string(encoded), nil
}

func decodeCSVV3Manifest(value string) (csvV3Manifest, error) {
	var manifest csvV3Manifest
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest JSONが不正です: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest JSONに余分な入力があります")
		}
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest JSONの終端が不正です: %w", err)
	}
	if manifest.Format != csvV3ManifestFormat || manifest.Version != 3 || len(manifest.Digest) != sha256.Size*2 {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifestの形式またはバージョンが不正です")
	}
	if _, err := hex.DecodeString(manifest.Digest); err != nil {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest digestが不正です")
	}
	if len(manifest.Counts) != len(csvV3ManifestRecordTypes) {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest record countの種類が不正です")
	}
	for _, recordType := range csvV3ManifestRecordTypes {
		count, ok := manifest.Counts[recordType]
		if !ok || count < 0 || count > maxCSVRows {
			return csvV3Manifest{}, fmt.Errorf("CSV v3 manifestの%s countが不正です", recordType)
		}
	}
	if manifest.Counts[csvV3ManifestRecordType] != 1 {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest countは1である必要があります")
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || string(canonical) != value {
		return csvV3Manifest{}, fmt.Errorf("CSV v3 manifest JSONがcanonical形式ではありません")
	}
	return manifest, nil
}

func validateCSVV3ManifestRecordShape(record []string, headers map[string]int) error {
	for _, header := range csvV3Headers {
		if header == csvVersionHeader || header == "record_type" || header == "setting_key" || header == "setting_value" {
			continue
		}
		if value, err := csvV3Get(record, headers, header); err != nil {
			return err
		} else if value != "" {
			return fmt.Errorf("CSV v3 manifestに余分な値があります: %s", header)
		}
	}
	return nil
}

// csvV3RecordAllowedColumns is the schema boundary for each typed row. The
// header is intentionally the complete v3 schema so one parser can be used for
// every record, but fields belonging to another record type must remain empty.
// Without this check an otherwise valid transaction row could smuggle an image
// payload, tag, setting, or relationship through a column that its decoder
// ignores. Such data would be silently lost on restore.
var csvV3RecordAllowedColumns = map[string]map[string]struct{}{
	"transaction": {
		"id": {}, "account": {}, "date": {}, "item": {}, "type": {}, "amount": {}, "balance": {}, "memo": {},
	},
	"transaction_legacy": {
		"id": {}, "account": {}, "date": {}, "item": {}, "type": {}, "amount": {}, "balance": {}, "memo": {},
	},
	"image": {
		"id": {}, "transaction_id": {}, "filename": {}, "mime_type": {}, "data_base64": {}, "created_at": {},
	},
	"tag": {
		"id": {}, "tag_name": {}, "tag_parent_id": {}, "tag_level": {},
	},
	"tag_legacy": {
		"id": {}, "tag_name": {}, "tag_parent_id": {}, "tag_level": {},
	},
	"transaction_tag": {
		"transaction_id": {}, "tag_id": {},
	},
	"transaction_link": {
		"parent_id": {}, "child_id": {},
	},
	"setting": {
		"setting_key": {}, "setting_value": {},
	},
	"setting_legacy": {
		"setting_key": {}, "setting_value": {},
	},
}

func validateCSVV3RecordShape(record []string, headers map[string]int, recordType string) error {
	allowed, ok := csvV3RecordAllowedColumns[recordType]
	if !ok {
		return fmt.Errorf("未対応のrecord_typeです: %s", recordType)
	}
	for _, header := range csvV3Headers {
		if header == csvVersionHeader || header == "record_type" {
			continue
		}
		if _, ok := allowed[header]; ok {
			continue
		}
		value, err := csvV3Get(record, headers, header)
		if err != nil {
			return err
		}
		if value != "" {
			return fmt.Errorf("CSV v3 %s行に許可されていない値があります: %s", recordType, header)
		}
	}
	return nil
}

// csvV3RawTextPrefix is intentionally an otherwise-invalid control-prefixed
// value. ValidateLedgerText and ValidateArchivedLedgerText reject that control
// byte, so it cannot collide with an existing persisted ledger value. Encoding
// the complete UTF-8 payload makes CR, CRLF, and literal backslashes lossless
// across encoding/csv's newline normalization while leaving old v3 rows (which
// have no marker) readable.
const csvV3RawTextPrefix = "\x01omni-money-csv-v3-text:"

func csvV3Text(value string) string {
	if strings.ContainsRune(value, '\r') {
		value = csvV3RawTextPrefix + base64.RawStdEncoding.EncodeToString([]byte(value))
	}
	return encodeCSVTextCell(value)
}

func decodeCSVV3TextCell(value string) (string, error) {
	decoded, err := decodeCSVTextCellV2(value)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(decoded, csvV3RawTextPrefix) {
		return decoded, nil
	}
	encoded := strings.TrimPrefix(decoded, csvV3RawTextPrefix)
	if encoded == "" {
		return "", fmt.Errorf("CSV v3テキストエンコーディングが空です")
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil || !utf8.Valid(raw) {
		return "", fmt.Errorf("CSV v3テキストエンコーディングが不正です")
	}
	return string(raw), nil
}

func validateCSVV3Setting(key, value string) error {
	if key != "credit_card_items" && key != "bank_account_items" {
		return fmt.Errorf("未対応のledger設定キーです: %s", key)
	}
	if err := validation.ValidateLedgerText("設定キー", key, maxCSVSettingKeyBytes, true); err != nil {
		return err
	}
	if len([]byte(value)) > maxCSVSettingValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("設定値が大きすぎるかUTF-8ではありません")
	}
	if _, err := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingStrict, maxCSVSettingValueBytes, validation.MaxSettingItems); err != nil {
		return err
	}
	return nil
}

func validateCSVV3ArchivedSetting(key, value string) error {
	if key != "credit_card_items" && key != "bank_account_items" {
		return fmt.Errorf("未対応のledger設定キーです: %s", key)
	}
	if err := validation.ValidateLedgerText("設定キー", key, maxCSVSettingKeyBytes, true); err != nil {
		return err
	}
	if len([]byte(value)) > maxCSVSettingValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("設定値が大きすぎるかUTF-8ではありません")
	}
	if _, err := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingArchive, maxCSVSettingValueBytes, validation.MaxSettingItems); err != nil {
		return err
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

func backupToCSVV3In(ctx context.Context, tx *sql.Tx, dst io.Writer) (string, error) {
	output := &csvLimitedStringWriter{dst: dst, limit: maxCSVExportBytes}
	writer := csv.NewWriter(output)
	if err := writer.Write(csvV3Headers); err != nil {
		return "", fmt.Errorf("CSV v3ヘッダー書き出しエラー: %w", err)
	}
	digest := sha256.New()
	recordCounts := make(map[string]int64)
	exportedRows := 0
	write := func(values map[string]string) error {
		// Reserve one row for the mandatory completion manifest. An export that
		// cannot carry its manifest is rejected before emitting a seemingly
		// restorable prefix.
		if exportedRows >= maxCSVRows-1 {
			return fmt.Errorf("CSV v3行数が上限%dを超えるため、復元不能なバックアップを作成できません", maxCSVRows)
		}
		record := csvV3Record(values)
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("CSV v3行書き出しエラー: %w", err)
		}
		updateCSVV3Digest(digest, record)
		recordCounts[values["record_type"]]++
		exportedRows++
		return nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, account, date, item, type, amount, balance, memo
		FROM transactions ORDER BY date, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3取引取得エラー: %w", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
		var id, amount, balance int64
		var account, date, item, txType, memo string
		if err := rows.Scan(&id, &account, &date, &item, &txType, &amount, &balance, &memo); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3取引スキャンエラー: %w", err)
		}
		recordType := "transaction"
		if validation.ValidateLedgerText("口座名", account, validation.MaxAccountBytes, true) != nil ||
			validation.ValidateLedgerText("項目", item, validation.MaxItemBytes, true) != nil ||
			validation.ValidateLedgerText("メモ", memo, validation.MaxMemoBytes, false) != nil {
			if err := validation.ValidateArchivedLedgerText("口座名", account, maxCSVFieldBytes, true); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3取引が不正です (id %d): %w", id, err)
			}
			if err := validation.ValidateArchivedLedgerText("項目", item, maxCSVFieldBytes, true); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3取引が不正です (id %d): %w", id, err)
			}
			if err := validation.ValidateArchivedLedgerText("メモ", memo, maxCSVFieldBytes, false); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3取引が不正です (id %d): %w", id, err)
			}
			recordType = "transaction_legacy"
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": recordType, "id": strconv.FormatInt(id, 10),
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

	rows, err = tx.QueryContext(ctx, `SELECT id, transaction_id, filename, mime_type, data, created_at
		FROM transaction_images ORDER BY transaction_id, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3画像取得エラー: %w", err)
	}
	var exportedImageBytes int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
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
		scratchRelease, scratchAvailable := TryAcquireCSVTempBudget(maxCSVImageDecodeScratchBytes)
		if !scratchAvailable {
			_ = rows.Close()
			return "", fmt.Errorf("CSV画像デコード用メモリ上限に達しました")
		}
		prepared, err := prepareDecodedTransactionImageContext(ctx, filename, mimeType, data)
		if err != nil {
			scratchRelease()
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像が不正です (id %d): %w", id, err)
		}
		if int64(len(prepared.data)) > models.MaxImageBytesDatabase-exportedImageBytes {
			scratchRelease()
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3画像の合計サイズが上限を超えました")
		}
		encodedLen := int64(base64.StdEncoding.EncodedLen(len(prepared.data)))
		encodedRelease, encodedAvailable := TryAcquireCSVTempBudget(encodedLen)
		if !encodedAvailable {
			scratchRelease()
			_ = rows.Close()
			return "", fmt.Errorf("CSV画像エンコード用メモリ上限に達しました")
		}
		encoded := base64.StdEncoding.EncodeToString(prepared.data)
		exportedImageBytes += int64(len(prepared.data))
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": "image", "id": strconv.FormatInt(id, 10),
			"transaction_id": strconv.FormatInt(transactionID, 10), "filename": csvV3Text(prepared.filename),
			"mime_type": csvV3Text(prepared.mimeType), "data_base64": encoded,
			"created_at": csvV3Text(createdAt),
		}); err != nil {
			encodedRelease()
			scratchRelease()
			_ = rows.Close()
			return "", err
		}
		// Keep both the decoder working allowance and the encoded field admitted
		// until csv.Writer has consumed the record. Releasing either before Write
		// lets a concurrent operation exceed the process-wide peak budget.
		encoded = ""
		prepared.data = nil
		data = nil
		encodedRelease()
		scratchRelease()
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("CSV v3画像取得エラー: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("CSV v3画像クローズエラー: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `SELECT id, name, parent_id, level, legacy_duplicate FROM tags ORDER BY level, id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3タグ取得エラー: %w", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
		var id, level int64
		var legacyDuplicate int
		var name string
		var parentID sql.NullInt64
		if err := rows.Scan(&id, &name, &parentID, &level, &legacyDuplicate); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3タグスキャンエラー: %w", err)
		}
		parent := ""
		if parentID.Valid {
			parent = strconv.FormatInt(parentID.Int64, 10)
		}
		recordType := "tag"
		canonicalName, nameErr := validation.ValidateTagName(name)
		if legacyDuplicate != 0 || nameErr != nil || canonicalName != name {
			// Tags created before the shared validator was introduced may contain
			// leading/trailing whitespace, path separators, or format characters.
			// Preserve those rows through an explicit archive trust boundary rather
			// than silently changing the user's tag during backup/restore.
			if err := validation.ValidateArchivedLedgerText("タグ名", name, maxCSVFieldBytes, true); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3タグ名が不正です (id %d): %w", id, err)
			}
			recordType = "tag_legacy"
		}
		if !parentID.Valid {
			if err := validation.ValidateTagHierarchy(int(level), nil); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3タグ階層が不正です (id %d): %w", id, err)
			}
		} else {
			var parentLevel int
			if err := tx.QueryRowContext(ctx, "SELECT level FROM tags WHERE id = ?", parentID.Int64).Scan(&parentLevel); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3タグ親取得エラー (id %d): %w", id, err)
			}
			if err := validation.ValidateTagHierarchy(int(level), &parentLevel); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3タグ階層が不正です (id %d): %w", id, err)
			}
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": recordType, "id": strconv.FormatInt(id, 10),
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

	rows, err = tx.QueryContext(ctx, `SELECT transaction_id, tag_id FROM transaction_tags ORDER BY transaction_id, tag_id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3タグ紐付け取得エラー: %w", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
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

	rows, err = tx.QueryContext(ctx, `SELECT parent_id, child_id FROM transaction_links ORDER BY parent_id, child_id`)
	if err != nil {
		return "", fmt.Errorf("CSV v3取引リンク取得エラー: %w", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
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

	rows, err = tx.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN ('credit_card_items', 'bank_account_items') ORDER BY key`)
	if err != nil {
		return "", fmt.Errorf("CSV v3設定取得エラー: %w", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return "", err
		}
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("CSV v3設定スキャンエラー: %w", err)
		}
		recordType := "setting"
		if err := validateCSVV3Setting(key, value); err != nil {
			// Keep settings accepted by the pre-v3 API (notably duplicate and
			// 256-byte entries) lossless under an explicit compatibility row.
			// Unsafe text, malformed JSON, unknown keys, and oversized values
			// still fail closed rather than becoming an archive injection path.
			if archivedErr := validateCSVV3ArchivedSetting(key, value); archivedErr != nil {
				_ = rows.Close()
				return "", fmt.Errorf("CSV v3設定が不正です (%s): %w", key, err)
			}
			recordType = "setting_legacy"
		}
		if err := write(map[string]string{
			csvVersionHeader: csvVersion3, "record_type": recordType,
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	manifestValue, err := newCSVV3Manifest(recordCounts, digest)
	if err != nil {
		return "", err
	}
	manifestRecord := csvV3Record(map[string]string{
		csvVersionHeader: csvVersion3, "record_type": csvV3ManifestRecordType,
		"setting_key": csvV3ManifestKey, "setting_value": manifestValue,
	})
	if err := writer.Write(manifestRecord); err != nil {
		return "", fmt.Errorf("CSV v3 manifest書き出しエラー: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("CSV v3書き出しエラー: %w", err)
	}
	return "", nil
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
	imageTempDir      string
	imageTempDirInfo  os.FileInfo
	imageTempRoot     *os.Root
	imageTempFiles    []csvV3TempFile
	tempReleases      []func()
	hasManifest       bool
}

type csvV3TempFile struct {
	name string
	info os.FileInfo
	file *os.File
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
	dataPath                      string
	// tempRoot/name/info/file pin the exact private spool object created during
	// parsing. Import reads the retained descriptor rather than reopening
	// dataPath by name, and compares the original size and digest immediately
	// before insertion.
	tempRoot   *os.Root
	tempName   string
	tempInfo   os.FileInfo
	tempFile   *os.File
	tempDigest [sha256.Size]byte
}

type csvV3Tag struct {
	id, parentID    int64
	name            string
	level           int
	archiveLegacy   bool
	legacyDuplicate bool
}

func csvV3HeaderMap(headers []string) (map[string]int, error) {
	m := make(map[string]int, len(headers))
	for i, header := range headers {
		if !utf8.ValidString(header) {
			return nil, fmt.Errorf("CSV v3ヘッダーがUTF-8ではありません")
		}
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

// isCSVV3Header recognizes only the official full v3 header emitted by this
// package. A legacy/v2 export may contain application-specific columns such as
// record_type or filename; those are not enough to route it through the strict
// typed parser. Requiring this exact schema prevents a subset from entering
// replace mode and deleting data that the archive cannot represent.
func isCSVV3Header(headers []string) bool {
	if len(headers) != len(csvV3Headers) {
		return false
	}
	for index, expected := range csvV3Headers {
		if headers[index] != expected {
			return false
		}
	}
	return true
}

func hasCSVV3Markers(headers []string) (versionIndex, recordTypeIndex int, ok bool) {
	versionIndex, recordTypeIndex = -1, -1
	for index, raw := range headers {
		switch strings.TrimSpace(raw) {
		case csvVersionHeader:
			versionIndex = index
		case "record_type":
			recordTypeIndex = index
		}
	}
	return versionIndex, recordTypeIndex, versionIndex >= 0 && recordTypeIndex >= 0
}

// isCSVV3Record is retained for callers that need to inspect a record, but it
// is deliberately not used for import routing: only the official full header
// is sufficient to select the strict v3 parser.
func isCSVV3Record(headers, record []string) bool {
	if isCSVV3Header(headers) {
		return true
	}
	versionIndex, recordTypeIndex, ok := hasCSVV3Markers(headers)
	if !ok || versionIndex >= len(record) || recordTypeIndex >= len(record) {
		return false
	}
	if strings.TrimSpace(record[versionIndex]) != csvVersion3 {
		return false
	}
	switch strings.TrimSpace(record[recordTypeIndex]) {
	case "transaction", "transaction_legacy", "image", "tag", "tag_legacy", "transaction_tag", "transaction_link", "setting", "setting_legacy":
		return true
	default:
		return false
	}
}

func csvV3Get(record []string, headers map[string]int, name string) (string, error) {
	idx, ok := headers[name]
	if !ok {
		return "", nil
	}
	if idx >= len(record) {
		return "", fmt.Errorf("%s列が不足しています", name)
	}
	if len(record[idx]) > maxCSVGuardFieldBytes {
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
	decoded, err := decodeCSVV3TextCell(raw)
	if err != nil {
		return "", fmt.Errorf("%s列のCSVエスケープが不正です: %w", name, err)
	}
	if len([]byte(decoded)) > maxCSVFieldBytes {
		return "", fmt.Errorf("%s列が大きすぎます", name)
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

func (p *csvV3Import) cleanup() error {
	if p == nil {
		return nil
	}
	var first error
	if p.imageTempRoot != nil {
		// Remove exactly the names this parser created, while the retained root
		// still pins the original directory. Never recursively remove a path.
		for _, temp := range p.imageTempFiles {
			if temp.name == "" {
				continue
			}
			if temp.file != nil {
				if err := temp.file.Close(); err != nil && first == nil {
					first = err
				}
				temp.file = nil
			}
			current, err := p.imageTempRoot.Lstat(temp.name)
			if err != nil {
				if !os.IsNotExist(err) && first == nil {
					first = err
				}
				continue
			}
			if temp.info != nil && !os.SameFile(temp.info, current) {
				if first == nil {
					first = fmt.Errorf("CSV画像一時ファイルのidentityが変更されています")
				}
				continue
			}
			if err := p.imageTempRoot.Remove(temp.name); err != nil && !os.IsNotExist(err) && first == nil {
				first = err
			}
		}
		if entries, err := fs.ReadDir(p.imageTempRoot.FS(), "."); err != nil {
			if first == nil {
				first = err
			}
		} else if len(entries) != 0 && first == nil {
			first = fmt.Errorf("CSV画像一時ディレクトリが空ではありません")
		}
		if err := p.imageTempRoot.Close(); err != nil && first == nil {
			first = err
		}
		p.imageTempRoot = nil
		p.imageTempFiles = nil
	}
	if p.imageTempDir != "" {
		// The retained directory identity is the only authority for removing
		// the path after the root handle is closed. Never remove a replacement
		// directory (or a symlink/non-directory) that an attacker placed at the
		// old pathname while cleanup was in progress.
		current, err := os.Lstat(p.imageTempDir)
		sameDirectory := err == nil && p.imageTempDirInfo != nil &&
			current.Mode().IsDir() && current.Mode()&os.ModeSymlink == 0 &&
			os.SameFile(p.imageTempDirInfo, current)
		if err != nil {
			if !os.IsNotExist(err) && first == nil {
				first = err
			}
		} else if !sameDirectory {
			if first == nil {
				first = fmt.Errorf("CSV画像一時ディレクトリのidentityが変更されています")
			}
		} else if err := os.Remove(p.imageTempDir); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		} else if first == nil {
			p.imageTempDir = ""
			p.imageTempDirInfo = nil
		}
	}
	// Drop decoded in-memory image aliases before releasing their admission
	// reservations. Spool-backed rows already clear data during spooling, but
	// the bounded Wails/string compatibility path keeps them here until the DB
	// consumer has completed.
	for index := range p.images {
		p.images[index].data = nil
	}
	for _, release := range p.tempReleases {
		release()
	}
	p.tempReleases = nil
	return first
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
	if int64(len(content)) > MaxCSVStringImportBytes {
		return csvV3Import{}, fmt.Errorf("文字列CSV入力が上限%d bytesを超えました", MaxCSVStringImportBytes)
	}
	return s.parseCSVV3Reader(context.Background(), strings.NewReader(content), false)
}

// parseCSVV3Reader parses directly from a bounded reader. When spoolImages is
// true, decoded image bytes are placed in private 0600 files and released from
// the Go heap before the database phase; this is the server/file import path.
func (s *Service) parseCSVV3Reader(ctx context.Context, input io.Reader, spoolImages bool) (parsed csvV3Import, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	guardedInput := &csvFieldLimitReader{ctx: ctx, input: input, maxFieldBytes: maxCSVGuardFieldBytes, rejectQuotedCR: true, fieldStart: true}
	limitedInput := &io.LimitedReader{R: guardedInput, N: MaxCSVImportBytes + 1}
	reader := csv.NewReader(limitedInput)
	reader.FieldsPerRecord = -1
	headers, err := reader.Read()
	if err != nil {
		return csvV3Import{}, fmt.Errorf("CSV v3ヘッダー読み取りエラー: %w", err)
	}
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
	}
	if !isCSVV3Header(headers) {
		return csvV3Import{}, fmt.Errorf("CSV v3ヘッダーが公式の完全スキーマと一致しません")
	}
	headerMap, err := csvV3HeaderMap(headers)
	if err != nil {
		return csvV3Import{}, err
	}
	parsed = csvV3Import{settings: make(map[string]string)}
	// Keep cleanup ownership independent from the named return value. Many
	// validation failures intentionally return a zero csvV3Import; that would
	// otherwise overwrite the named result before this defer can clean files
	// already created for earlier rows.
	var parseTempDir string
	var parseTempDirInfo os.FileInfo
	var parseTempReleases []func()
	var parseTempRoot *os.Root
	var parseTempFiles []csvV3TempFile
	defer func() {
		if err != nil {
			owner := csvV3Import{
				imageTempDir:     parseTempDir,
				imageTempDirInfo: parseTempDirInfo,
				imageTempRoot:    parseTempRoot,
				imageTempFiles:   parseTempFiles,
				tempReleases:     parseTempReleases,
			}
			// cleanup is identity-safe and nonrecursive. It also has once-guarded
			// reservations, so an earlier explicit cleanup remains harmless. Do not
			// discard a cleanup failure: a parse error must report both the malformed
			// input and any plaintext spool that could not be safely removed.
			if cleanupErr := owner.cleanup(); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("CSV画像一時領域のcleanupに失敗しました: %w", cleanupErr))
			}
		}
	}()
	transactionIDs := make(map[int64]struct{})
	tagIDs := make(map[int64]struct{})
	tagsByID := make(map[int64]csvV3Tag)
	tagNames := make(map[string]struct{})
	imageIDs := make(map[int64]struct{})
	seenTagLinks := make(map[[2]int64]struct{})
	seenTransactionLinks := make(map[[2]int64]struct{})
	recordCounts := make(map[string]int64)
	digest := sha256.New()
	manifestSeen := false
	rowNumber := 1
	for {
		if err := ctx.Err(); err != nil {
			return csvV3Import{}, err
		}
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
		if manifestSeen {
			return csvV3Import{}, fmt.Errorf("CSV v3 manifestは最終行である必要があります (行%d)", rowNumber)
		}
		for _, value := range record {
			if !utf8.ValidString(value) {
				return csvV3Import{}, fmt.Errorf("CSV v3に不正なUTF-8があります (行%d)", rowNumber)
			}
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
		if recordType == csvV3ManifestRecordType {
			if manifestSeen {
				return csvV3Import{}, fmt.Errorf("CSV v3 manifestが重複しています (行%d)", rowNumber)
			}
			if err := validateCSVV3ManifestRecordShape(record, headerMap); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			key, err := csvV3DecodedText(record, headerMap, "setting_key", true)
			if err != nil || key != csvV3ManifestKey {
				if err == nil {
					err = fmt.Errorf("manifest keyが不正です")
				}
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			value, err := csvV3DecodedText(record, headerMap, "setting_value", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			manifest, err := decodeCSVV3Manifest(value)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			manifestSeen = true
			parsed.hasManifest = true
			for _, recordType := range csvV3ManifestRecordTypes {
				if recordType == csvV3ManifestRecordType {
					continue
				}
				if recordCounts[recordType] != manifest.Counts[recordType] {
					return csvV3Import{}, fmt.Errorf("CSV v3 manifestの%s countが一致しません", recordType)
				}
			}
			if manifest.Counts[csvV3ManifestRecordType] != 1 ||
				hex.EncodeToString(digest.Sum(nil)) != manifest.Digest {
				return csvV3Import{}, fmt.Errorf("CSV v3 manifest digestまたはcountが一致しません")
			}
			continue
		}
		if !containsCSVV3ManifestRecordType(recordType) {
			return csvV3Import{}, fmt.Errorf("未対応のrecord_typeです (行%d): %q", rowNumber, recordType)
		}
		if err := validateCSVV3RecordShape(record, headerMap, recordType); err != nil {
			return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
		}
		recordCounts[recordType]++
		updateCSVV3Digest(digest, record)
		switch recordType {
		case "transaction", "transaction_legacy":
			archiveText := recordType == "transaction_legacy"
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
			textValidator := validation.ValidateLedgerText
			if archiveText {
				textValidator = validation.ValidateArchivedLedgerText
			}
			if err := textValidator("口座名", account, func() int {
				if archiveText {
					return maxCSVFieldBytes
				}
				return validation.MaxAccountBytes
			}(), true); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := textValidator("項目", item, func() int {
				if archiveText {
					return maxCSVFieldBytes
				}
				return validation.MaxItemBytes
			}(), true); err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			if err := textValidator("メモ", memo, func() int {
				if archiveText {
					return maxCSVFieldBytes
				}
				return validation.MaxMemoBytes
			}(), false); err != nil {
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
			worstDecoded := int64(base64.StdEncoding.DecodedLen(len(encoded)))
			// DecodedLen is a conservative upper bound. With valid padding it can
			// exceed the exact output by up to two bytes, so do not reject a
			// valid image merely because it lands exactly on the byte quota.
			if worstDecoded <= 0 || worstDecoded > models.MaxImageBytes+2 {
				return csvV3Import{}, fmt.Errorf("画像Base64のデコード後サイズが上限を超えます (行%d)", rowNumber)
			}
			// Admit the possible decoded bytes before DecodeString allocates them.
			// The reservation remains for the parsed image (or its private spool)
			// until import cleanup. Spooling takes a second, short-lived reservation
			// while the decoded heap buffer and file copy coexist.
			imageRelease, available := TryAcquireCSVTempBudget(worstDecoded)
			if !available {
				return csvV3Import{}, fmt.Errorf("CSV画像一時領域が上限に達しました (行%d)", rowNumber)
			}
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || len(data) == 0 {
				imageRelease()
				return csvV3Import{}, fmt.Errorf("画像Base64が不正です (行%d)", rowNumber)
			}
			exactRelease, exactAvailable := ResizeCSVTempBudget(worstDecoded, int64(len(data)))
			if !exactAvailable {
				imageRelease()
				return csvV3Import{}, fmt.Errorf("CSV画像一時領域が上限に達しました (行%d)", rowNumber)
			}
			parsed.tempReleases = append(parsed.tempReleases, exactRelease)
			parseTempReleases = append(parseTempReleases, exactRelease)
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
			decodeScratchRelease, scratchAvailable := TryAcquireCSVTempBudget(maxCSVImageDecodeScratchBytes)
			if !scratchAvailable {
				return csvV3Import{}, fmt.Errorf("画像デコード用メモリ上限に達しました (行%d)", rowNumber)
			}
			prepared, err := prepareDecodedTransactionImageContext(ctx, filename, mimeType, data)
			decodeScratchRelease()
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
			image := csvV3Image{id: id, transactionID: txID, filename: prepared.filename, mimeType: prepared.mimeType, createdAt: createdAt, data: prepared.data}
			if spoolImages {
				if parsed.imageTempDir == "" {
					dir, imageRoot, err := fileprivacy.CreatePrivateTempDir("omni-money-csv-images-")
					if err != nil {
						return csvV3Import{}, fmt.Errorf("画像一時領域の作成に失敗しました: %w", err)
					}
					// Register the owner before any subsequent validation can fail.
					// The defer below can therefore close and clean this root even
					// when the function returns a zero result value.
					parseTempDir = dir
					parseTempRoot = imageRoot
					dirInfo, statErr := os.Lstat(dir)
					if statErr != nil || !dirInfo.Mode().IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
						return csvV3Import{}, fmt.Errorf("画像一時領域のidentity取得に失敗しました")
					}
					parsed.imageTempDir = dir
					parsed.imageTempDirInfo = dirInfo
					parseTempDirInfo = dirInfo
					parsed.imageTempRoot = imageRoot
				}
				// During the write, decoded bytes remain in the Go heap while an
				// identical private-file copy is live. The persistent admission above
				// covers the eventual private file; reserve a second worst-case copy
				// only for this short write window.
				tempRelease, available := TryAcquireCSVTempBudget(int64(len(data)))
				if !available {
					return csvV3Import{}, fmt.Errorf("CSV画像一時領域が上限に達しました")
				}
				parsed.tempReleases = append(parsed.tempReleases, tempRelease)
				parseTempReleases = append(parseTempReleases, tempRelease)
				name := fmt.Sprintf("%d.bin", id)
				path := filepath.Join(parsed.imageTempDir, name)
				file, err := fileprivacy.CreateExclusive(parsed.imageTempRoot, parsed.imageTempDir, name)
				if err != nil {
					return csvV3Import{}, fmt.Errorf("画像一時ファイルの作成に失敗しました: %w", err)
				}
				// Register the descriptor before any inspection or hardening can
				// fail.  A newly-created but not-yet-statted file must still be
				// removed through the retained root on every early return; a
				// pathname-only fallback would leak plaintext or remove a
				// replacement.
				tempFileIndex := len(parsed.imageTempFiles)
				parsed.imageTempFiles = append(parsed.imageTempFiles, csvV3TempFile{name: name, file: file})
				parseTempFiles = parsed.imageTempFiles
				createdInfo, statErr := file.Stat()
				if statErr != nil {
					return csvV3Import{}, fmt.Errorf("画像一時ファイルのidentity取得に失敗しました: %w", statErr)
				}
				parsed.imageTempFiles[tempFileIndex].info = createdInfo
				if err := fileprivacy.Harden(file); err != nil {
					return csvV3Import{}, fmt.Errorf("画像一時ファイルの保護に失敗しました: %w", err)
				}
				if _, err := file.Write(prepared.data); err != nil {
					return csvV3Import{}, fmt.Errorf("画像一時ファイルの書き込みに失敗しました: %w", err)
				}
				tempInfo, err := file.Stat()
				if err != nil || !fileprivacy.IsPrivate(file, tempInfo) || tempInfo.Size() != int64(len(prepared.data)) {
					return csvV3Import{}, fmt.Errorf("画像一時ファイルのidentity取得に失敗しました")
				}
				parsed.imageTempFiles[tempFileIndex].info = tempInfo
				image.dataPath = path
				image.tempRoot = parsed.imageTempRoot
				image.tempName = name
				image.tempInfo = tempInfo
				image.tempFile = file
				image.tempDigest = sha256.Sum256(prepared.data)
				image.data = nil
				// Drop both heap aliases before releasing the short-lived spool
				// reservation. The persistent-file reservation remains owned by
				// parsed.cleanup through validation and SQL INSERT.
				prepared.data = nil
				data = nil
				tempRelease()
			}
			parsed.images = append(parsed.images, image)
		case "tag", "tag_legacy":
			archiveTag := recordType == "tag_legacy"
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
			if archiveTag {
				if err := validation.ValidateArchivedLedgerText("タグ名", name, maxCSVFieldBytes, true); err != nil {
					return csvV3Import{}, fmt.Errorf("タグ名が不正です (行%d): %w", rowNumber, err)
				}
			} else {
				canonicalName, err := validation.ValidateTagName(name)
				if err != nil {
					return csvV3Import{}, fmt.Errorf("タグ名が不正です (行%d): %w", rowNumber, err)
				}
				// Normal writes canonicalize with ValidateTagName. A v3 archive
				// must nevertheless be lossless: reject a non-canonical spelling
				// instead of silently trimming or rewriting it. Existing rows that
				// predate the shared validator are emitted as tag_legacy above.
				if canonicalName != name {
					return csvV3Import{}, fmt.Errorf("タグ名は正規化済みの値で指定してください (行%d)", rowNumber)
				}
			}
			level, err := csvV3Int(record, headerMap, "tag_level", true, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("タグ階層が不正です (行%d)", rowNumber)
			}
			if err := validation.ValidateTagLevel(int(level)); err != nil {
				return csvV3Import{}, fmt.Errorf("タグ階層が不正です (行%d): %w", rowNumber, err)
			}
			parent, err := csvV3Int(record, headerMap, "tag_parent_id", false, true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("タグ親id (行%d): %w", rowNumber, err)
			}
			if parent == 0 {
				if err := validation.ValidateTagHierarchy(int(level), nil); err != nil {
					return csvV3Import{}, fmt.Errorf("タグ階層が不正です (行%d): %w", rowNumber, err)
				}
			}
			row := csvV3Tag{id: id, parentID: parent, name: name, level: int(level), archiveLegacy: archiveTag}
			if parent == 0 {
				if !archiveTag {
					if _, exists := tagNames[name]; exists {
						return csvV3Import{}, fmt.Errorf("同じ階層のタグ名が重複しています (行%d)", rowNumber)
					}
					tagNames[name] = struct{}{}
				}
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
		case "setting", "setting_legacy":
			archiveSetting := recordType == "setting_legacy"
			key, err := csvV3DecodedText(record, headerMap, "setting_key", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			value, err := csvV3DecodedText(record, headerMap, "setting_value", true)
			if err != nil {
				return csvV3Import{}, fmt.Errorf("行%d: %w", rowNumber, err)
			}
			validateSetting := validateCSVV3Setting
			if archiveSetting {
				validateSetting = validateCSVV3ArchivedSetting
			}
			if err := validateSetting(key, value); err != nil {
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
	if err := ctx.Err(); err != nil {
		return csvV3Import{}, err
	}
	if limitedInput.N == 0 {
		return csvV3Import{}, fmt.Errorf("CSV入力が上限%d bytesを超えました", MaxCSVImportBytes)
	}
	// Stable source-ID ordering decides which duplicate remains the normal
	// marker=0 row. This preserves the exact archive order/identity while
	// making a lone tag_legacy behave like an ordinary legacy row.
	rootIndexes := make([]int, 0, len(parsed.tags))
	for index, row := range parsed.tags {
		if row.parentID == 0 {
			rootIndexes = append(rootIndexes, index)
		}
	}
	sort.SliceStable(rootIndexes, func(i, j int) bool {
		return parsed.tags[rootIndexes[i]].id < parsed.tags[rootIndexes[j]].id
	})
	seenRootNames := make(map[string]int, len(rootIndexes))
	for _, index := range rootIndexes {
		row := &parsed.tags[index]
		seen := seenRootNames[row.name]
		row.legacyDuplicate = seen > 0
		seenRootNames[row.name] = seen + 1
	}
	for _, row := range parsed.tags {
		if row.parentID == 0 {
			if err := validation.ValidateTagHierarchy(row.level, nil); err != nil {
				return csvV3Import{}, fmt.Errorf("タグ親なしの階層が不正です: %d: %w", row.id, err)
			}
			continue
		}
		parent, ok := tagsByID[row.parentID]
		if !ok {
			return csvV3Import{}, fmt.Errorf("タグ親が見つかりません: %d", row.parentID)
		}
		if err := validation.ValidateTagHierarchy(row.level, &parent.level); err != nil {
			return csvV3Import{}, fmt.Errorf("タグ親の階層が不正です: %d: %w", row.id, err)
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

func loadCSVV3Settings(ctx context.Context, tx *sql.Tx, incoming map[string]string, replace bool) (map[string]string, error) {
	settings := make(map[string]string)
	if !replace {
		rows, err := tx.QueryContext(ctx, "SELECT key, value FROM settings WHERE key IN ('credit_card_items', 'bank_account_items')")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, err := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingArchive, maxCSVSettingValueBytes, validation.MaxSettingItems); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("既存のledger設定が不正です (%s): %w", key, err)
			}
			settings[key] = value
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("既存のledger設定読み取りエラー: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("既存のledger設定クローズエラー: %w", err)
		}
	}
	for key, value := range incoming {
		if !replace {
			if existing, exists := settings[key]; exists && existing != value {
				return nil, fmt.Errorf("appendでは既存のledger設定キーを異なる値で上書きできません: %s", key)
			}
		}
		settings[key] = value
	}
	return settings, nil
}

func csvV3AccountSettings(settings map[string]string, key string) map[string]bool {
	set := make(map[string]bool)
	var raw []string
	if value, ok := settings[key]; ok {
		if parsed, err := validation.ParseLedgerSettingItemsWithMode(value, validation.LedgerSettingArchive, maxCSVSettingValueBytes, validation.MaxSettingItems); err == nil {
			raw = parsed.Items
		}
		set = stringSet(raw)
	}
	return set
}

type csvImageUsage struct {
	count int64
	bytes int64
}

func csvV3ImageBytes(row csvV3Image) (int64, error) {
	if row.dataPath == "" {
		return int64(len(row.data)), nil
	}
	file, err := openCSVTempImage(row)
	if err != nil {
		return 0, err
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !fileprivacy.IsPrivate(file, info) || info.Size() <= 0 || info.Size() > models.MaxImageBytes {
		return 0, fmt.Errorf("画像一時ファイルが不正です")
	}
	return info.Size(), nil
}

func readCSVTempImage(row csvV3Image) ([]byte, func(), error) {
	file, err := openCSVTempImage(row)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !fileprivacy.IsPrivate(file, info) || info.Size() <= 0 || info.Size() > models.MaxImageBytes {
		return nil, nil, fmt.Errorf("画像一時ファイルが不正です")
	}
	// The persistent-file admission is retained by parsed.cleanup. Charge the
	// decoded heap copy while it is read and consumed by the INSERT as well.
	// The caller owns this release and must not release it until ExecContext has
	// returned; otherwise another import can allocate into the live INSERT
	// buffer and exceed the weighted process budget.
	release, available := TryAcquireCSVTempBudget(info.Size())
	if !available {
		return nil, nil, fmt.Errorf("CSV画像一時領域が上限に達しました")
	}
	data, err := io.ReadAll(io.LimitReader(file, models.MaxImageBytes+1))
	if err != nil {
		release()
		return nil, nil, err
	}
	if len(data) == 0 || int64(len(data)) > models.MaxImageBytes || int64(len(data)) != row.tempInfo.Size() {
		release()
		return nil, nil, fmt.Errorf("画像一時ファイルのサイズが不正です")
	}
	if sha256.Sum256(data) != row.tempDigest {
		release()
		return nil, nil, fmt.Errorf("画像一時ファイルの内容が検証後に変更されています")
	}
	return data, release, nil
}

// openCSVTempImage uses the root and name retained by the parser. The
// creation-time identity is checked again immediately before reading, so a
// same-account rename/replacement cannot substitute bytes after validation.
func openCSVTempImage(row csvV3Image) (*os.File, error) {
	if row.tempRoot == nil || row.tempName == "" || row.tempInfo == nil || row.tempFile == nil {
		return nil, fmt.Errorf("画像一時ファイルのidentity情報がありません")
	}
	entry, err := row.tempRoot.Lstat(row.tempName)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(row.tempInfo, entry) {
		return nil, fmt.Errorf("画像一時ファイルのidentityが変更されています")
	}
	file := row.tempFile
	opened, err := file.Stat()
	if err != nil || !fileprivacy.IsPrivate(file, opened) || !opened.Mode().IsRegular() || !os.SameFile(row.tempInfo, opened) || opened.Size() != row.tempInfo.Size() || !os.SameFile(opened, entry) {
		return nil, fmt.Errorf("画像一時ファイルのidentity検証に失敗しました")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, opened.Size()); err != nil {
		return nil, fmt.Errorf("画像一時ファイルの内容検証に失敗しました: %w", err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	if digest != row.tempDigest {
		return nil, fmt.Errorf("画像一時ファイルの内容が検証後に変更されています")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return file, nil
}

// openCSVTempRead opens an image spool through a directory handle rather than
// resolving an arbitrary path. The handle and directory entry are compared so
// a replaced file, symlink, FIFO, or device cannot be read during import.
func openCSVTempRead(path string) (*os.File, *os.Root, error) {
	dir, name := filepath.Split(path)
	if dir == "" || name == "" || filepath.Base(name) != name {
		return nil, nil, fmt.Errorf("画像一時ファイル名が不正です")
	}
	root, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return nil, nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	entry, entryErr := root.Lstat(name)
	if statErr != nil || entryErr != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, entry) {
		_ = file.Close()
		_ = root.Close()
		return nil, nil, fmt.Errorf("画像一時ファイルのidentity検証に失敗しました")
	}
	return file, root, nil
}

// validateCSVV3ImageQuota performs one bounded aggregate check per affected
// transaction/account/database. It avoids invoking three SUM queries for every
// image, which would become quadratic for a large valid archive.
func validateCSVV3ImageQuota(ctx context.Context, tx *sql.Tx, parsed csvV3Import, transactionMap map[int64]int64) error {
	perTransaction := make(map[int64]csvImageUsage)
	perAccount := make(map[string]csvImageUsage)
	accounts := make(map[int64]string)
	for _, row := range parsed.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		newTxID, ok := transactionMap[row.transactionID]
		if !ok {
			return fmt.Errorf("CSV v3画像の取引が見つかりません: %d", row.transactionID)
		}
		bytes, err := csvV3ImageBytes(row)
		if err != nil {
			return fmt.Errorf("CSV v3画像サイズ取得エラー: %w", err)
		}
		usage := perTransaction[newTxID]
		usage.count++
		usage.bytes += bytes
		perTransaction[newTxID] = usage
		account, exists := accounts[newTxID]
		if !exists {
			if err := tx.QueryRowContext(ctx, "SELECT account FROM transactions WHERE id = ?", newTxID).Scan(&account); err != nil {
				return err
			}
			accounts[newTxID] = account
		}
		accountUsage := perAccount[account]
		accountUsage.count++
		accountUsage.bytes += bytes
		perAccount[account] = accountUsage
	}
	for transactionID, usage := range perTransaction {
		var count, bytes int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(length(data)), 0) FROM transaction_images WHERE transaction_id = ?", transactionID).Scan(&count, &bytes); err != nil {
			return err
		}
		if count+usage.count > int64(models.MaxImagesPerTransaction) || bytes+usage.bytes > models.MaxImageBytesPerTransaction {
			return fmt.Errorf("画像クォータが1取引の上限を超えました")
		}
	}
	for account, usage := range perAccount {
		var bytes int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(ti.data)), 0)
			FROM transaction_images ti JOIN transactions t ON t.id = ti.transaction_id WHERE t.account = ?`, account).Scan(&bytes); err != nil {
			return err
		}
		if bytes+usage.bytes > models.MaxImageBytesPerAccount {
			return fmt.Errorf("口座「%s」の画像保存量が上限を超えました", account)
		}
	}
	var databaseBytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(data)), 0) FROM transaction_images").Scan(&databaseBytes); err != nil {
		return err
	}
	if databaseBytes+parsed.decodedImageBytes > models.MaxImageBytesDatabase {
		return fmt.Errorf("DB全体の画像保存量が上限を超えました")
	}
	return nil
}

func (s *Service) importCSVV3(content, mode string) (int, error) {
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	parsed, err := s.parseCSVV3(content)
	if err != nil {
		return 0, err
	}
	count, importErr := s.importCSVV3Parsed(context.Background(), &parsed, mode)
	if cleanupErr := parsed.cleanup(); cleanupErr != nil {
		return 0, errors.Join(importErr, fmt.Errorf("CSV画像一時領域のcleanupに失敗しました: %w", cleanupErr))
	}
	return count, importErr
}

// ImportCSVReaderContext is the file/HTTP streaming entrypoint. v3 is parsed
// from the reader and image payloads are spooled one-at-a-time to private
// files, so the complete Base64 archive is never duplicated in memory.
func (s *Service) ImportCSVReaderContext(ctx context.Context, input io.Reader, mode string) (int, error) {
	if input == nil {
		return 0, fmt.Errorf("CSV入力がありません")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var release func()
	if !HasCSVImportReservation(ctx) {
		var ok bool
		release, ok = TryAcquireCSVOperationSlot()
		if !ok {
			return 0, fmt.Errorf("CSV入出力が混雑しています。しばらくしてから再試行してください")
		}
		defer release()
	}
	buffered := bufio.NewReader(input)
	firstLine, err := readCSVHeaderLine(buffered)
	if err != nil {
		return 0, err
	}
	source := io.MultiReader(strings.NewReader(firstLine), buffered)
	// The header is already bounded by readCSVHeaderLine. Parse only that
	// record for format selection; replaying a first data record into a
	// bytes.Buffer would duplicate an attacker-controlled field before the
	// bounded parser enforces its record budget. Only the official full v3
	// header selects the typed parser; legacy/v2 files with an application
	// column named record_type remain on the compatibility path.
	headerReader := csv.NewReader(strings.NewReader(firstLine))
	headerReader.FieldsPerRecord = -1
	header, headerErr := headerReader.Read()
	hasV3RecordType := false
	if headerErr == nil {
		if len(header) > 0 {
			header[0] = strings.TrimPrefix(header[0], "\ufeff")
		}
		hasV3RecordType = isCSVV3Header(header)
	}
	// The first physical line is replayed exactly once. The total limiter covers
	// this line and the unread stream, so format selection cannot reset the
	// archive limit or create a second full input copy.
	stream := &csvTotalLimitReader{
		input:     source,
		remaining: MaxCSVImportBytes,
	}
	if hasV3RecordType {
		parsed, parseErr := s.parseCSVV3Reader(ctx, stream, true)
		if parseErr != nil {
			return 0, parseErr
		}
		count, importErr := s.importCSVV3Parsed(ctx, &parsed, mode)
		if cleanupErr := parsed.cleanup(); cleanupErr != nil {
			return 0, errors.Join(importErr, fmt.Errorf("CSV画像一時領域のcleanupに失敗しました: %w", cleanupErr))
		}
		return count, importErr
	}
	if mode == "replace" {
		return 0, ErrCSVReplaceRequiresV3
	}
	// Legacy/v2 remains source-compatible for append imports. Full replace is
	// intentionally v3-only because v1/v2 cannot describe extension data;
	// the raw archive is never materialized as a second string (the separate
	// JSON/string compatibility path remains bounded at 64 MiB).
	return s.importCSVLegacyReaderContext(ctx, stream, mode)
}

type csvLegacyImportRow struct {
	account string
	date    time.Time
	item    string
	txType  string
	amount  int64
	memo    string
}

// importCSVLegacyReaderContext is the bounded append-only compatibility path
// for native files and HTTP uploads. It parses rows as they arrive and
// retains only the validated columns needed for the DB transaction; the raw
// 512 MiB archive is never copied into a second string.
func (s *Service) importCSVLegacyReaderContext(ctx context.Context, input io.Reader, mode string) (int, error) {
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	if mode == "replace" {
		return 0, ErrCSVReplaceRequiresV3
	}
	guarded := &csvFieldLimitReader{ctx: ctx, input: input, maxFieldBytes: maxCSVGuardFieldBytes, fieldStart: true}
	// This entrypoint is used by raw HTTP/Desktop readers. Their wire contract
	// is the 512 MiB bounded stream; only the JSON/string compatibility path is
	// constrained to MaxCSVStringImportBytes in importCSVContext.
	limited := &io.LimitedReader{R: guarded, N: MaxCSVImportBytes + 1}
	reader := csv.NewReader(limited)
	reader.FieldsPerRecord = -1
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
	for i, header := range headers {
		name := strings.TrimSpace(header)
		if name == "" {
			return 0, fmt.Errorf("CSVヘッダーが空です")
		}
		if _, exists := headerMap[name]; exists {
			return 0, fmt.Errorf("CSVヘッダーが重複しています: %s", name)
		}
		headerMap[name] = i
	}
	versionIndex, versionedCSV := headerMap[csvVersionHeader]
	for _, required := range []string{"account", "date", "item", "type", "amount"} {
		if _, ok := headerMap[required]; !ok {
			return 0, fmt.Errorf("必須ヘッダーが不足: %s", required)
		}
	}
	rows := make([]csvLegacyImportRow, 0, 128)
	var parsedTextBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		rowNumber := len(rows) + 2
		if readErr != nil {
			return 0, fmt.Errorf("CSV行読み取りエラー (行%d): %w", rowNumber, readErr)
		}
		for _, value := range record {
			if !utf8.ValidString(value) {
				return 0, fmt.Errorf("CSVに不正なUTF-8があります (行%d)", rowNumber)
			}
		}
		if len(rows) >= maxCSVRows {
			return 0, fmt.Errorf("CSV行数が上限%dを超えました", maxCSVRows)
		}
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
			version := strings.TrimSpace(record[versionIndex])
			if version != csvVersion2 {
				return 0, fmt.Errorf("未対応のCSVバージョンです (行%d): %q", rowNumber, version)
			}
		}
		account, err := field("account")
		if err != nil {
			return 0, err
		}
		dateString, err := field("date")
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
		amountString, err := field("amount")
		if err != nil {
			return 0, err
		}
		memo := ""
		if idx, ok := headerMap["memo"]; ok && idx < len(record) {
			memo = record[idx]
		}
		if versionedCSV {
			for name, value := range map[string]*string{
				"account": &account, "date": &dateString, "item": &item,
				"type": &txType, "memo": &memo,
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
		dateString = strings.TrimSpace(dateString)
		txType = strings.ToLower(strings.TrimSpace(txType))
		amountString = strings.TrimSpace(amountString)
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
		amount, err := strconv.ParseInt(amountString, 10, 64)
		if err != nil || amount <= 0 {
			return 0, fmt.Errorf("金額は正の整数である必要があります (行%d)", rowNumber)
		}
		if err := validation.ValidateTransactionAmount(amount); err != nil {
			return 0, fmt.Errorf("金額が不正です (行%d): %w", rowNumber, err)
		}
		date, err := parseDateStrict(dateString)
		if err != nil {
			return 0, fmt.Errorf("日付形式が正しくありません (行%d): %w", rowNumber, err)
		}
		additional := int64(len(account) + len(dateString) + len(item) + len(txType) + len(memo))
		if additional > maxCSVParsedTextBytes-parsedTextBytes {
			return 0, fmt.Errorf("CSV解析済みテキスト合計が上限を超えました")
		}
		parsedTextBytes += additional
		rows = append(rows, csvLegacyImportRow{account: account, date: date, item: item, txType: txType, amount: amount, memo: memo})
	}
	if limited.N == 0 {
		return 0, fmt.Errorf("CSV入力が上限%d bytesを超えました", MaxCSVImportBytes)
	}
	return s.importCSVLegacyRowsContext(ctx, rows, mode)
}

func (s *Service) importCSVLegacyRowsContext(ctx context.Context, rows []csvLegacyImportRow, mode string) (int, error) {
	if mode == "replace" {
		return 0, ErrCSVReplaceRequiresV3
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)")
	if err != nil {
		return 0, fmt.Errorf("プリペアドステートメントエラー: %w", err)
	}
	affectedAccounts := make(map[string]struct{})
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			_ = stmt.Close()
			return 0, err
		}
		if _, err := stmt.ExecContext(ctx, row.account, row.date, row.item, row.txType, row.amount, row.memo); err != nil {
			_ = stmt.Close()
			return 0, fmt.Errorf("CSVインポートエラー (行%d): %w", index+2, err)
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
	return len(rows), nil
}

// readCSVHeaderLine bounds the format probe itself.  ReadString would keep
// growing an attacker-controlled first line before parseCSVV3Reader gets a
// chance to enforce the archive limit.
func readCSVHeaderLine(input *bufio.Reader) (string, error) {
	if input == nil {
		return "", fmt.Errorf("CSV入力が空です")
	}
	var line strings.Builder
	line.Grow(256)
	for {
		part, err := input.ReadSlice('\n')
		if len(part) > maxCSVHeaderBytes-line.Len() {
			return "", fmt.Errorf("CSVヘッダーが大きすぎます")
		}
		if len(part) > 0 {
			_, _ = line.Write(part)
		}
		if err == nil {
			return line.String(), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if line.Len() == 0 {
				return "", fmt.Errorf("CSV入力が空です")
			}
			return line.String(), nil
		}
		return "", fmt.Errorf("CSVヘッダー読み取りエラー: %w", err)
	}
}

// ImportCSVFileContext is the Desktop/native file path. It keeps the file
// descriptor and image spools bounded while retaining ImportCSV(string, ...)
// as a compatibility binding for existing Wails clients.
func (s *Service) ImportCSVFileContext(ctx context.Context, path, mode string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("CSVファイルが選択されていません")
	}
	file, root, err := openCSVTempRead(path)
	if err != nil {
		return 0, fmt.Errorf("CSVファイルを開けません: %w", err)
	}
	defer file.Close()
	defer root.Close()
	if info, err := file.Stat(); err != nil {
		return 0, err
	} else if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("CSVパスは通常ファイルで指定してください")
	} else if info.Size() > MaxCSVImportBytes {
		return 0, fmt.Errorf("CSV入力が上限%d bytesを超えました", MaxCSVImportBytes)
	}
	return s.ImportCSVReaderContext(ctx, file, mode)
}

func (s *Service) importCSVV3Parsed(ctx context.Context, parsed *csvV3Import, mode string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode != "append" && mode != "replace" {
		return 0, fmt.Errorf("インポートモードはappendまたはreplaceで指定してください")
	}
	if parsed == nil {
		return 0, fmt.Errorf("CSV v3解析結果がありません")
	}
	if !parsed.hasManifest {
		return 0, fmt.Errorf("CSV v3 importには完全性manifestが必要です")
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("CSV v3 transaction開始エラー: %w", err)
	}
	defer tx.Rollback()
	if mode == "replace" {
		// Keep this list explicit. In particular, settings outside the two
		// ledger keys are owned by other features and must survive replace.
		deletes := []struct {
			name  string
			query string
		}{
			{"transaction_links", "DELETE FROM transaction_links"},
			{"transaction_tags", "DELETE FROM transaction_tags"},
			{"transaction_images", "DELETE FROM transaction_images"},
			{"transactions", "DELETE FROM transactions"},
			{"tags", "DELETE FROM tags"},
			{"credit_card_items", "DELETE FROM settings WHERE key = 'credit_card_items'"},
			{"bank_account_items", "DELETE FROM settings WHERE key = 'bank_account_items'"},
			{"ai_transaction_idempotency", "DELETE FROM ai_transaction_idempotency"},
			{"ai_daily_transaction_usage", "DELETE FROM ai_daily_transaction_usage"},
		}
		for _, item := range deletes {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, item.query); err != nil {
				return 0, fmt.Errorf("CSV v3既存データ削除エラー (%s): %w", item.name, err)
			}
		}
	}
	transactionMap := make(map[int64]int64, len(parsed.transactions))
	accounts := make(map[string]struct{})
	for _, row := range parsed.transactions {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, ?)", row.account, row.date, row.item, row.txType, row.amount, row.memo)
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
	settings, err := loadCSVV3Settings(ctx, tx, parsed.settings, mode == "replace")
	if err != nil {
		return 0, fmt.Errorf("CSV v3設定取得エラー: %w", err)
	}
	if len(parsed.settings) > 0 {
		for key, value := range parsed.settings {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
				return 0, fmt.Errorf("CSV v3設定登録エラー: %w", err)
			}
		}
	}
	tagMap := make(map[int64]int64, len(parsed.tags))
	for level := 1; level <= validation.MaxTagLevel; level++ {
		for _, row := range parsed.tags {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
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
			// Archive-only duplicate roots represent distinct historical rows;
			// never merge them into an existing root. Normal rows retain append
			// semantics and reuse a matching tag where one already exists.
			if row.legacyDuplicate {
				lookupErr = sql.ErrNoRows
			} else if row.parentID == 0 {
				lookupErr = tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ? AND parent_id IS NULL AND legacy_duplicate = 0", row.name).Scan(&existing)
			} else {
				lookupErr = tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ? AND parent_id = ?", row.name, newParent).Scan(&existing)
			}
			if lookupErr == nil {
				tagMap[row.id] = existing
				continue
			}
			if lookupErr != sql.ErrNoRows {
				return 0, fmt.Errorf("CSV v3タグ重複確認エラー: %w", lookupErr)
			}
			insertSQL := "INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)"
			if row.legacyDuplicate {
				insertSQL = "INSERT INTO tags (name, parent_id, level, legacy_duplicate) VALUES (?, ?, ?, 1)"
			}
			result, err := tx.ExecContext(ctx, insertSQL, row.name, newParent, row.level)
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
	if err := validateCSVV3ImageQuota(ctx, tx, *parsed, transactionMap); err != nil {
		return 0, fmt.Errorf("CSV v3画像クォータ: %w", err)
	}
	for _, row := range parsed.images {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		newTxID, ok := transactionMap[row.transactionID]
		if !ok {
			return 0, fmt.Errorf("CSV v3画像の取引が見つかりません: %d", row.transactionID)
		}
		data := row.data
		var dataRelease func()
		if row.dataPath != "" {
			data, dataRelease, err = readCSVTempImage(row)
			if err != nil {
				return 0, fmt.Errorf("CSV v3画像一時ファイル読み取りエラー: %w", err)
			}
		}
		if row.createdAt == "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, ?, ?, ?)", newTxID, row.filename, data, row.mimeType)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO transaction_images (transaction_id, filename, data, mime_type, created_at) VALUES (?, ?, ?, ?, ?)", newTxID, row.filename, data, row.mimeType, row.createdAt)
		}
		if err != nil {
			if dataRelease != nil {
				data = nil
				dataRelease()
			}
			return 0, fmt.Errorf("CSV v3画像登録エラー: %w", err)
		}
		if dataRelease != nil {
			// The DB driver has finished consuming data. Release the temporary
			// heap admission exactly once, after the final consumer, not when the
			// file read happens to complete.
			data = nil
			dataRelease()
		}
	}
	for _, pair := range parsed.tagLinks {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		newTxID, ok1 := transactionMap[pair[0]]
		newTagID, ok2 := tagMap[pair[1]]
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("CSV v3タグ紐付けの参照先が見つかりません")
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)", newTxID, newTagID); err != nil {
			return 0, fmt.Errorf("CSV v3タグ紐付け登録エラー: %w", err)
		}
	}
	creditCards := csvV3AccountSettings(settings, "credit_card_items")
	bankAccounts := csvV3AccountSettings(settings, "bank_account_items")
	for _, pair := range parsed.transactionLinks {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		parent, ok1 := transactionMap[pair[0]]
		child, ok2 := transactionMap[pair[1]]
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("CSV v3リンクの参照先が見つかりません")
		}
		// Source links are canonicalized before validation, but ID allocation
		// is independent of source ID order. Re-normalize after remapping so
		// the database invariant is preserved when the target sequence reverses
		// the endpoint ordering.
		if parent > child {
			parent, child = child, parent
		}
		var accountA, accountB string
		if err := tx.QueryRowContext(ctx, "SELECT account FROM transactions WHERE id = ?", parent).Scan(&accountA); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT account FROM transactions WHERE id = ?", child).Scan(&accountB); err != nil {
			return 0, err
		}
		if !isCardWithdrawalLinkAccountsWithSettings(accountA, accountB, creditCards, bankAccounts) {
			return 0, fmt.Errorf("CSV v3リンクはクレジットカード項目と銀行口座項目の取引間でのみ追加できます")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)", parent, child); err != nil {
			return 0, fmt.Errorf("CSV v3リンク登録エラー: %w", err)
		}
	}
	if mode == "replace" {
		// Replace deleted the complete transaction/link set above, so validating
		// the newly imported links is appropriate. Append must preserve every
		// pre-existing link; its incoming links were already checked against the
		// merged settings and must not trigger a global prune.
		if err := pruneInvalidTransactionLinksContext(ctx, tx, settings); err != nil {
			return 0, fmt.Errorf("CSV v3リンク整合性チェックエラー: %w", err)
		}
	}
	for account := range accounts {
		if err := recalculateBalanceInContext(ctx, tx, account); err != nil {
			return 0, fmt.Errorf("CSV v3残高再計算エラー: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// All image descriptors and heap aliases have been consumed by the INSERTs
	// above. Finalize the private spool while the SQL transaction is still
	// pending; if identity-safe cleanup cannot prove removal, rollback instead
	// of returning a successful import while plaintext remains on disk.
	if err := parsed.cleanup(); err != nil {
		return 0, fmt.Errorf("CSV v3画像一時領域のcleanupに失敗しました: %w", err)
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
	return s.ImportCSVWithReservationContext(context.Background(), content, mode)
}

func (s *Service) ImportCSVWithReservationContext(ctx context.Context, content, mode string) (int, error) {
	return s.importCSVContext(ctx, content, mode)
}
