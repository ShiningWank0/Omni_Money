package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"omni_money/backend/models"
)

const maxSubscriptionRules = 256
const subscriptionBatchSize = 24

var ErrSubscriptionConflict = errors.New("サブスクが変更されています。再読み込みしてください")

// Only static domain validation messages may cross the HTTP boundary. SQL,
// network, and provider errors continue through the existing fixed messages.
type SubscriptionValidationError struct{ message string }

func (e *SubscriptionValidationError) Error() string { return e.message }
func subscriptionInputError(message string) error {
	return &SubscriptionValidationError{message: message}
}

type SubscriptionInput struct {
	SourceTransactionID int64   `json:"source_transaction_id"`
	Revision            int64   `json:"revision"`
	Account             string  `json:"account"`
	Item                string  `json:"item"`
	Memo                string  `json:"memo"`
	Currency            string  `json:"currency"`
	OriginalAmount      string  `json:"original_amount"`
	Interval            string  `json:"interval"`
	AnchorDay           int     `json:"anchor_day"`
	AnchorMonth         int     `json:"anchor_month"`
	Timezone            string  `json:"timezone"`
	NextDate            string  `json:"next_date"`
	Enabled             bool    `json:"enabled"`
	Tags                []int64 `json:"tags"`
}

type SubscriptionRule struct {
	SubscriptionInput
	ID           string `json:"id"`
	NextSequence int64  `json:"-"`
	SourceDate   string `json:"-"`
}

type subscriptionDefinition struct {
	SubscriptionInput
	SourceDate string `json:"source_date"`
}

func subscriptionPeriod(date, interval string) string {
	if interval == "yearly" {
		return date[:4]
	}
	return date[:7]
}

type SubscriptionSync struct {
	Created int      `json:"created"`
	HasMore bool     `json:"has_more"`
	Errors  []string `json:"errors"`
}

func subscriptionDate(value string) (time.Time, error) {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Format(time.DateOnly) != value || date.Year() < 1 || date.Year() > 9998 {
		return time.Time{}, subscriptionInputError("支払日を正しい年月日で指定してください")
	}
	return date, nil
}

func subscriptionScheduledDate(year int, month time.Month, day int) string {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

func subscriptionNextDate(rule SubscriptionRule) (string, error) {
	date, err := subscriptionDate(rule.NextDate)
	if err != nil {
		return "", err
	}
	year, month := date.Year(), date.Month()
	if rule.Interval == "monthly" {
		month++
		if month > 12 {
			month = 1
			year++
		}
	} else {
		year++
		month = time.Month(rule.AnchorMonth)
	}
	if year > 9998 {
		return "", subscriptionInputError("サブスクの年月が上限に達しました")
	}
	return subscriptionScheduledDate(year, month, rule.AnchorDay), nil
}

func subscriptionToday(timezone string, now time.Time) (string, error) {
	if timezone == "" || timezone == "Local" || len(timezone) > 64 {
		return "", subscriptionInputError("タイムゾーンが無効です")
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return "", subscriptionInputError("タイムゾーンが無効です")
	}
	return now.In(zone).Format(time.DateOnly), nil
}

func validateSubscription(input SubscriptionInput) error {
	if input.SourceTransactionID <= 0 || input.Revision < 0 || (input.Interval != "monthly" && input.Interval != "yearly") || input.AnchorDay < 1 || input.AnchorDay > 31 || input.AnchorMonth < 1 || input.AnchorMonth > 12 || len(input.Tags) > 100 {
		return subscriptionInputError("サブスクの設定が無効です")
	}
	date, err := subscriptionDate(input.NextDate)
	if err != nil {
		return err
	}
	if _, err := subscriptionToday(input.Timezone, time.Now()); err != nil {
		return err
	}
	if subscriptionScheduledDate(date.Year(), date.Month(), input.AnchorDay) != input.NextDate || (input.Interval == "yearly" && date.Month() != time.Month(input.AnchorMonth)) {
		return subscriptionInputError("次回支払日を周期の基準日に合わせてください")
	}
	// Validate the original decimal independently of the eventual exchange
	// rate; tiny foreign amounts can legitimately round to a positive yen.
	if !subscriptionCurrencies[input.Currency] || !subscriptionMoneyPattern.MatchString(input.OriginalAmount) || strings.Trim(input.OriginalAmount, "0.") == "" || (input.Currency == "JPY" && strings.Contains(input.OriginalAmount, ".")) {
		return subscriptionInputError("通貨・金額を確認してください")
	}
	if input.Currency == "JPY" {
		if _, err := subscriptionYen(input.OriginalAmount, input.Currency, "1"); err != nil {
			return err
		}
	}
	return validateTransactionData(models.TransactionRequest{Account: input.Account, Item: input.Item, Memo: input.Memo, Type: "expense", Amount: 1})
}

const subscriptionSelect = "SELECT id, revision, enabled, next_sequence, next_date, definition FROM subscription_rules"

type subscriptionScanner interface{ Scan(...any) error }

func scanSubscription(row subscriptionScanner) (SubscriptionRule, error) {
	var rule SubscriptionRule
	var definition string
	var revision, sequence int64
	var id, next string
	var enabled bool
	if err := row.Scan(&id, &revision, &enabled, &sequence, &next, &definition); err != nil {
		return rule, err
	}
	var stored subscriptionDefinition
	if err := json.Unmarshal([]byte(definition), &stored); err != nil {
		return rule, err
	}
	rule.SubscriptionInput, rule.SourceDate = stored.SubscriptionInput, stored.SourceDate
	if _, err := subscriptionDate(rule.SourceDate); err != nil {
		return rule, err
	}
	rule.ID, rule.Revision, rule.Enabled, rule.NextSequence, rule.NextDate = id, revision, enabled, sequence, next
	rule.Tags = []int64{}
	return rule, validateSubscription(rule.SubscriptionInput)
}

func subscriptionTagsIn(ctx context.Context, q csvContextQueryer, id string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT tag_id FROM subscription_rule_tags WHERE rule_id=? ORDER BY tag_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) Subscriptions() ([]SubscriptionRule, error) {
	return s.subscriptionsContext(context.Background())
}

func (s *Service) subscriptionsContext(ctx context.Context) ([]SubscriptionRule, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, subscriptionSelect+" ORDER BY enabled DESC, next_date, id LIMIT 257")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []SubscriptionRule{}
	for rows.Next() {
		rule, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(rules) > maxSubscriptionRules {
		return nil, subscriptionInputError("サブスク数が上限を超えています")
	}
	for i := range rules {
		rules[i].Tags, err = subscriptionTagsIn(ctx, db, rules[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return rules, nil
}

// SaveSubscription creates from an existing manual expense or updates one
// rule using an optimistic revision. Updating a transaction never edits rules.
func (s *Service) SaveSubscription(id string, input SubscriptionInput) (*SubscriptionRule, error) {
	return s.saveSubscriptionAt(id, input, time.Now())
}

func (s *Service) saveSubscriptionAt(id string, input SubscriptionInput, now time.Time) (*SubscriptionRule, error) {
	if input.Tags == nil {
		input.Tags = []int64{}
	}
	if err := validateSubscription(input); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	tx, err := beginLedgerWrite(context.Background(), db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rule := SubscriptionRule{SubscriptionInput: input, ID: id, NextSequence: 1}
	if id == "" {
		var date, txType string
		var generated bool
		if err := tx.QueryRow("SELECT date, type, EXISTS(SELECT 1 FROM subscription_occurrences WHERE transaction_id=transactions.id) FROM transactions WHERE id=?", input.SourceTransactionID).Scan(&date, &txType, &generated); err != nil {
			return nil, err
		}
		if txType != "expense" || generated {
			return nil, subscriptionInputError("手動で登録した支出取引からサブスクを作成してください")
		}
		sourceDate, err := parseDateStrict(date)
		if err != nil {
			return nil, err
		}
		rule.SourceDate = sourceDate.Format(time.DateOnly)
		if subscriptionPeriod(input.NextDate, input.Interval) <= subscriptionPeriod(rule.SourceDate, input.Interval) {
			return nil, subscriptionInputError("元の取引の翌周期以降の次回支払日を指定してください")
		}
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM subscription_rules").Scan(&count); err != nil {
			return nil, err
		}
		if count >= maxSubscriptionRules {
			return nil, subscriptionInputError("サブスクは256件まで登録できます")
		}
		rows, err := tx.Query(subscriptionSelect)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			previous, err := scanSubscription(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if previous.SourceTransactionID == input.SourceTransactionID || (previous.Account == input.Account && previous.Item == input.Item && previous.Currency == input.Currency) {
				rows.Close()
				return nil, subscriptionInputError("同じ支払いのサブスクが登録済みです。管理画面から編集・再開してください")
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		uuid := make([]byte, 16)
		if _, err := rand.Read(uuid); err != nil {
			return nil, err
		}
		uuid[6] = uuid[6]&0x0f | 0x40
		uuid[8] = uuid[8]&0x3f | 0x80
		rule.ID = fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
		rule.Revision = 1
		today, _ := subscriptionToday(input.Timezone, now)
		if input.NextDate < today {
			return nil, subscriptionInputError("新規登録の次回支払日は当日以降を指定してください")
		}
	} else {
		if !ValidSaveRequestID(id) {
			return nil, sql.ErrNoRows
		}
		previous, err := scanSubscription(tx.QueryRow(subscriptionSelect+" WHERE id=?", id))
		if err != nil {
			return nil, err
		}
		if input.Revision != previous.Revision {
			return nil, ErrSubscriptionConflict
		}
		if input.SourceTransactionID != previous.SourceTransactionID {
			return nil, subscriptionInputError("登録元の取引は変更できません")
		}
		rule.Revision, rule.NextSequence = previous.Revision+1, previous.NextSequence
		rule.SourceDate = previous.SourceDate
		scheduleChanged := input.Interval != previous.Interval || input.AnchorDay != previous.AnchorDay || input.AnchorMonth != previous.AnchorMonth || input.NextDate != previous.NextDate || input.Timezone != previous.Timezone || (!previous.Enabled && input.Enabled)
		if scheduleChanged {
			today, _ := subscriptionToday(input.Timezone, now)
			var last string
			if err := tx.QueryRow("SELECT COALESCE(MAX(due_date), '') FROM subscription_occurrences WHERE rule_id=?", id).Scan(&last); err != nil {
				return nil, err
			}
			if last == "" || last < rule.SourceDate {
				last = rule.SourceDate
			}
			if input.NextDate < today || subscriptionPeriod(input.NextDate, input.Interval) <= subscriptionPeriod(last, input.Interval) {
				return nil, subscriptionInputError("次回支払日は当日以降かつ処理済みの支払いの翌周期以降に指定してください")
			}
		}
	}
	if err := validateTagIDsIn(tx, input.Tags); err != nil {
		return nil, err
	}
	definitionInput := rule.SubscriptionInput
	definitionInput.Tags = nil
	definition, err := json.Marshal(subscriptionDefinition{SubscriptionInput: definitionInput, SourceDate: rule.SourceDate})
	if err != nil || len(definition) > 32768 {
		return nil, subscriptionInputError("サブスクの設定が大きすぎます")
	}
	if id == "" {
		_, err = tx.Exec("INSERT INTO subscription_rules(id,revision,enabled,next_sequence,next_date,definition) VALUES(?,?,?,?,?,?)", rule.ID, rule.Revision, rule.Enabled, rule.NextSequence, rule.NextDate, string(definition))
	} else {
		_, err = tx.Exec("UPDATE subscription_rules SET revision=?, enabled=?, next_date=?, definition=? WHERE id=?", rule.Revision, rule.Enabled, rule.NextDate, string(definition), rule.ID)
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM subscription_rule_tags WHERE rule_id=?", rule.ID); err != nil {
		return nil, err
	}
	for _, tag := range input.Tags {
		if _, err := tx.Exec("INSERT OR IGNORE INTO subscription_rule_tags(rule_id,tag_id) VALUES(?,?)", rule.ID, tag); err != nil {
			return nil, err
		}
	}
	if _, err := s.database(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.autoSnapshot()
	return &rule, nil
}

// SyncSubscriptions is explicitly invoked while a caller holds its own vault
// lease. It owns no background timer/key and cannot run on a locked vault.
// Each bounded pass commits one occurrence at a time in due-date order.
func (s *Service) SyncSubscriptions(ctx context.Context) (*SubscriptionSync, error) {
	return s.syncSubscriptionsAt(ctx, time.Now(), fetchSubscriptionRate)
}

func (s *Service) syncSubscriptionsAt(ctx context.Context, now time.Time, rateLookup func(context.Context, string, string) (subscriptionRate, error)) (*SubscriptionSync, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := &SubscriptionSync{Errors: []string{}}
	blocked := map[string]bool{}
	quotes := map[string]subscriptionRate{}
	for attempt := 0; attempt < subscriptionBatchSize; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rules, err := s.subscriptionsContext(ctx)
		if err != nil {
			return nil, err
		}
		var due *SubscriptionRule
		for i := range rules {
			rule := &rules[i]
			today, err := subscriptionToday(rule.Timezone, now)
			if err != nil {
				return nil, err
			}
			if rule.Enabled && !blocked[rule.ID] && rule.NextDate <= today && (due == nil || rule.NextDate < due.NextDate || (rule.NextDate == due.NextDate && rule.ID < due.ID)) {
				due = rule
			}
		}
		if due == nil {
			break
		}
		quote := subscriptionRate{Date: due.NextDate, Rate: "1"}
		if due.Currency != "JPY" {
			key := due.Currency + due.NextDate
			var cached bool
			quote, cached = quotes[key]
			if !cached {
				quote, err = rateLookup(ctx, due.Currency, due.NextDate)
			}
			if err != nil {
				blocked[due.ID] = true
				result.Errors = append(result.Errors, ErrSubscriptionRateUnavailable.Error())
				continue
			}
			date, dateErr := subscriptionDate(quote.Date)
			payment, _ := subscriptionDate(due.NextDate)
			if dateErr != nil || date.After(payment) || date.Before(payment.AddDate(0, 0, -7)) {
				return nil, ErrSubscriptionRateUnavailable
			}
			quotes[key] = quote
		}
		if _, err := subscriptionYen(due.OriginalAmount, due.Currency, quote.Rate); err != nil {
			blocked[due.ID] = true
			result.Errors = append(result.Errors, "サブスクの円換算額が範囲外です。管理画面で金額を確認してください")
			continue
		}
		created, err := s.generateSubscription(ctx, *due, quote, now)
		if errors.Is(err, ErrSubscriptionConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if created {
			result.Created++
		}
	}
	rules, err := s.subscriptionsContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		today, _ := subscriptionToday(rule.Timezone, now)
		if rule.Enabled && !blocked[rule.ID] && rule.NextDate <= today {
			result.HasMore = true
		}
	}
	return result, nil
}

func (s *Service) generateSubscription(ctx context.Context, expected SubscriptionRule, quote subscriptionRate, now time.Time) (bool, error) {
	db, err := s.database()
	if err != nil {
		return false, err
	}
	tx, err := beginLedgerWrite(ctx, db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rule, err := scanSubscription(tx.QueryRowContext(ctx, subscriptionSelect+" WHERE id=?", expected.ID))
	if err != nil {
		return false, err
	}
	today, err := subscriptionToday(rule.Timezone, now)
	if err != nil {
		return false, err
	}
	if !rule.Enabled || rule.Revision != expected.Revision || rule.NextSequence != expected.NextSequence || rule.NextDate != expected.NextDate || rule.NextDate > today {
		return false, ErrSubscriptionConflict
	}
	amount, err := subscriptionYen(rule.OriginalAmount, rule.Currency, quote.Rate)
	if err != nil {
		return false, err
	}
	rule.Tags, err = subscriptionTagsIn(ctx, tx, rule.ID)
	if err != nil {
		return false, err
	}
	request := models.TransactionRequest{Account: rule.Account, Date: rule.NextDate, Item: rule.Item, Type: "expense", Amount: amount, Memo: rule.Memo, Tags: rule.Tags}
	prepared, err := prepareTransactionInsertContext(ctx, request)
	if err != nil {
		return false, err
	}
	response, err := addPreparedTransactionIn(tx, prepared)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO subscription_occurrences(rule_id,sequence,due_date,revision,transaction_id,currency,original_amount,rate,rate_date,yen_amount) VALUES(?,?,?,?,?,?,?,?,?,?)`, rule.ID, rule.NextSequence, rule.NextDate, rule.Revision, response.ID, rule.Currency, rule.OriginalAmount, quote.Rate, quote.Date, amount); err != nil {
		return false, err
	}
	next, err := subscriptionNextDate(rule)
	enabled := true
	if err != nil {
		date, dateErr := subscriptionDate(rule.NextDate)
		if dateErr != nil || date.Year() != 9998 {
			return false, err
		}
		// The last supported payment still commits; only future generation
		// stops at the documented calendar boundary.
		next, enabled = rule.NextDate, false
	}
	if _, err := tx.ExecContext(ctx, "UPDATE subscription_rules SET next_date=?, enabled=?, next_sequence=next_sequence+1, revision=revision+1 WHERE id=?", next, enabled, rule.ID); err != nil {
		return false, err
	}
	// Check the request lease again after a potentially contended writer wait.
	if _, err := s.database(); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.autoSnapshot()
	return true, nil
}

func subscriptionProvenanceIn(q interface{ QueryRow(string, ...any) *sql.Row }, id int64) (*models.SubscriptionProvenance, error) {
	var p models.SubscriptionProvenance
	var confirmed bool
	var amount int64
	err := q.QueryRow("SELECT rule_id,confirmed,currency,original_amount,rate,rate_date,yen_amount FROM subscription_occurrences WHERE transaction_id=?", id).Scan(&p.RuleID, &confirmed, &p.Currency, &p.OriginalAmount, &p.Rate, &p.RateDate, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Unconfirmed, p.YenAmountExact = !confirmed, strconv.FormatInt(amount, 10)
	return &p, nil
}

func attachSubscriptionProvenance(db *sql.DB, transactions []models.TransactionResponse, where string, args []any) error {
	if len(transactions) == 0 {
		return nil
	}
	// #nosec G202 -- where contains only fixed clauses assembled in getTransactions; every user value remains a bound placeholder.
	rows, err := db.Query(`SELECT o.transaction_id,o.rule_id,o.confirmed,o.currency,o.original_amount,o.rate,o.rate_date,o.yen_amount FROM subscription_occurrences o JOIN (SELECT id FROM transactions`+where+`) ft ON ft.id=o.transaction_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := make(map[int64]*models.SubscriptionProvenance)
	for rows.Next() {
		var id, amount int64
		var confirmed bool
		p := &models.SubscriptionProvenance{}
		if err := rows.Scan(&id, &p.RuleID, &confirmed, &p.Currency, &p.OriginalAmount, &p.Rate, &p.RateDate, &amount); err != nil {
			return err
		}
		p.Unconfirmed, p.YenAmountExact = !confirmed, strconv.FormatInt(amount, 10)
		byID[id] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range transactions {
		transactions[i].Subscription = byID[transactions[i].ID]
	}
	return nil
}

type SubscriptionHistory struct {
	Transactions []models.TransactionResponse `json:"transactions"`
	NextOffset   int                          `json:"next_offset"`
}

func (s *Service) SubscriptionTransactions(id, account, search, start, end string, offset int) (*SubscriptionHistory, error) {
	if !ValidSaveRequestID(id) || offset < 0 || offset > 1000000 || len(search) > 1000 {
		return nil, subscriptionInputError("検索条件が無効です")
	}
	if start != "" {
		if _, err := subscriptionDate(start); err != nil {
			return nil, err
		}
	}
	if end != "" {
		if _, err := subscriptionDate(end); err != nil {
			return nil, err
		}
		date, _ := subscriptionDate(end)
		end = date.AddDate(0, 0, 1).Format(time.DateOnly)
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	var exists int
	if err := db.QueryRow("SELECT 1 FROM subscription_rules WHERE id=?", id).Scan(&exists); err != nil {
		return nil, err
	}
	transactions, err := s.getTransactions(account, search, id, start, end, offset, 101)
	if err != nil {
		return nil, err
	}
	page := &SubscriptionHistory{Transactions: transactions, NextOffset: -1}
	if len(transactions) > 100 {
		page.Transactions = transactions[:100]
		page.NextOffset = offset + 100
	}
	return page, nil
}

type SubscriptionCalendarEntry struct {
	Date           string                      `json:"date"`
	RuleID         string                      `json:"rule_id"`
	Item           string                      `json:"item"`
	Account        string                      `json:"account"`
	Currency       string                      `json:"currency"`
	OriginalAmount string                      `json:"original_amount"`
	Transaction    *models.TransactionResponse `json:"transaction,omitempty"`
}

// Pure read: browsing even a far-future month never invokes generation or FX.
func (s *Service) SubscriptionCalendar(month string) ([]SubscriptionCalendarEntry, error) {
	date, err := subscriptionDate(month + "-01")
	if err != nil {
		return nil, err
	}
	end := date.AddDate(0, 1, 0).Format(time.DateOnly)
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT o.rule_id,o.due_date,o.currency,o.original_amount,t.id,t.account,t.date,t.item,t.type,COALESCE(a.amount,t.amount),t.balance,t.memo,o.confirmed,o.rate,o.rate_date,o.yen_amount FROM subscription_occurrences o LEFT JOIN transactions t ON t.id=o.transaction_id LEFT JOIN transaction_archive_amounts a ON a.transaction_id=t.id WHERE o.due_date>=? AND o.due_date<? ORDER BY o.due_date,o.rule_id LIMIT 2049`, month+"-01", end)
	if err != nil {
		return nil, err
	}
	entries := []SubscriptionCalendarEntry{}
	consumed := map[string]bool{}
	count := 0
	for rows.Next() {
		count++
		if count > 2048 {
			rows.Close()
			return nil, subscriptionInputError("この月のサブスク件数が上限を超えています")
		}
		entry := SubscriptionCalendarEntry{}
		var id, amount, balance sql.NullInt64
		var account, item, dateStr, txType, memo sql.NullString
		var confirmed bool
		var rate, rateDate string
		var originalYen int64
		if err := rows.Scan(&entry.RuleID, &entry.Date, &entry.Currency, &entry.OriginalAmount, &id, &account, &dateStr, &item, &txType, &amount, &balance, &memo, &confirmed, &rate, &rateDate, &originalYen); err != nil {
			rows.Close()
			return nil, err
		}
		consumed[entry.RuleID+entry.Date] = true
		if !id.Valid {
			continue
		}
		stored := models.Transaction{ID: id.Int64, Account: account.String, Date: parseDate(dateStr.String), Item: item.String, Type: txType.String, Amount: amount.Int64, Balance: balance.Int64, Memo: memo.String}
		transaction := stored.ToResponse()
		transaction.Subscription = &models.SubscriptionProvenance{RuleID: entry.RuleID, Unconfirmed: !confirmed, Currency: entry.Currency, OriginalAmount: entry.OriginalAmount, Rate: rate, RateDate: rateDate, YenAmountExact: strconv.FormatInt(originalYen, 10)}
		entry.Transaction, entry.Item, entry.Account = &transaction, item.String, account.String
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Transaction.Tags, err = transactionSaveTagsIn(tx, entries[i].Transaction.ID)
		if err != nil {
			return nil, err
		}
	}
	ruleRows, err := tx.Query(subscriptionSelect + " WHERE enabled=1 ORDER BY id LIMIT 257")
	if err != nil {
		return nil, err
	}
	defer ruleRows.Close()
	ruleCount := 0
	for ruleRows.Next() {
		ruleCount++
		if ruleCount > maxSubscriptionRules {
			return nil, subscriptionInputError("サブスク数が上限を超えています")
		}
		rule, err := scanSubscription(ruleRows)
		if err != nil {
			return nil, err
		}
		if rule.Interval == "yearly" && int(date.Month()) != rule.AnchorMonth {
			continue
		}
		payment := subscriptionScheduledDate(date.Year(), date.Month(), rule.AnchorDay)
		if payment < rule.NextDate || consumed[rule.ID+payment] {
			continue
		}
		entries = append(entries, SubscriptionCalendarEntry{Date: payment, RuleID: rule.ID, Item: rule.Item, Account: rule.Account, Currency: rule.Currency, OriginalAmount: rule.OriginalAmount})
	}
	return entries, ruleRows.Err()
}
