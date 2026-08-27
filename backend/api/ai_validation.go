package api

import (
	"fmt"
	"strings"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

// normalizeAndValidateAITransaction はAI専用入口だけに適用する入力境界。
// 人間が通常UIから登録する取引には日付範囲制限を適用しない。
func normalizeAndValidateAITransaction(req models.TransactionRequest, now time.Time) (models.TransactionRequest, error) {
	req.Account = strings.TrimSpace(req.Account)
	req.Date = strings.TrimSpace(req.Date)
	req.Time = strings.TrimSpace(req.Time)
	req.Item = strings.TrimSpace(req.Item)
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Memo = strings.TrimSpace(req.Memo)

	if req.Account == "" {
		return req, fmt.Errorf("口座名は必須です")
	}
	if req.Date == "" {
		return req, fmt.Errorf("日付は必須です")
	}
	if req.Item == "" {
		return req, fmt.Errorf("項目は必須です")
	}
	if req.Type != "income" && req.Type != "expense" {
		return req, fmt.Errorf("種別はincomeまたはexpenseである必要があります")
	}
	if err := validation.ValidateTransactionAmount(req.Amount); err != nil {
		return req, err
	}

	location := now.Location()
	date, err := time.ParseInLocation("2006-01-02", req.Date, location)
	if err != nil {
		return req, fmt.Errorf("日付はYYYY-MM-DD形式で指定してください")
	}
	if req.Time != "" {
		if _, err := time.ParseInLocation("15:04", req.Time, location); err != nil {
			return req, fmt.Errorf("時刻はHH:MM形式で指定してください")
		}
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	minDate := today.AddDate(-1, 0, 0)
	maxDate := today.AddDate(0, 0, 2)
	if date.Before(minDate) || date.After(maxDate) {
		return req, fmt.Errorf(
			"AI経由の取引日は%sから%sまでの範囲で指定してください",
			minDate.Format("2006-01-02"),
			maxDate.Format("2006-01-02"),
		)
	}

	return req, nil
}

// validateAITransactionReferences はAI入力に含まれるDB参照を事前検証する。
// 画像は通常Web/Wailsと同じcore.AddTransaction境界で検証する。
func validateAITransactionReferences(req models.TransactionRequest) (models.TransactionRequest, error) {
	if len(req.Tags) > 0 {
		uniqueTags := make([]int64, 0, len(req.Tags))
		seen := make(map[int64]struct{}, len(req.Tags))
		for _, tagID := range req.Tags {
			if tagID <= 0 {
				return req, fmt.Errorf("タグIDは正の整数で指定してください")
			}
			if _, exists := seen[tagID]; exists {
				continue
			}
			seen[tagID] = struct{}{}
			uniqueTags = append(uniqueTags, tagID)
		}

		placeholders := make([]string, len(uniqueTags))
		args := make([]interface{}, len(uniqueTags))
		for i, tagID := range uniqueTags {
			placeholders[i] = "?"
			args[i] = tagID
		}
		var count int
		err := database.GetDB().QueryRow(
			"SELECT COUNT(*) FROM tags WHERE id IN ("+strings.Join(placeholders, ",")+")",
			args...,
		).Scan(&count)
		if err != nil {
			return req, fmt.Errorf("タグの存在確認に失敗しました: %w", err)
		}
		if count != len(uniqueTags) {
			return req, fmt.Errorf("存在しないタグIDが含まれています")
		}
		req.Tags = uniqueTags
	}

	return req, nil
}
