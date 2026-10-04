package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"omni_money/backend/models"
)

var (
	ErrSaveRequestConflict    = errors.New("save request ID was reused for a different operation")
	ErrSaveQueueFull          = errors.New("too many pending transaction saves")
	ErrSaveStorageUnavailable = errors.New("transaction save storage unavailable")
)

const maxSavePayloadBytes = 32 * 1024 * 1024
const maxPendingSaveBytes = 128 * 1024 * 1024
const maxPendingSaves = 32

type TransactionSave struct {
	RequestID   string                      `json:"request_id"`
	State       string                      `json:"state"`
	Transaction *models.TransactionResponse `json:"transaction,omitempty"`
	Error       string                      `json:"error,omitempty"`
	Request     *models.TransactionRequest  `json:"request,omitempty"`
}

type transactionSavePayload struct {
	TargetID    int64                     `json:"target_id"`
	Transaction models.TransactionRequest `json:"transaction"`
}

func ValidSaveRequestID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for index, c := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// AcceptTransactionSave durably receives the complete operation before any
// expensive image decoding. IDs are scoped by this service's user vault.
// Matching contents with different IDs remain independent legitimate saves.
func (s *Service) AcceptTransactionSave(id string, targetID int64, request models.TransactionRequest) (result *TransactionSave, resultErr error) {
	if !ValidSaveRequestID(id) || targetID < 0 {
		return nil, fmt.Errorf("保存要求IDまたは取引IDが無効です")
	}
	if _, err := parseTransactionDate(request.Date, request.Time); err != nil {
		return nil, err
	}
	if err := validateTransactionData(request); err != nil {
		return nil, err
	}
	if targetID == 0 && (len(request.DeleteImageIDs) != 0 || len(request.LinkAddIDs) != 0 || len(request.LinkRemoveIDs) != 0) {
		return nil, fmt.Errorf("新規取引に更新専用の操作は指定できません")
	}
	if len(request.Images) > models.MaxImagesPerTransaction {
		return nil, fmt.Errorf("画像数が上限を超えています")
	}
	payload, err := json.Marshal(transactionSavePayload{TargetID: targetID, Transaction: request})
	if err != nil || len(payload) > maxSavePayloadBytes {
		return nil, fmt.Errorf("保存要求のサイズが上限を超えています")
	}
	hash := sha256.Sum256(payload)
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, ErrSaveRequestConflict) && !errors.Is(resultErr, ErrSaveQueueFull) && !errors.Is(resultErr, ErrServiceUnavailable) {
			resultErr = fmt.Errorf("%w: %v", ErrSaveStorageUnavailable, resultErr)
		}
	}()
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var previousHash []byte
	err = tx.QueryRow("SELECT request_hash FROM transaction_save_requests WHERE request_id = ?", id).Scan(&previousHash)
	if err == nil {
		if !bytes.Equal(previousHash, hash[:]) {
			return nil, ErrSaveRequestConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var count, size int64
		if err := tx.QueryRow("SELECT COUNT(*), COALESCE(SUM(length(payload)), 0) FROM transaction_save_requests WHERE payload IS NOT NULL").Scan(&count, &size); err != nil {
			return nil, err
		}
		if count >= maxPendingSaves || size+int64(len(payload)) > maxPendingSaveBytes {
			return nil, ErrSaveQueueFull
		}
		_, err = tx.Exec("INSERT INTO transaction_save_requests(request_id, target_id, request_hash, payload, state, created_at) VALUES(?, ?, ?, ?, 'pending', ?)", id, targetID, hash[:], payload, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The FULL-synchronous SQL commit above is the receipt boundary. The worker
	// and its lifetime belong to the instance, never to this HTTP request.
	result, err = s.TransactionSaveStatus(id)
	if err != nil {
		return nil, err
	}
	s.instance.WakeLedgerWorker()
	return result, nil
}

func (s *Service) TransactionSaveStatus(id string) (*TransactionSave, error) {
	if !ValidSaveRequestID(id) {
		return nil, sql.ErrNoRows
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	result := &TransactionSave{RequestID: id}
	var response, failure sql.NullString
	if err := db.QueryRow("SELECT state, result, error FROM transaction_save_requests WHERE request_id = ?", id).Scan(&result.State, &response, &failure); err != nil {
		return nil, err
	}
	if response.Valid {
		var transaction models.TransactionResponse
		if err := json.Unmarshal([]byte(response.String), &transaction); err != nil {
			return nil, err
		}
		result.Transaction = &transaction
	}
	result.Error = failure.String
	if result.State == "pending" {
		s.instance.WakeLedgerWorker()
	}
	return result, nil
}

// One instance-owned worker processes requests in receipt order. A crash
// before commit leaves pending; after commit it leaves completed. The ledger
// mutation and result marker are committed in the same SQL transaction.
func (s *Service) processTransactionSaves() {
	for {
		var id string
		var payload []byte
		err := s.db.QueryRow("SELECT request_id, payload FROM transaction_save_requests WHERE state = 'pending' ORDER BY rowid LIMIT 1").Scan(&id, &payload)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			log.Print("transaction save queue read failed; durable requests retained")
			return
		}
		if err := s.processTransactionSave(id, payload); err != nil {
			log.Print("transaction save queue write failed; durable request retained")
			return
		}
	}
}

func (s *Service) failTransactionSave(id string, err error) error {
	// Preserve failed input in the encrypted vault until the owner explicitly
	// dismisses the notice; a processing error must not silently discard input.
	message := "取引を保存できませんでした。入力内容を確認してください"
	if err != nil && strings.Contains(err.Error(), "画像") {
		message = "画像を保存できませんでした。形式と容量を確認してください"
	}
	_, updateErr := s.db.Exec("UPDATE transaction_save_requests SET state = 'failed', error = ? WHERE request_id = ? AND state = 'pending'", message, id)
	return updateErr
}

func (s *Service) TransactionSaveNotices() ([]TransactionSave, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT request_id, state, COALESCE(error, ''), payload FROM transaction_save_requests WHERE payload IS NOT NULL ORDER BY rowid LIMIT 32")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]TransactionSave, 0)
	pending := false
	for rows.Next() {
		var notice TransactionSave
		var raw []byte
		if err := rows.Scan(&notice.RequestID, &notice.State, &notice.Error, &raw); err != nil {
			return nil, err
		}
		var payload transactionSavePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		// Notices carry text metadata only; image contents stay server-side.
		payload.Transaction.Images = nil
		notice.Request = &payload.Transaction
		results = append(results, notice)
		pending = pending || notice.State == "pending"
	}
	if pending {
		s.instance.WakeLedgerWorker()
	}
	return results, rows.Err()
}

func (s *Service) DismissFailedTransactionSave(id string) error {
	if !ValidSaveRequestID(id) {
		return sql.ErrNoRows
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.Exec("UPDATE transaction_save_requests SET payload = NULL WHERE request_id = ? AND state = 'failed'", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) processTransactionSave(id string, raw []byte) error {
	var payload transactionSavePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return s.failTransactionSave(id, err)
	}
	if err := validateTransactionData(payload.Transaction); err != nil {
		return s.failTransactionSave(id, err)
	}
	date, err := parseTransactionDate(payload.Transaction.Date, payload.Transaction.Time)
	if err != nil {
		return s.failTransactionSave(id, err)
	}
	images, err := prepareTransactionImagesContext(context.Background(), payload.Transaction.Images)
	if err != nil {
		return s.failTransactionSave(id, err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow("SELECT state FROM transaction_save_requests WHERE request_id = ?", id).Scan(&state); err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	var response *models.TransactionResponse
	if payload.TargetID == 0 {
		response, err = addPreparedTransactionIn(tx, preparedTransactionInsert{request: payload.Transaction, date: date, images: images})
	} else {
		response, err = updatePreparedTransactionIn(tx, payload.TargetID, payload.Transaction, date, images)
	}
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return rollbackErr
		}
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code != sqlite3.ErrConstraint {
			return err
		}
		return s.failTransactionSave(id, err)
	}
	response.Tags, err = transactionSaveTagsIn(tx, response.ID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(encoded) > maxSavePayloadBytes {
		if err := tx.Rollback(); err != nil {
			return err
		}
		return s.failTransactionSave(id, errors.New("保存結果が上限を超えています"))
	}
	if _, err := tx.Exec("UPDATE transaction_save_requests SET state = 'completed', payload = NULL, result = ? WHERE request_id = ? AND state = 'pending'", string(encoded), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.autoSnapshot()
	return nil
}

func transactionSaveTagsIn(tx *sql.Tx, id int64) ([]models.Tag, error) {
	rows, err := tx.Query("SELECT t.id, t.name, t.parent_id, t.level FROM tags t INNER JOIN transaction_tags tt ON t.id = tt.tag_id WHERE tt.transaction_id = ? ORDER BY t.level, t.name", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make([]models.Tag, 0)
	for rows.Next() {
		var tag models.Tag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.ParentID, &tag.Level); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}
