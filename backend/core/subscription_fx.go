package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"omni_money/backend/validation"
)

var ErrSubscriptionRateUnavailable = errors.New("為替レートを取得できませんでした。外貨の自動登録は次回利用時に再試行します")

var subscriptionCurrencies = map[string]bool{"JPY": true, "USD": true, "EUR": true, "GBP": true, "AUD": true, "CAD": true, "CHF": true, "CNY": true, "HKD": true, "SGD": true}
var subscriptionMoneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,2})?$`)
var subscriptionRatePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,16})?$`)

const subscriptionRateBodyLimit = 16 * 1024

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
	// The documented JSON representation is the only format we request. Do not
	// sniff HTML/error pages or allow ambiguous Content-Type headers/encodings.
	contentTypes := response.Header.Values("Content-Type")
	if response.StatusCode != http.StatusOK || len(contentTypes) != 1 {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	mediaType, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, subscriptionRateBodyLimit+1))
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	return decodeSubscriptionRate(data, currency, due)
}

// This intentionally accepts less than the general Frankfurter Rate schema:
// the fixed ECB query does not request expand=providers, other pairs, or formats.
// Validate tokens before creating domain values: struct decoding silently folds
// key case, overwrites duplicate keys and accepts quoted json.Number values.
func decodeSubscriptionRate(data []byte, currency, due string) (subscriptionRate, error) {
	if len(data) > subscriptionRateBodyLimit || !utf8.Valid(data) || !subscriptionCurrencies[currency] || currency == "JPY" {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	date, err := subscriptionDate(due)
	if err != nil {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	minimum := date.AddDate(0, 0, -7).Format(time.DateOnly)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	chosen := subscriptionRate{}
	seen := make(map[string]bool)
	for decoder.More() {
		// The inclusive seven-day lookback has at most eight distinct dates.
		if len(seen) == 8 {
			return subscriptionRate{}, ErrSubscriptionRateUnavailable
		}
		row, err := decodeSubscriptionRateRow(decoder)
		if err != nil {
			return subscriptionRate{}, ErrSubscriptionRateUnavailable
		}
		if _, err := subscriptionDate(row["date"]); err != nil || row["date"] < minimum || row["date"] > due || row["base"] != currency || row["quote"] != "JPY" || seen[row["date"]] || !validSubscriptionRate(row["rate"]) {
			return subscriptionRate{}, ErrSubscriptionRateUnavailable
		}
		seen[row["date"]] = true
		if row["date"] > chosen.Date {
			chosen = subscriptionRate{row["date"], row["rate"]}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') || chosen.Date == "" {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	if _, err := decoder.Token(); err != io.EOF {
		return subscriptionRate{}, ErrSubscriptionRateUnavailable
	}
	return chosen, nil
}

func decodeSubscriptionRateRow(decoder *json.Decoder) (map[string]string, error) {
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrSubscriptionRateUnavailable
	}
	row := make(map[string]string, 4)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || (key != "date" && key != "base" && key != "quote" && key != "rate") {
			return nil, ErrSubscriptionRateUnavailable
		}
		// Token unescapes keys first, so ra\u0074e cannot hide a duplicate rate.
		if _, duplicate := row[key]; duplicate {
			return nil, ErrSubscriptionRateUnavailable
		}
		value, err := decoder.Token()
		if err != nil {
			return nil, ErrSubscriptionRateUnavailable
		}
		if key == "rate" {
			number, ok := value.(json.Number)
			if !ok {
				return nil, ErrSubscriptionRateUnavailable
			}
			row[key] = number.String()
		} else {
			text, ok := value.(string)
			if !ok {
				return nil, ErrSubscriptionRateUnavailable
			}
			row[key] = text
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(row) != 4 {
		return nil, ErrSubscriptionRateUnavailable
	}
	return row, nil
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
