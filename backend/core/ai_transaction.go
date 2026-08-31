package core

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"omni_money/backend/models"
)

var ErrAIIdempotencyConflict = errors.New("idempotency key was already used for a different request")

// AIDailyQuotaExceededError reports the bounded time until the next UTC quota
// window. It deliberately contains no credential or request data.
type AIDailyQuotaExceededError struct {
	RetryAfterSeconds int
}

func (err *AIDailyQuotaExceededError) Error() string {
	return "AI transaction daily quota exceeded"
}

// AITransactionIdentity contains only fixed-size digests. The raw
// Idempotency-Key is validated and hashed at the HTTP boundary and can never be
// persisted accidentally through this API.
type AITransactionIdentity struct {
	CredentialID          string
	IdempotencyKeySHA256  [32]byte
	RequestSHA256         [32]byte
	MaxTransactionsPerDay int
	Now                   time.Time
}

type AITransactionResult struct {
	Transaction *models.TransactionResponse
	Replayed    bool
}

// AddAITransaction atomically combines idempotency claiming, persistent UTC
// daily quota accounting, and the ledger mutation. A replay never consumes
// quota and never requests another snapshot.
func (s *Service) AddAITransaction(ctx context.Context, req models.TransactionRequest, identity AITransactionIdentity) (*AITransactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	if identity.CredentialID == "" || identity.MaxTransactionsPerDay <= 0 || identity.Now.IsZero() {
		return nil, errors.New("invalid AI transaction identity")
	}

	prepared, err := prepareTransactionInsertContext(ctx, req)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("トランザクション開始エラー: %w", err)
	}
	defer tx.Rollback()

	now := identity.Now.UTC()
	replayed, response, err := claimAIIdempotency(ctx, tx, identity, now)
	if err != nil {
		return nil, err
	}
	if replayed {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("idempotency再送確認エラー: %w", err)
		}
		return &AITransactionResult{Transaction: response, Replayed: true}, nil
	}

	if err := consumeAIDailyQuota(ctx, tx, identity, now); err != nil {
		return nil, err
	}
	response, err = addPreparedTransactionIn(tx, prepared)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_transaction_idempotency
		SET transaction_id = ?, response_account = ?, response_date = ?
		WHERE credential_id = ? AND idempotency_key_sha256 = ? AND transaction_id IS NULL`,
		response.ID,
		response.Account,
		response.Date,
		identity.CredentialID,
		identity.IdempotencyKeySHA256[:],
	)
	if err != nil {
		return nil, fmt.Errorf("idempotency応答保存エラー: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return nil, fmt.Errorf("idempotency応答保存件数が不正です")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("トランザクションコミットエラー: %w", err)
	}

	s.autoSnapshot()
	return &AITransactionResult{Transaction: response}, nil
}

func claimAIIdempotency(ctx context.Context, tx *sql.Tx, identity AITransactionIdentity, now time.Time) (bool, *models.TransactionResponse, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO ai_transaction_idempotency (
		credential_id, idempotency_key_sha256, request_sha256, created_at
	) VALUES (?, ?, ?, ?)
	ON CONFLICT(credential_id, idempotency_key_sha256) DO NOTHING`,
		identity.CredentialID,
		identity.IdempotencyKeySHA256[:],
		identity.RequestSHA256[:],
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, nil, fmt.Errorf("idempotency確保エラー: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, nil, fmt.Errorf("idempotency確保結果エラー: %w", err)
	}
	if inserted == 1 {
		return false, nil, nil
	}
	if inserted != 0 {
		return false, nil, fmt.Errorf("idempotency確保件数が不正です")
	}

	var storedRequestHash []byte
	var transactionID sql.NullInt64
	var account, date sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT request_sha256, transaction_id, response_account, response_date
		FROM ai_transaction_idempotency
		WHERE credential_id = ? AND idempotency_key_sha256 = ?`,
		identity.CredentialID,
		identity.IdempotencyKeySHA256[:],
	).Scan(&storedRequestHash, &transactionID, &account, &date); err != nil {
		return false, nil, fmt.Errorf("idempotency再送取得エラー: %w", err)
	}
	if len(storedRequestHash) != len(identity.RequestSHA256) || subtle.ConstantTimeCompare(storedRequestHash, identity.RequestSHA256[:]) != 1 {
		return false, nil, ErrAIIdempotencyConflict
	}
	if !transactionID.Valid || !account.Valid || !date.Valid {
		return false, nil, errors.New("idempotency metadata is incomplete")
	}
	return true, &models.TransactionResponse{
		ID:           transactionID.Int64,
		Account:      account.String,
		Date:         date.String,
		AmountExact:  "0",
		BalanceExact: "0",
	}, nil
}

func consumeAIDailyQuota(ctx context.Context, tx *sql.Tx, identity AITransactionIdentity, now time.Time) error {
	utcDate := now.Format("2006-01-02")
	result, err := tx.ExecContext(ctx, `INSERT INTO ai_daily_transaction_usage (
		credential_id, utc_date, successful_creates
	) VALUES (?, ?, 1)
	ON CONFLICT(credential_id, utc_date) DO UPDATE SET
		successful_creates = successful_creates + 1
	WHERE successful_creates < ?`,
		identity.CredentialID,
		utcDate,
		identity.MaxTransactionsPerDay,
	)
	if err != nil {
		return fmt.Errorf("AI日次クォータ更新エラー: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("AI日次クォータ結果エラー: %w", err)
	}
	if updated == 1 {
		return nil
	}
	if updated != 0 {
		return fmt.Errorf("AI日次クォータ更新件数が不正です")
	}

	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	remaining := nextDay.Sub(now)
	retryAfter := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		retryAfter++
	}
	if retryAfter < 1 {
		retryAfter = 1
	}
	return &AIDailyQuotaExceededError{RetryAfterSeconds: retryAfter}
}
