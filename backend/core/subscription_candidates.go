package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"omni_money/backend/models"
	"omni_money/backend/validation"
)

type SubscriptionCandidate struct {
	Fingerprint string                     `json:"fingerprint"`
	Interval    string                     `json:"interval"`
	Transaction models.TransactionResponse `json:"transaction"`
	Count       int                        `json:"count"`
}

func subscriptionCandidateKey(account, item, amount, interval string) string {
	value, _ := json.Marshal([]string{account, item, amount, interval})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

// Conservative, local-only suggestions: at least three consecutive payments
// with the same exact yen amount and dates within three days of the original
// day (clamped at month end). Generated/linked payments and bank-side funds
// are excluded. A rejection persists for that exact payment pattern.
func (s *Service) SubscriptionCandidates() ([]SubscriptionCandidate, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rules, err := s.Subscriptions()
	if err != nil {
		return nil, err
	}
	bankAccounts, err := s.GetBankAccountSettings()
	if err != nil {
		return nil, err
	}
	banks := map[string]bool{}
	for _, account := range bankAccounts {
		banks[account] = true
	}
	rows, err := db.Query(`SELECT t.id,t.account,t.date,t.item,t.type,t.amount,t.balance,t.memo FROM transactions t WHERE t.type='expense' AND t.amount<=? AND t.date<=? AND NOT EXISTS(SELECT 1 FROM subscription_occurrences o WHERE o.transaction_id=t.id) AND NOT EXISTS(SELECT 1 FROM transaction_links l WHERE l.parent_id=t.id OR l.child_id=t.id) AND NOT EXISTS(SELECT 1 FROM transaction_archive_amounts a WHERE a.transaction_id=t.id) ORDER BY t.date DESC,t.id DESC LIMIT 5000`, validation.MaxTransactionAmount, time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]models.TransactionResponse{}
	for rows.Next() {
		var t models.Transaction
		var date string
		if err := rows.Scan(&t.ID, &t.Account, &date, &t.Item, &t.Type, &t.Amount, &t.Balance, &t.Memo); err != nil {
			return nil, err
		}
		if banks[t.Account] {
			continue
		}
		covered := false
		for _, rule := range rules {
			if rule.Account == t.Account && rule.Item == t.Item {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		t.Date = parseDate(date)
		key := subscriptionCandidateKey(t.Account, t.Item, strconv.FormatInt(t.Amount, 10), "")
		groups[key] = append(groups[key], t.ToResponse())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	results := []SubscriptionCandidate{}
	for _, group := range groups {
		if len(group) < 3 {
			continue
		}
		interval := ""
		for _, cycle := range []string{"monthly", "yearly"} {
			valid := true
			latest := parseDate(group[0].Date)
			for i := 1; i < 3; i++ {
				date := parseDate(group[i].Date)
				months := (latest.Year()-date.Year())*12 + int(latest.Month()-date.Month())
				expected := i
				if cycle == "yearly" {
					expected = i * 12
				}
				anchor, _ := time.Parse(time.DateOnly, subscriptionScheduledDate(date.Year(), date.Month(), latest.Day()))
				delta := date.Sub(anchor).Hours() / 24
				if months != expected || delta < -3 || delta > 3 {
					valid = false
					break
				}
			}
			if valid {
				interval = cycle
				break
			}
		}
		if interval == "" {
			continue
		}
		latest := group[0]
		fingerprint := subscriptionCandidateKey(latest.Account, latest.Item, latest.AmountExact, interval)
		var rejected int
		err := db.QueryRow("SELECT 1 FROM subscription_dismissals WHERE fingerprint=?", fingerprint).Scan(&rejected)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		results = append(results, SubscriptionCandidate{Fingerprint: fingerprint, Interval: interval, Transaction: latest, Count: len(group)})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Transaction.Date != results[j].Transaction.Date {
			return results[i].Transaction.Date > results[j].Transaction.Date
		}
		return results[i].Fingerprint < results[j].Fingerprint
	})
	if len(results) > 100 {
		results = results[:100]
	}
	for i := range results {
		results[i].Transaction.Tags, err = s.GetTransactionTags(results[i].Transaction.ID)
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *Service) DismissSubscriptionCandidate(fingerprint string) error {
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || len(decoded) != 32 {
		return errors.New("候補IDが無効です")
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := beginLedgerWrite(context.Background(), db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM subscription_dismissals").Scan(&count); err != nil {
		return err
	}
	if count >= 10000 {
		return errors.New("候補の拒否記録が上限に達しました")
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO subscription_dismissals(fingerprint) VALUES(?)", fingerprint); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.autoSnapshot()
	return nil
}
