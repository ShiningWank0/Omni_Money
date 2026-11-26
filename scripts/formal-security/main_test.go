package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func changedSource(t *testing.T, old, change string) *source {
	t.Helper()
	data, err := os.ReadFile("../../backend/validation/amount.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), old) != 1 {
		t.Fatalf("expected exactly one fixture location for %q", old)
	}
	path := filepath.Join(t.TempDir(), "amount.go")
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, change, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSource(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUnsupportedStatementStopsProof(t *testing.T) {
	s := changedSource(t, "return a + b, nil", "a++\nreturn a + b, nil")
	if _, err := s.query(contracts[1]); err == nil || !strings.Contains(err.Error(), "unsupported proof input") {
		t.Fatalf("unsupported body: %v", err)
	}
}

func TestProofFindsContractChanges(t *testing.T) {
	bin := os.Getenv("OMNI_PROOF_Z3")
	if bin == "" {
		t.Skip("the formal-security CI step supplies the verified solver")
	}
	for _, tt := range []struct {
		contract      contract
		before, after string
	}{
		{contracts[0], "amount <= 0", "amount < 0"},
		{contracts[1], "a > math.MaxInt64-b", "a >= math.MaxInt64-b"},
		{contracts[2], "return a - b, nil", "return a + b, nil"},
	} {
		t.Run(tt.contract.name, func(t *testing.T) {
			s := changedSource(t, tt.before, tt.after)
			result, err := verify(s, bin)
			if !errors.Is(err, errViolated) || result.Status != "findings" || len(result.Findings) != 1 {
				t.Fatalf("changed implementation: report=%+v err=%v", result, err)
			}
			if result.Findings[0]["id"] != tt.contract.name {
				t.Fatalf("wrong failing contract: %+v", result.Findings)
			}
		})
	}
}
