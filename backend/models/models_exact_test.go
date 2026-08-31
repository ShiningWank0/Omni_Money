package models

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestTransactionResponseIncludesLosslessInt64Companions(t *testing.T) {
	tx := Transaction{ID: 1, Account: "cash", Date: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Item: "legacy", Type: "income", Amount: math.MaxInt64, Balance: math.MinInt64}
	encoded, err := json.Marshal(tx.ToResponse())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{`"amount":9223372036854775807`, `"amount_exact":"9223372036854775807"`, `"balance":-9223372036854775808`, `"balance_exact":"-9223372036854775808"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response %s missing %s", text, want)
		}
	}
}
