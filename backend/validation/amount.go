package validation

import (
	"fmt"
	"math"
)

// MaxTransactionAmount は全入力経路で許可する1取引あたりの上限金額。
const MaxTransactionAmount int64 = 1_000_000_000

func ValidateTransactionAmount(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("金額は正の数値である必要があります")
	}
	if amount > MaxTransactionAmount {
		return fmt.Errorf("金額は%d円以下で指定してください", MaxTransactionAmount)
	}
	return nil
}

func CheckedAddInt64(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("金額集計がint64の範囲を超えます")
	}
	return a + b, nil
}

func CheckedSubInt64(a, b int64) (int64, error) {
	if (b > 0 && a < math.MinInt64+b) || (b < 0 && a > math.MaxInt64+b) {
		return 0, fmt.Errorf("金額集計がint64の範囲を超えます")
	}
	return a - b, nil
}
