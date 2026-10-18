package core

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"omni_money/backend/models"
)

type cardBankAssignments map[int64]map[int64]bool

// cardBankPair gives an edge its business direction; stored parent/child IDs
// are ordered numerically and must never be used to infer that direction.
func cardBankPair(idA int64, accountA string, idB int64, accountB string, cards, banks map[string]bool) (int64, int64, error) {
	accountA, accountB = strings.TrimSpace(accountA), strings.TrimSpace(accountB)
	if (cards[accountA] && banks[accountA]) || (cards[accountB] && banks[accountB]) {
		return 0, 0, fmt.Errorf("紐付け対象のクレジットカードと銀行口座の分類が重複しています")
	}
	forward := cards[accountA] && banks[accountB]
	reverse := banks[accountA] && cards[accountB]
	if forward {
		return idA, idB, nil
	}
	if reverse {
		return idB, idA, nil
	}
	return 0, 0, fmt.Errorf("紐付けはクレジットカード項目と銀行口座項目の取引間でのみ追加できます")
}

func readCardBankAssignments(ctx context.Context, q sqlContextExecutor, settings map[string]string, bankID int64) (cardBankAssignments, error) {
	cards := stringSetFromSetting(settings["credit_card_items"])
	banks := stringSetFromSetting(settings["bank_account_items"])
	query := `SELECT l.parent_id, p.account, l.child_id, c.account
		FROM transaction_links l
		JOIN transactions p ON p.id = l.parent_id
		JOIN transactions c ON c.id = l.child_id`
	var args []interface{}
	if bankID > 0 {
		// Inspect only this bank's cards and their other links. Both endpoint
		// indexes are used; there is no per-card query or whole-ledger scan.
		query = `WITH linked AS (
			SELECT child_id AS id FROM transaction_links WHERE parent_id = ?
			UNION SELECT parent_id FROM transaction_links WHERE child_id = ?
		)
		SELECT l.parent_id, p.account, l.child_id, c.account
		FROM transaction_links l
		JOIN transactions p ON p.id = l.parent_id
		JOIN transactions c ON c.id = l.child_id
		WHERE l.parent_id IN (SELECT id FROM linked) OR l.child_id IN (SELECT id FROM linked)`
		args = []interface{}{bankID, bankID}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make(cardBankAssignments)
	for rows.Next() {
		var idA, idB int64
		var accountA, accountB string
		if err := rows.Scan(&idA, &accountA, &idB, &accountB); err != nil {
			return nil, err
		}
		cardID, bankID, err := cardBankPair(idA, accountA, idB, accountB, cards, banks)
		if err != nil {
			continue // Historical invalid/ambiguous pairs are not silently changed.
		}
		if assignments[cardID] == nil {
			assignments[cardID] = make(map[int64]bool)
		}
		assignments[cardID][bankID] = true
	}
	return assignments, rows.Err()
}

// Existing multi-bank links may be reduced or left untouched while the user
// repairs them. Mutations must not create or extend a multi-bank assignment.
func validateCardBankAssignmentChanges(ctx context.Context, q sqlContextExecutor, settings map[string]string, before cardBankAssignments) error {
	after, err := readCardBankAssignments(ctx, q, settings, 0)
	if err != nil {
		return err
	}
	for cardID, banks := range after {
		if len(banks) <= 1 {
			continue
		}
		for bankID := range banks {
			if !before[cardID][bankID] {
				return fmt.Errorf("クレジットカード取引1件に紐付けられる銀行取引は1件までです")
			}
		}
	}
	return nil
}

func addCardBankLinkIn(ctx context.Context, q sqlContextExecutor, idA, idB int64, cards, banks map[string]bool) error {
	if idA <= 0 || idB <= 0 || idA == idB {
		return fmt.Errorf("同一または無効な取引とは紐付けできません")
	}
	var accountA, accountB string
	if err := q.QueryRowContext(ctx, "SELECT account FROM transactions WHERE id = ?", idA).Scan(&accountA); err != nil {
		return fmt.Errorf("紐付け対象の取引が見つかりません: %w", err)
	}
	if err := q.QueryRowContext(ctx, "SELECT account FROM transactions WHERE id = ?", idB).Scan(&accountB); err != nil {
		return fmt.Errorf("紐付け対象の取引が見つかりません: %w", err)
	}
	cardID, bankID, err := cardBankPair(idA, accountA, idB, accountB, cards, banks)
	if err != nil {
		return err
	}
	p, c := idA, idB
	if p > c {
		p, c = c, p
	}
	var exists int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM transaction_links WHERE parent_id = ? AND child_id = ?", p, c).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil // Retrying an existing edge does not extend a legacy conflict.
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.account FROM transactions t WHERE t.id IN (
		SELECT child_id FROM transaction_links WHERE parent_id = ?
		UNION SELECT parent_id FROM transaction_links WHERE child_id = ?
	)`, cardID, cardID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var existingID int64
		var account string
		if err := rows.Scan(&existingID, &account); err != nil {
			_ = rows.Close()
			return err
		}
		if existingID != bankID && banks[strings.TrimSpace(account)] {
			_ = rows.Close()
			return fmt.Errorf("このクレジットカード取引は別の銀行取引に紐付いています。先に紐付けを解除してください")
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)", p, c)
	return err
}

func scanLinkedTransaction(row interface{ Scan(...interface{}) error }) (models.LinkedTransactionResponse, error) {
	var result models.LinkedTransactionResponse
	var date time.Time
	err := row.Scan(&result.ID, &result.FundItem, &date, &result.Item, &result.Type, &result.Amount, &result.Memo)
	result.AmountExact = models.ExactInt64(result.Amount)
	result.Date = date.Format("2006-01-02")
	if date.Hour() != 0 || date.Minute() != 0 || date.Second() != 0 {
		result.Date = date.Format("2006-01-02 15:04:05")
	}
	return result, err
}

// GetCardReconciliation is specifically for registered card/bank pairs.
// Future general-purpose transaction links must not automatically inherit
// this expense-minus-refund summation or the bank-withdrawal comparison.
// A mismatch is informational and is never a condition for saving a link.
func (s *Service) GetCardReconciliation(bankID int64) (*models.CardReconciliationResponse, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	bank, err := scanLinkedTransaction(tx.QueryRowContext(ctx, `SELECT id, account, date, item, type,
		COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), memo
		FROM transactions WHERE id = ?`, bankID))
	if err != nil {
		return nil, fmt.Errorf("照合対象の銀行取引が見つかりません: %w", err)
	}
	settings, err := loadLedgerSettingsIn(tx)
	if err != nil {
		return nil, err
	}
	cards, banks := stringSetFromSetting(settings["credit_card_items"]), stringSetFromSetting(settings["bank_account_items"])
	result := &models.CardReconciliationResponse{Bank: bank, Cards: []models.LinkedTransactionResponse{}}
	bankAccount := strings.TrimSpace(bank.FundItem)
	if !banks[bankAccount] || cards[bankAccount] {
		return result, nil
	}
	result.Applicable = true
	rows, err := tx.QueryContext(ctx, `SELECT id, account, date, item, type,
		COALESCE((SELECT amount FROM transaction_archive_amounts WHERE transaction_id = transactions.id), amount), memo
		FROM transactions WHERE id IN (
		SELECT child_id FROM transaction_links WHERE parent_id = ?
		UNION SELECT parent_id FROM transaction_links WHERE child_id = ?
		) ORDER BY date, id`, bankID, bankID)
	if err != nil {
		return nil, err
	}
	total := new(big.Int)
	for rows.Next() {
		card, err := scanLinkedTransaction(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		_, _, pairErr := cardBankPair(card.ID, card.FundItem, bank.ID, bank.FundItem, cards, banks)
		if pairErr != nil {
			result.HasConflicts = true
			continue
		}
		amount := big.NewInt(card.Amount)
		if card.Type == "income" {
			amount.Neg(amount)
		}
		total.Add(total, amount)
		result.Cards = append(result.Cards, card)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	assignments, err := readCardBankAssignments(ctx, tx, settings, bankID)
	if err != nil {
		return nil, err
	}
	for index := range result.Cards {
		card := &result.Cards[index]
		card.LinkedBankIDs = []int64{}
		for ownerID := range assignments[card.ID] {
			card.LinkedBankIDs = append(card.LinkedBankIDs, ownerID)
		}
		sort.Slice(card.LinkedBankIDs, func(i, j int) bool { return card.LinkedBankIDs[i] < card.LinkedBankIDs[j] })
		result.HasConflicts = result.HasConflicts || len(card.LinkedBankIDs) > 1
	}
	bankAmount := big.NewInt(bank.Amount)
	if bank.Type == "income" {
		bankAmount.Neg(bankAmount)
	}
	result.CardTotalExact = total.String()
	result.BankAmountExact = bankAmount.String()
	result.DifferenceExact = new(big.Int).Sub(total, bankAmount).String()
	result.Matched = total.Cmp(bankAmount) == 0
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
