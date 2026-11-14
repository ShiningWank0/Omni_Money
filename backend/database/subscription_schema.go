package database

import (
	"context"
	"fmt"
	"strings"
)

// Subscription state lives only in the encrypted ledger. Occurrence rows
// survive transaction deletion: their sequence is never reused.
var subscriptionSchema = []struct{ kind, name, ddl string }{
	{"table", "subscription_rules", `CREATE TABLE IF NOT EXISTS subscription_rules (
		id TEXT PRIMARY KEY NOT NULL CHECK(length(id) = 36),
		revision INTEGER NOT NULL CHECK(revision > 0),
		enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
		next_sequence INTEGER NOT NULL CHECK(next_sequence > 0),
		next_date TEXT NOT NULL CHECK(length(next_date) = 10),
		definition TEXT NOT NULL CHECK(length(definition) BETWEEN 2 AND 32768)
	)`},
	{"table", "subscription_rule_tags", `CREATE TABLE IF NOT EXISTS subscription_rule_tags (
		rule_id TEXT NOT NULL REFERENCES subscription_rules(id) ON DELETE CASCADE,
		tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
		PRIMARY KEY(rule_id, tag_id)
	)`},
	{"table", "subscription_occurrences", `CREATE TABLE IF NOT EXISTS subscription_occurrences (
		rule_id TEXT NOT NULL REFERENCES subscription_rules(id),
		sequence INTEGER NOT NULL CHECK(sequence > 0),
		due_date TEXT NOT NULL CHECK(length(due_date) = 10),
		revision INTEGER NOT NULL CHECK(revision > 0),
		transaction_id INTEGER UNIQUE REFERENCES transactions(id) ON DELETE SET NULL,
		confirmed INTEGER NOT NULL DEFAULT 0 CHECK(confirmed IN (0, 1)),
		currency TEXT NOT NULL CHECK(length(currency) = 3),
		original_amount TEXT NOT NULL CHECK(length(original_amount) BETWEEN 1 AND 20),
		rate TEXT NOT NULL CHECK(length(rate) BETWEEN 1 AND 32),
		rate_date TEXT NOT NULL CHECK(length(rate_date) = 10),
		yen_amount INTEGER NOT NULL CHECK(yen_amount > 0),
		PRIMARY KEY(rule_id, sequence),
		UNIQUE(rule_id, due_date)
	)`},
	{"table", "subscription_dismissals", `CREATE TABLE IF NOT EXISTS subscription_dismissals (
		fingerprint TEXT PRIMARY KEY NOT NULL CHECK(length(fingerprint) = 64)
	)`},
	{"index", "idx_subscription_due", `CREATE INDEX IF NOT EXISTS idx_subscription_due ON subscription_rules(enabled, next_date, id)`},
	{"index", "idx_subscription_occurrence_date", `CREATE INDEX IF NOT EXISTS idx_subscription_occurrence_date ON subscription_occurrences(due_date, rule_id)`},
}

func validateSubscriptionSchema(ctx context.Context, target schemaQueryer) error {
	for _, object := range subscriptionSchema {
		var actual string
		if err := target.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type=? AND name=?", object.kind, object.name).Scan(&actual); err != nil {
			return err
		}
		if canonicalDDL(actual) != canonicalDDL(strings.Replace(object.ddl, " IF NOT EXISTS", "", 1)) {
			return fmt.Errorf("subscription schema mismatch: %s", object.name)
		}
	}
	return nil
}
