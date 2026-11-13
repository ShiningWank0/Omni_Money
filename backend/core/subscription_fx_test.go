package core

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
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
	for _, test := range []struct {
		status  int
		body    string
		invalid bool
	}{
		{200, `[{"date":"2024-05-13","base":"USD","quote":"JPY","rate":155.86}]`, false},
		{302, `https://localhost/private`, true}, {500, `private error`, true}, {200, strings.Repeat("x", 16385), true},
	} {
		subscriptionRateClient = &http.Client{Transport: subscriptionRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "https" || request.URL.Host != "api.frankfurter.dev" || request.URL.Path != "/v2/providers/ecb/rates" || request.Method != "GET" {
				t.Fatalf("unexpected destination=%s", request.URL)
			}
			query := request.URL.Query()
			if len(query) != 4 || query.Get("base") != "USD" || query.Get("quotes") != "JPY" || query.Get("from") != "2024-05-07" || query.Get("to") != "2024-05-14" || request.Body != nil || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
				t.Fatalf("private input in request: %v", request)
			}
			return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
		})}
		_, err := fetchSubscriptionRate(context.Background(), "USD", "2024-05-14")
		if (err != nil) != test.invalid {
			t.Fatalf("status=%d invalid=%v error=%v", test.status, test.invalid, err)
		}
	}
}
