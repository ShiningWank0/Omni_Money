package main

import (
	"context"
	"fmt"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/models"
)

// App はWailsバインディング用のアプリケーション構造体
type App struct {
	ctx context.Context
}

// NewApp は新しいAppインスタンスを作成する
func NewApp() *App {
	return &App{}
}

// startup はアプリ起動時に呼ばれるコールバック
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// --- 口座関連 ---

// GetAccounts はデータベースから口座名のリストを返す
func (a *App) GetAccounts() ([]string, error) {
	return core.GetAccounts()
}

// --- 取引関連 ---

// GetTransactions は取引履歴を返す
func (a *App) GetTransactions(account string, search string) ([]models.TransactionResponse, error) {
	return core.GetTransactions(account, search)
}

// AddTransaction は新しい取引を追加する
func (a *App) AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	return core.AddTransaction(req)
}

// UpdateTransaction は既存の取引を更新する
func (a *App) UpdateTransaction(id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	return core.UpdateTransaction(id, req)
}

// DeleteTransaction は取引を削除する
func (a *App) DeleteTransaction(id int64) error {
	return core.DeleteTransaction(id)
}

// --- 残高関連 ---

// GetBalanceHistory は残高推移データを返す
func (a *App) GetBalanceHistory() (*models.BalanceHistoryResponse, error) {
	return core.GetBalanceHistory()
}

// GetBalanceHistoryFiltered はクレジットカード除外を考慮した残高推移データを返す
func (a *App) GetBalanceHistoryFiltered(fundItems []string) (*models.BalanceHistoryResponse, error) {
	return core.GetBalanceHistoryFiltered(fundItems)
}

// --- 項目関連 ---

// GetItems は項目名のリストを返す
func (a *App) GetItems(account string) ([]string, error) {
	return core.GetItems(account)
}

// --- クレジットカード設定 ---

// GetCreditCardSettings はクレジットカード設定を取得する
func (a *App) GetCreditCardSettings() ([]string, error) {
	return core.GetCreditCardSettings()
}

// SaveCreditCardSettings はクレジットカード設定を保存する
func (a *App) SaveCreditCardSettings(items []string) error {
	return core.SaveCreditCardSettings(items)
}

// --- CSV関連 ---

// BackupToCSV はCSVバックアップを作成する
func (a *App) BackupToCSV() (string, error) {
	return core.BackupToCSV()
}

// ImportCSV はCSVファイルからデータをインポートする
func (a *App) ImportCSV(content string, mode string) (int, error) {
	return core.ImportCSV(content, mode)
}

// --- スナップショット関連 ---

// CreateSnapshot はデータベースのスナップショットを作成する
func (a *App) CreateSnapshot() (string, error) {
	return database.CreateSnapshot("")
}

// ListSnapshots はスナップショットの一覧を返す
func (a *App) ListSnapshots() ([]string, error) {
	return database.ListSnapshots("")
}

// RestoreSnapshot はスナップショットからデータベースを復元する
func (a *App) RestoreSnapshot(name string) error {
	return database.RestoreSnapshot("", name)
}

// Greet は挨拶を返す（テスト用）
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, Omni Moneyへようこそ!", name)
}
