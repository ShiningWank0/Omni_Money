package validation

import (
	"math"
	"math/big"
	"testing"
)

func TestValidateTransactionAmount(t *testing.T) {
	for _, tt := range []struct {
		amount  int64
		wantErr bool
	}{
		{amount: -1, wantErr: true},
		{amount: 0, wantErr: true},
		{amount: 1},
		{amount: MaxTransactionAmount},
		{amount: MaxTransactionAmount + 1, wantErr: true},
	} {
		if err := ValidateTransactionAmount(tt.amount); (err != nil) != tt.wantErr {
			t.Errorf("ValidateTransactionAmount(%d) error=%v, wantErr=%v", tt.amount, err, tt.wantErr)
		}
	}
}

func FuzzCheckedArithmetic(f *testing.F) {
	for _, seed := range [][2]int64{{0, 0}, {math.MaxInt64, 1}, {math.MinInt64, -1}, {math.MaxInt64, -1}, {math.MinInt64, 1}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, a, b int64) {
		min := big.NewInt(math.MinInt64)
		max := big.NewInt(math.MaxInt64)

		wantAdd := new(big.Int).Add(big.NewInt(a), big.NewInt(b))
		addInRange := wantAdd.Cmp(min) >= 0 && wantAdd.Cmp(max) <= 0
		gotAdd, addErr := CheckedAddInt64(a, b)
		if addInRange != (addErr == nil) || (addInRange && gotAdd != wantAdd.Int64()) {
			t.Fatalf("add(%d,%d) = %d,%v; oracle=%s", a, b, gotAdd, addErr, wantAdd)
		}

		wantSub := new(big.Int).Sub(big.NewInt(a), big.NewInt(b))
		subInRange := wantSub.Cmp(min) >= 0 && wantSub.Cmp(max) <= 0
		gotSub, subErr := CheckedSubInt64(a, b)
		if subInRange != (subErr == nil) || (subInRange && gotSub != wantSub.Int64()) {
			t.Fatalf("sub(%d,%d) = %d,%v; oracle=%s", a, b, gotSub, subErr, wantSub)
		}
	})
}

func TestCheckedArithmeticBoundaries(t *testing.T) {
	if _, err := CheckedAddInt64(math.MaxInt64, 1); err == nil {
		t.Fatal("MaxInt64 + 1 succeeded")
	}
	if _, err := CheckedAddInt64(math.MinInt64, -1); err == nil {
		t.Fatal("MinInt64 + (-1) succeeded")
	}
	if _, err := CheckedSubInt64(math.MinInt64, 1); err == nil {
		t.Fatal("MinInt64 - 1 succeeded")
	}
	if _, err := CheckedSubInt64(math.MaxInt64, -1); err == nil {
		t.Fatal("MaxInt64 - (-1) succeeded")
	}
	if got, err := CheckedAddInt64(math.MaxInt64-1, 1); err != nil || got != math.MaxInt64 {
		t.Fatalf("checked add boundary = %d, %v", got, err)
	}
	if got, err := CheckedSubInt64(math.MinInt64+1, 1); err != nil || got != math.MinInt64 {
		t.Fatalf("checked sub boundary = %d, %v", got, err)
	}
}
