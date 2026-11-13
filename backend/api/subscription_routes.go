package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"omni_money/backend/core"
)

func subscriptionError(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, sql.ErrNoRows) {
		jsonSafeError(w, "サブスクが見つかりません", http.StatusNotFound)
		return
	}
	if errors.Is(err, core.ErrSubscriptionConflict) {
		jsonSafeError(w, err.Error(), http.StatusConflict)
		return
	}
	var inputError *core.SubscriptionValidationError
	if status < http.StatusInternalServerError && errors.As(err, &inputError) {
		jsonSafeError(w, inputError.Error(), status)
		return
	}
	writeFinancialError(w, err, status)
}

func handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		rules, err := service.Subscriptions()
		if err != nil {
			subscriptionError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, rules, http.StatusOK)
		return
	}
	saveSubscription(w, r, service, "")
}

func saveSubscription(w http.ResponseWriter, r *http.Request, service *core.Service, id string) {
	var input core.SubscriptionInput
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		jsonError(w, financialInvalidRequestMessage, http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		jsonError(w, financialInvalidRequestMessage, http.StatusBadRequest)
		return
	}
	rule, err := service.SaveSubscription(id, input)
	if err != nil {
		subscriptionError(w, err, http.StatusBadRequest)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	jsonResponse(w, rule, status)
}

func handleSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || !core.ValidSaveRequestID(parts[0]) {
		jsonSafeError(w, "サブスクが見つかりません", http.StatusNotFound)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodPut {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		saveSubscription(w, r, service, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "transactions" {
		if r.Method != http.MethodGet {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		offset := 0
		if raw := query.Get("offset"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				jsonError(w, financialInvalidRequestMessage, http.StatusBadRequest)
				return
			}
			offset = value
		}
		history, err := service.SubscriptionTransactions(parts[0], query.Get("account"), query.Get("search"), query.Get("start"), query.Get("end"), offset)
		if err != nil {
			subscriptionError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, history, http.StatusOK)
		return
	}
	http.NotFound(w, r)
}

func handleSubscriptionSync(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	result, err := service.SyncSubscriptions(r.Context())
	if err != nil {
		subscriptionError(w, err, http.StatusServiceUnavailable)
		return
	}
	jsonResponse(w, result, http.StatusOK)
}

func handleSubscriptionCalendar(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	entries, err := service.SubscriptionCalendar(r.URL.Query().Get("month"))
	if err != nil {
		subscriptionError(w, err, http.StatusBadRequest)
		return
	}
	jsonResponse(w, entries, http.StatusOK)
}

func handleSubscriptionCandidates(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	candidates, err := service.SubscriptionCandidates()
	if err != nil {
		subscriptionError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, candidates, http.StatusOK)
}

func handleSubscriptionCandidateDismiss(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	fingerprint := strings.TrimPrefix(r.URL.Path, "/api/subscription-candidates/")
	if err := service.DismissSubscriptionCandidate(fingerprint); err != nil {
		subscriptionError(w, err, http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]bool{"success": true}, http.StatusOK)
}
