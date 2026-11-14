package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func TestSubscriptionFXDecimalRoundingAndRange(t *testing.T) {
	for _, test := range []struct {
		amount, currency, rate string
		want                   int64
		invalid                bool
	}{
		{"9.99", "USD", "150.5", 1503, false}, {"0.01", "USD", "150", 2, false}, {"1.00", "EUR", "155.49", 155, false},
		{"1000000000", "JPY", "1", 1000000000, false}, {"1000000001", "JPY", "1", 0, true},
		{"1.1", "JPY", "1", 0, true}, {"1e2", "USD", "150", 0, true}, {"NaN", "USD", "1", 0, true},
		{"0", "USD", "150", 0, true}, {"-1", "USD", "150", 0, true}, {"1", "USD", "-1", 0, true},
		{"1", "USD", "NaN", 0, true}, {"1", "BTC", "150", 0, true}, {"0.001", "USD", "150", 0, true},
		{"999999999999.99", "USD", "9999999999.9999999999999999", 0, true},
	} {
		t.Run(test.amount+test.currency+test.rate, func(t *testing.T) {
			got, err := subscriptionYen(test.amount, test.currency, test.rate)
			if (err != nil) != test.invalid || (!test.invalid && got != test.want) {
				t.Fatalf("yen=%d error=%v", got, err)
			}
		})
	}
}

func TestSubscriptionFXRejectsInvalidDatesPairsAndNumbers(t *testing.T) {
	valid := `[{"date":"2024-05-10","base":"USD","quote":"JPY","rate":155.74},{"date":"2024-05-13","base":"USD","quote":"JPY","rate":155.86}]`
	quote, err := decodeSubscriptionRate([]byte(valid), "USD", "2024-05-14")
	if err != nil || quote.Date != "2024-05-13" || quote.Rate != "155.86" {
		t.Fatalf("quote=%+v error=%v", quote, err)
	}
	for _, payload := range []string{
		`[]`, `null`, `{}`, valid + `{}`,
		`[{"date":"2024-05-15","base":"USD","quote":"JPY","rate":155.86}]`,
		`[{"date":"2024-05-01","base":"USD","quote":"JPY","rate":155.86}]`,
		`[{"date":"2024-02-30","base":"USD","quote":"JPY","rate":155.86}]`,
		`[{"date":"2024-05-13","base":"EUR","quote":"JPY","rate":155.86}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"EUR","rate":155.86}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":0}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":-1}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":1e30}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":"https://internal/"}]`,
		`[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":1},{"date":"2024-05-13","base":"USD","quote":"JPY","rate":2}]`,
	} {
		t.Run(payload, func(t *testing.T) {
			if _, err := decodeSubscriptionRate([]byte(payload), "USD", "2024-05-14"); err == nil {
				t.Fatal("invalid provider payload accepted")
			}
		})
	}
}

// Captured from the fixed public ECB endpoint on 2026-11-13. Its OpenAPI 2.1.1
// Rate schema requires date/base/quote strings and a positive JSON number rate.
// https://api.frankfurter.dev/v2/providers/ecb/rates?base=USD&quotes=JPY&from=2024-05-08&to=2024-05-15
const subscriptionFXProviderSample = `[{"date":"2024-05-08","base":"USD","quote":"JPY","rate":155.59},{"date":"2024-05-09","base":"USD","quote":"JPY","rate":155.91},{"date":"2024-05-10","base":"USD","quote":"JPY","rate":155.74},{"date":"2024-05-13","base":"USD","quote":"JPY","rate":155.86},{"date":"2024-05-14","base":"USD","quote":"JPY","rate":156.44},{"date":"2024-05-15","base":"USD","quote":"JPY","rate":155.49}]`

func TestSubscriptionFXVerifiedProviderContract(t *testing.T) {
	for _, test := range []struct {
		name, payload, due, wantDate, wantRate string
	}{
		{"observed response", subscriptionFXProviderSample, "2024-05-15", "2024-05-15", "155.49"},
		{"weekend", `[{"date":"2024-05-09","base":"USD","quote":"JPY","rate":155.91},{"date":"2024-05-10","base":"USD","quote":"JPY","rate":155.74}]`, "2024-05-12", "2024-05-10", "155.74"},
		{"order and whitespace", " [ {\"rate\":155.8600,\"quote\":\"JPY\",\"base\":\"USD\",\"date\":\"2024-05-13\"}, {\"date\":\"2024-05-10\",\"base\":\"USD\",\"quote\":\"JPY\",\"rate\":155.74} ] \r\n", "2024-05-14", "2024-05-13", "155.8600"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeSubscriptionRate([]byte(test.payload), "USD", test.due)
			if err != nil || got.Date != test.wantDate || got.Rate != test.wantRate {
				t.Fatalf("quote=%+v error=%v", got, err)
			}
		})
	}
}

func TestSubscriptionFXRejectsUntrustedJSONShape(t *testing.T) {
	valid := `[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":155.86}]`
	for name, payload := range map[string]string{
		"quoted numeric rate": strings.ReplaceAll(valid, `155.86`, `"155.86"`),
		"code as rate":        strings.ReplaceAll(valid, `155.86`, `"<script>alert(1)</script>"`),
		"SQL as rate":         strings.ReplaceAll(valid, `155.86`, `"1; DROP TABLE transactions"`),
		"NaN":                 strings.ReplaceAll(valid, `155.86`, `NaN`),
		"infinity":            strings.ReplaceAll(valid, `155.86`, `Infinity`),
		"null rate":           strings.ReplaceAll(valid, `155.86`, `null`),
		"boolean rate":        strings.ReplaceAll(valid, `155.86`, `true`),
		"object rate":         strings.ReplaceAll(valid, `155.86`, `{"value":155.86}`),
		"array rate":          strings.ReplaceAll(valid, `155.86`, `[155.86]`),
		"oversized integer":   strings.ReplaceAll(valid, `155.86`, `10000000000`),
		"excess precision":    strings.ReplaceAll(valid, `155.86`, `155.12345678901234567`),
		"deeply nested rate":  strings.ReplaceAll(valid, `155.86`, strings.Repeat("[", 1000)+"1"+strings.Repeat("]", 1000)),
		"unexpected field":    strings.ReplaceAll(valid, `155.86}`, `155.86,"url":"https://internal/"}`),
		"prototype field":     strings.ReplaceAll(valid, `155.86}`, `155.86,"__proto__":{"rate":1}}`),
		"unrequested expand":  strings.ReplaceAll(valid, `155.86}`, `155.86,"providers":[]}`),
		"case variant key":    strings.ReplaceAll(valid, `"rate":`, `"Rate":`),
		"case variant base":   strings.ReplaceAll(valid, `"USD"`, `"usd"`),
		"case variant quote":  strings.ReplaceAll(valid, `"JPY"`, `"jpy"`),
		"missing date":        strings.ReplaceAll(valid, `"date":"2024-05-13",`, ``),
		"missing base":        strings.ReplaceAll(valid, `"base":"USD",`, ``),
		"missing quote":       strings.ReplaceAll(valid, `"quote":"JPY",`, ``),
		"missing rate":        strings.ReplaceAll(valid, `,"rate":155.86`, ``),
		"null date":           strings.ReplaceAll(valid, `"2024-05-13"`, `null`),
		"object date":         strings.ReplaceAll(valid, `"2024-05-13"`, `{}`),
		"numeric base":        strings.ReplaceAll(valid, `"USD"`, `123`),
		"object base":         strings.ReplaceAll(valid, `"USD"`, `{}`),
		"boolean quote":       strings.ReplaceAll(valid, `"JPY"`, `false`),
		"object quote":        strings.ReplaceAll(valid, `"JPY"`, `{}`),
		"duplicate date key":  strings.ReplaceAll(valid, `"date":`, `"date":"2024-05-12","date":`),
		"duplicate base key":  strings.ReplaceAll(valid, `"base":`, `"base":"EUR","base":`),
		"duplicate quote key": strings.ReplaceAll(valid, `"quote":`, `"quote":"EUR","quote":`),
		"duplicate rate key":  strings.ReplaceAll(valid, `"rate":`, `"rate":1,"rate":`),
		"identical duplicate": strings.ReplaceAll(valid, `"rate":`, `"rate":155.86,"rate":`),
		"escaped duplicate":   strings.ReplaceAll(valid, `"rate":`, `"ra\u0074e":1,"rate":`),
		"invalid UTF-8":       strings.ReplaceAll(valid, `"USD"`, "\"US\xff\""),
		"unpaired surrogate":  strings.ReplaceAll(valid, `"USD"`, `"US\ud800"`),
		"null row":            strings.ReplaceAll(valid, `[{`, `[null,{`),
		"primitive row":       strings.ReplaceAll(valid, `[{`, `[1,{`),
		"object wrapper":      `{"rates":` + valid + `}`,
		"trailing primitive":  valid + `true`,
		"trailing comment":    valid + `/* hidden */`,
		"trailing comma":      strings.ReplaceAll(valid, `}]`, `},]`),
		"truncated":           valid[:len(valid)-1],
		"oversized body":      valid + strings.Repeat(" ", subscriptionRateBodyLimit),
	} {
		t.Run(name, func(t *testing.T) {
			quote, err := decodeSubscriptionRate([]byte(payload), "USD", "2024-05-14")
			if !errors.Is(err, ErrSubscriptionRateUnavailable) || quote != (subscriptionRate{}) {
				t.Fatalf("untrusted payload yielded quote=%+v error=%v", quote, err)
			}
		})
	}
	// Both the HTTP reader and the parser must enforce the same byte boundary.
	bounded := valid + strings.Repeat(" ", subscriptionRateBodyLimit-len(valid))
	if _, err := decodeSubscriptionRate([]byte(bounded), "USD", "2024-05-14"); err != nil {
		t.Fatalf("valid boundary rejected: %v", err)
	}
	if _, err := decodeSubscriptionRate([]byte(bounded+" "), "USD", "2024-05-14"); err == nil {
		t.Fatal("body byte limit exceeded")
	}
	var rows []string
	for day := 7; day <= 14; day++ {
		rows = append(rows, fmt.Sprintf(`{"date":"2024-05-%02d","base":"USD","quote":"JPY","rate":155.86}`, day))
	}
	if _, err := decodeSubscriptionRate([]byte("["+strings.Join(rows, ",")+"]"), "USD", "2024-05-14"); err != nil {
		t.Fatalf("eight distinct days rejected: %v", err)
	}
	rows = append(rows, `{"date":"2024-05-06","base":"USD","quote":"JPY","rate":155.86}`)
	if _, err := decodeSubscriptionRate([]byte("["+strings.Join(rows, ",")+"]"), "USD", "2024-05-14"); err == nil {
		t.Fatal("too many records accepted")
	}
}

type subscriptionRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip subscriptionRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestSubscriptionFXRequestPrivacyOriginAndBoundedResponse(t *testing.T) {
	original := subscriptionRateClient
	t.Cleanup(func() { subscriptionRateClient = original })
	if original.Timeout <= 0 || original.CheckRedirect == nil {
		t.Fatal("network deadlines or redirect policy missing")
	}
	transport := original.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.MaxConnsPerHost == 0 {
		t.Fatal("unsafe FX transport")
	}
	if err := original.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatal("redirects allowed")
	}
	valid := `[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":155.86}]`
	for _, test := range []struct {
		name         string
		status       int
		contentTypes []string
		body         string
		readError    bool
		invalid      bool
	}{
		{"JSON", 200, []string{"application/json"}, valid, false, false},
		{"observed charset", 200, []string{"application/json; charset=utf-8"}, valid, false, false},
		{"redirect", 302, []string{"application/json"}, "https://localhost/private", false, true},
		{"server error", 500, []string{"application/json"}, "private error", false, true},
		{"too large", 200, []string{"application/json"}, valid + strings.Repeat(" ", subscriptionRateBodyLimit), false, true},
		{"missing content type", 200, nil, valid, false, true},
		{"wrong content type", 200, []string{"text/html"}, valid, false, true},
		{"unrequested NDJSON", 200, []string{"application/x-ndjson"}, valid, false, true},
		{"wrong charset", 200, []string{"application/json; charset=utf-16"}, valid, false, true},
		{"malformed content type", 200, []string{"application/json; charset"}, valid, false, true},
		{"duplicate content type", 200, []string{"application/json", "text/html"}, valid, false, true},
		{"read failure after valid JSON", 200, []string{"application/json"}, valid, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			subscriptionRateClient = &http.Client{Transport: subscriptionRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme != "https" || request.URL.Host != "api.frankfurter.dev" || request.URL.Path != "/v2/providers/ecb/rates" || request.Method != "GET" {
					t.Fatalf("unexpected destination=%s", request.URL)
				}
				query := request.URL.Query()
				if len(query) != 4 || query.Get("base") != "USD" || query.Get("quotes") != "JPY" || query.Get("from") != "2024-05-07" || query.Get("to") != "2024-05-14" || request.Body != nil || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
					t.Fatalf("private input in request: %v", request)
				}
				if request.Header.Get("Accept") != "application/json" {
					t.Fatal("JSON representation not explicitly requested")
				}
				var body io.Reader = strings.NewReader(test.body)
				if test.readError {
					body = io.MultiReader(body, iotest.ErrReader(errors.New("untrusted read error")))
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(body), Header: http.Header{"Content-Type": test.contentTypes}}, nil
			})}
			_, err := fetchSubscriptionRate(context.Background(), "USD", "2024-05-14")
			if (err != nil) != test.invalid {
				t.Fatalf("status=%d invalid=%v error=%v", test.status, test.invalid, err)
			}
		})
	}
}

func TestSubscriptionFXUntrustedResponseDoesNotChangeLedgerOrSchedule(t *testing.T) {
	instance, service := openCoreTestService(t, "untrusted-fx")
	rule := subscriptionTestRule(t, service, "USD")
	before, err := service.GetTransactions("", "")
	if err != nil {
		t.Fatal(err)
	}
	original := subscriptionRateClient
	t.Cleanup(func() { subscriptionRateClient = original })
	valid := `[{"date":"2026-02-27","base":"USD","quote":"JPY","rate":150.5}]`
	responseBody := ""
	requests := 0
	subscriptionRateClient = &http.Client{Transport: subscriptionRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Query().Get("to") != "2026-02-28" {
			t.Fatal("failed validation advanced payment date")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
	})}
	for name, payload := range map[string]string{
		"quoted number":      strings.ReplaceAll(valid, `150.5`, `"150.5"`),
		"SQL injection":      strings.ReplaceAll(valid, `150.5`, `"1; DROP TABLE transactions"`),
		"duplicate rate":     strings.ReplaceAll(valid, `"rate":`, `"ra\u0074e":1,"rate":`),
		"unknown field":      strings.ReplaceAll(valid, `150.5}`, `150.5,"url":"https://internal/"}`),
		"wrong pair":         strings.ReplaceAll(valid, `"JPY"`, `"EUR"`),
		"future quote":       strings.ReplaceAll(valid, `2026-02-27`, `2026-03-01`),
		"valid then invalid": strings.ReplaceAll(valid, `}]`, `},{"date":"2026-02-26","base":"USD","quote":"JPY","rate":"<script>"}]`),
		"trailing JSON":      valid + `{"rate":1}`,
		"oversized body":     valid + strings.Repeat(" ", subscriptionRateBodyLimit),
	} {
		t.Run(name, func(t *testing.T) {
			responseBody = payload
			result, err := service.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), fetchSubscriptionRate)
			if err != nil || result.Created != 0 || result.HasMore || len(result.Errors) != 1 || result.Errors[0] != ErrSubscriptionRateUnavailable.Error() {
				t.Fatalf("untrusted response result=%+v error=%v", result, err)
			}
			var occurrences int
			if err := instance.DB().QueryRow("SELECT COUNT(*) FROM subscription_occurrences").Scan(&occurrences); err != nil || occurrences != 0 {
				t.Fatalf("untrusted occurrence persisted: count=%d error=%v", occurrences, err)
			}
			transactions, err := service.GetTransactions("", "")
			if err != nil || len(transactions) != len(before) || transactions[0].ID != before[0].ID || transactions[0].BalanceExact != before[0].BalanceExact {
				t.Fatalf("ledger changed: transactions=%+v error=%v", transactions, err)
			}
			rules, err := service.Subscriptions()
			if err != nil || len(rules) != 1 || rules[0].ID != rule.ID || rules[0].Revision != rule.Revision || rules[0].NextDate != rule.NextDate || rules[0].NextSequence != rule.NextSequence || !rules[0].Enabled {
				t.Fatalf("schedule changed: rules=%+v error=%v", rules, err)
			}
		})
	}
	responseBody = valid
	result, err := service.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), fetchSubscriptionRate)
	if err != nil || result.Created != 1 || len(result.Errors) != 0 {
		t.Fatalf("valid retry=%+v error=%v", result, err)
	}
	history, err := service.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if err != nil || len(history.Transactions) != 1 || history.Transactions[0].AmountExact != "1503" || history.Transactions[0].Subscription.RateDate != "2026-02-27" || history.Transactions[0].Subscription.Rate != "150.5" {
		t.Fatalf("validated quote not applied once: history=%+v error=%v", history, err)
	}
	requestsAfterSuccess := requests
	result, err = service.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), fetchSubscriptionRate)
	if err != nil || result.Created != 0 || requests != requestsAfterSuccess {
		t.Fatalf("successful payment duplicated: result=%+v error=%v", result, err)
	}
}

func FuzzSubscriptionRateJSON(f *testing.F) {
	f.Add([]byte(subscriptionFXProviderSample))
	f.Add([]byte(`[{"date":"2024-05-15","base":"USD","quote":"JPY","rate":155.49}]`))
	f.Add([]byte(`[{"date":"2024-05-15","base":"USD","quote":"JPY","rate":1,"ra\u0074e":2}]`))
	f.Add([]byte(`[{"date":"2024-05-15","base":"USD","quote":"JPY","rate":"155.49"}]`))
	f.Add([]byte{0xff, '[', ']'})
	f.Fuzz(func(t *testing.T, data []byte) {
		original := bytes.Clone(data)
		quote, err := decodeSubscriptionRate(data, "USD", "2024-05-15")
		if !bytes.Equal(data, original) {
			t.Fatal("validation mutated untrusted bytes")
		}
		if err != nil {
			if !errors.Is(err, ErrSubscriptionRateUnavailable) || quote != (subscriptionRate{}) {
				t.Fatal("failed validation returned untrusted output")
			}
			return
		}
		if len(data) > subscriptionRateBodyLimit || !utf8.Valid(data) || quote.Date < "2024-05-08" || quote.Date > "2024-05-15" || !validSubscriptionRate(quote.Rate) {
			t.Fatal("accepted quote violated boundary")
		}
		// A valid document must stop being valid when an extra value, an unknown
		// field or a duplicate (including an existing escaped key) is injected.
		if _, err := decodeSubscriptionRate(append(bytes.Clone(data), []byte(`{}`)...), "USD", "2024-05-15"); err == nil {
			t.Fatal("accepted appended JSON")
		}
		for _, injected := range []string{`"unexpected":null,`, `"rate":1,`} {
			modified := bytes.Replace(data, []byte("{"), []byte("{"+injected), 1)
			if _, err := decodeSubscriptionRate(modified, "USD", "2024-05-15"); err == nil {
				t.Fatal("accepted injected object field")
			}
		}
	})
}
