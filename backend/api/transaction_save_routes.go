package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"omni_money/backend/core"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

type transactionSaveRequest struct {
	UserID      string                    `json:"user_id"`
	RequestID   string                    `json:"request_id"`
	TargetID    int64                     `json:"target_id"`
	Transaction models.TransactionRequest `json:"transaction"`
}

func handleTransactionSaves(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		notices, err := service.TransactionSaveNotices()
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{"saves": notices}, http.StatusOK)
		return
	}
	var request transactionSaveRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		jsonError(w, financialInvalidRequestMessage, http.StatusBadRequest)
		return
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		jsonError(w, financialInvalidRequestMessage, http.StatusBadRequest)
		return
	}
	session, authenticated := middleware.SessionFromContext(r.Context())
	if !authenticated || request.UserID != session.UserID || request.UserID == "" {
		jsonError(w, "保存要求のアカウントが一致しません", http.StatusForbidden)
		return
	}
	result, err := service.AcceptTransactionSave(request.RequestID, request.TargetID, request.Transaction)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSaveRequestConflict):
			jsonError(w, "保存要求IDが別の操作に使用されています", http.StatusConflict)
		case errors.Is(err, core.ErrSaveQueueFull):
			jsonError(w, "保存受付が混み合っています。再試行してください", http.StatusTooManyRequests)
		case errors.Is(err, core.ErrSaveStorageUnavailable):
			writeFinancialError(w, err, http.StatusServiceUnavailable)
		default:
			writeFinancialError(w, err, http.StatusBadRequest)
		}
		return
	}
	jsonResponse(w, result, http.StatusAccepted)
}

func handleTransactionSaveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/transaction-saves/")
	if r.Method == http.MethodDelete {
		err := service.DismissFailedTransactionSave(id)
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "保存要求が見つかりません", http.StatusNotFound)
		} else if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
		} else {
			jsonResponse(w, map[string]bool{"success": true}, http.StatusOK)
		}
		return
	}
	result, err := service.TransactionSaveStatus(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "保存要求が見つかりません", http.StatusNotFound)
		} else {
			writeFinancialError(w, err, http.StatusInternalServerError)
		}
		return
	}
	jsonResponse(w, result, http.StatusOK)
}
