package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"omni_money/backend/validation"
)

var ErrSubscriptionRateUnavailable = errors.New("為替レートを取得できませんでした。外貨の自動登録は次回利用時に再試行します")

var subscriptionCurrencies = map[string]bool{"JPY": true, "USD": true, "EUR": true, "GBP": true, "AUD": true, "CAD": true, "CHF": true, "CNY": true, "HKD": true, "SGD": true}
var subscriptionMoneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,2})?$`)
var subscriptionRatePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,16})?$`)

type subscriptionRate struct {
	Date string
	Rate string
}

// Fixed HTTPS origin, no proxies, redirects, cookies, credentials, caller URLs,
// account names, item names, or payment amounts. Only currency/date leave the
// process. Network I/O always happens before reserving SQLite's writer.
var subscriptionRateClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    3 * time.Second,
		ResponseHeaderTimeout:  3 * time.Second,
		MaxResponseHeaderBytes: 8192,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        4,
		IdleConnTimeout:        30 * time.Second,
	},
}

func fetchSubscriptionRate(ctx context.Context, currency, due string) (subscriptionRate, error) {
	if !subscriptionCurrencies[currency] || currency == "JPY" {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	date, err := subscriptionDate(due)
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	query := url.Values{"base": {currency}, "quotes": {"JPY"}, "from": {date.AddDate(0, 0, -7).Format(time.DateOnly)}, "to": {due}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.frankfurter.dev/v2/providers/ecb/rates?"+query.Encode(), nil)
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	req.Header.Set("Accept", "application/json")
	response, err := subscriptionRateClient.Do(req)
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	return decodeSubscriptionRate(data, currency, due)
}

func decodeSubscriptionRate(data []byte, currency, due string) (subscriptionRate, error) {
	var rows []struct {
		Date  string      `json:"date"`
		Base  string      `json:"base"`
		Quote string      `json:"quote"`
		Rate  json.Number `json:"rate"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&rows); err != nil || len(rows) == 0 || len(rows) > 8 {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	date, err := subscriptionDate(due)
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	minimum := date.AddDate(0, 0, -7).Format(time.DateOnly)
	chosen := subscriptionRate{}
	seen := make(map[string]bool)
	for _, row := range rows {
		if _, err := subscriptionDate(row.Date); err != nil || row.Date < minimum || row.Date > due || !strings.EqualFold(row.Base, currency) || !strings.EqualFold(row.Quote, "JPY") || seen[row.Date] || !validSubscriptionRate(row.Rate.String()) {
			return subscriptionRate{}, ErrSubscriptionRateUnavailable
		}
		seen[row.Date] = true
		if row.Date > chosen.Date {
			chosen = subscriptionRate{row.Date, row.Rate.String()}
		}
	}
	return chosen, nil
}

func validSubscriptionRate(rate string) bool {
	if !subscriptionRatePattern.MatchString(rate) {
		return false
	}
	r, ok := new(big.Rat).SetString(rate)
	return ok && r.Sign() > 0
}

// Decimal strings and rational arithmetic avoid binary floating point and
// JavaScript's safe-integer boundary. Round positive yen half up exactly once.
func subscriptionYen(amount, currency, rate string) (int64, error) {
	if !subscriptionCurrencies[currency] || !subscriptionMoneyPattern.MatchString(amount) || !validSubscriptionRate(rate) || (currency == "JPY" && (strings.Contains(amount, ".") || rate != "1")) {
		return 0, errors.New("サブスクの通貨・金額が無効です")
	}
	a, ok := new(big.Rat).SetString(amount)
	if !ok || a.Sign() <= 0 {
		return 0, errors.New("サブスクの金額が無効です")
	}
	r, _ := new(big.Rat).SetString(rate)
	a.Mul(a, r)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(a.Num(), a.Denom(), remainder)
	if remainder.Lsh(remainder, 1).Cmp(a.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 || quotient.Cmp(big.NewInt(validation.MaxTransactionAmount)) > 0 {
		return 0, errors.New("円換算したサブスク金額が取引の範囲外です")
	}
	return quotient.Int64(), nil
}
