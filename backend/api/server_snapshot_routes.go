package api

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"

	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/vault"
)

type serverSnapshotRestoreRequest struct {
	Name string `json:"name"`
}

func handleServerSnapshots(w http.ResponseWriter, r *http.Request) {
	snapshots, ok := middleware.SnapshotServiceFromContext(r.Context())
	if !ok {
		jsonError(w, "ユーザーデータを安全に開けません", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := snapshots.ListSnapshotsContext(r.Context())
		if err != nil {
			log.Printf("security_event=snapshot_list result=error")
			jsonError(w, "スナップショットを利用できません", http.StatusServiceUnavailable)
			return
		}
		if entries == nil {
			entries = []string{}
		}
		jsonResponse(w, entries, http.StatusOK)
	case http.MethodPost:
		path, err := snapshots.CreateSnapshotContext(r.Context())
		if err != nil {
			log.Printf("security_event=snapshot_create result=error")
			jsonError(w, "スナップショットを作成できません", http.StatusServiceUnavailable)
			return
		}
		// Never expose the vault path in an API response or audit record.
		jsonResponse(w, map[string]string{
			"path": filepath.Base(path), "name": filepath.Base(path),
			"message": "スナップショットを作成しました",
		}, http.StatusCreated)
		log.Printf("security_event=snapshot_create result=success")
	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleServerSnapshotRestore(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" || dependencies.Snapshots == nil {
			writeAuthRequired(w)
			return
		}
		var request serverSnapshotRestoreRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		if request.Name == "" {
			jsonError(w, "スナップショット名が必要です", http.StatusBadRequest)
			return
		}
		if err := database.ValidateSnapshotName(request.Name); err != nil {
			jsonError(w, "スナップショット名が不正です", http.StatusBadRequest)
			return
		}
		operation, userID, err := dependencies.Snapshots.BeginRestore(session.ID)
		if err != nil {
			log.Printf("security_event=snapshot_restore result=begin_error")
			switch {
			case errors.Is(err, vault.ErrRestoreInFlight):
				jsonError(w, "復元処理が既に実行中です", http.StatusConflict)
			case errors.Is(err, vault.ErrDraining), errors.Is(err, vault.ErrClosed):
				jsonError(w, "アカウントサービスを利用できません", http.StatusServiceUnavailable)
			default:
				writeAuthRequired(w)
			}
			return
		}
		if operation == nil || userID == "" || userID != session.UserID {
			if operation != nil {
				operation.Release()
			}
			dependencies.Sessions.ClearSessionCookie(w, r)
			w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
			log.Printf("security_event=snapshot_restore result=identity_error")
			jsonError(w, "アカウントサービスを利用できません", http.StatusServiceUnavailable)
			return
		}
		// The capability has already atomically drained the exact vault entry.
		// Invalidate every session for this user, including the current one, so
		// no stale root can be used after restore.  Other users are untouched.
		dependencies.Sessions.DeleteAllSessionsForUser(userID)
		// Restore invalidates the current root as well as every sibling session.
		// Clear the browser credential before doing the potentially slow disk
		// operation; both success and failure require a fresh login.
		dependencies.Sessions.ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		err = operation.RestoreSnapshot(r.Context(), request.Name)
		if err != nil {
			log.Printf("security_event=snapshot_restore result=error")
			jsonResponse(w, map[string]interface{}{
				"error": "スナップショットを復元できません", "login_required": true,
			}, http.StatusInternalServerError)
			return
		}
		log.Printf("security_event=snapshot_restore result=success")
		jsonResponse(w, map[string]interface{}{
			"message":         "スナップショットから復元しました。再ログインしてください",
			"reauth_required": true,
			"login_required":  true,
		}, http.StatusOK)
	}
}
