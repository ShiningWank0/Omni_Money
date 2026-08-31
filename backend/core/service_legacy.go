package core

import (
	"context"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

// This file preserves the historical package-level API used by Desktop/Wails.
// Server code must construct an explicit Service with NewService instead.

// newLegacyService is the sole bridge to the historical package-level
// database. It exists for Desktop/Wails and source-compatible tests only.
func newLegacyService() (*Service, error) {
	db := database.GetDB()
	if db == nil {
		return nil, ErrServiceUnavailable
	}
	return &Service{
		db:       db,
		legacy:   true,
		snapshot: database.AutoSnapshot,
	}, nil
}

// NewLegacyService returns an explicit Service backed by the historical
// package-level Desktop database. Only compatibility wiring such as the legacy
// router may use it; multi-user server code must use a request vault Service.
func NewLegacyService() (*Service, error) {
	return newLegacyService()
}

func GetAccounts() ([]string, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetAccounts()
}

func GetItems(account string) ([]string, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetItems(account)
}

func GetTransactions(account, search string) ([]models.TransactionResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTransactions(account, search)
}

func AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	return AddTransactionContext(context.Background(), req)
}

func AddTransactionContext(ctx context.Context, req models.TransactionRequest) (*models.TransactionResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.AddTransactionContext(ctx, req)
}

func UpdateTransaction(id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	return UpdateTransactionContext(context.Background(), id, req)
}

func UpdateTransactionContext(ctx context.Context, id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.UpdateTransactionContext(ctx, id, req)
}

func DeleteTransaction(id int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.DeleteTransaction(id)
}

func GetBalanceHistory() (*models.BalanceHistoryResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetBalanceHistory()
}

func GetBalanceHistoryFiltered(fundItems []string) (*models.BalanceHistoryResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetBalanceHistoryFiltered(fundItems)
}

func GetCreditCardSettings() ([]string, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetCreditCardSettings()
}

func SaveCreditCardSettings(items []string) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.SaveCreditCardSettings(items)
}

func GetBankAccountSettings() ([]string, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetBankAccountSettings()
}

func SaveBankAccountSettings(items []string) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.SaveBankAccountSettings(items)
}

func BackupToCSV() (string, error) {
	s, err := newLegacyService()
	if err != nil {
		return "", err
	}
	return s.BackupToCSV()
}

// BackupToCSVV2 is an explicit transactions-only compatibility export. It is
// not a complete backup and is intended only for append/import by old clients.
func BackupToCSVV2() (string, error) {
	s, err := newLegacyService()
	if err != nil {
		return "", err
	}
	return s.BackupToCSVV2()
}

// BackupToCSVFull always emits the normalized v3 export, including extension
// columns for images, tags, links, and settings even when they are empty.
func BackupToCSVFull() (string, error) {
	s, err := newLegacyService()
	if err != nil {
		return "", err
	}
	return s.BackupToCSVFull()
}

func BackupToCSVFile() (string, error) {
	s, err := newLegacyService()
	if err != nil {
		return "", err
	}
	return s.BackupToCSVFile()
}

func BackupToCSVDirectory(destination string) (string, error) {
	s, err := newLegacyService()
	if err != nil {
		return "", err
	}
	return s.BackupToCSVDirectory(destination)
}

func ImportCSV(content, mode string) (int, error) {
	s, err := newLegacyService()
	if err != nil {
		return 0, err
	}
	return s.ImportCSV(content, mode)
}

func recalculateBalance(account string) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.recalculateBalance(account)
}

func AddTransactionImage(transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	return AddTransactionImageContext(context.Background(), transactionID, img)
}

func AddTransactionImageContext(ctx context.Context, transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.AddTransactionImageContext(ctx, transactionID, img)
}

func GetTransactionImages(transactionID int64) ([]models.TransactionImageResponse, error) {
	return GetTransactionImagesContext(context.Background(), transactionID)
}

func GetTransactionImagesContext(ctx context.Context, transactionID int64) ([]models.TransactionImageResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTransactionImagesContext(ctx, transactionID)
}

func DeleteTransactionImage(imageID int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.DeleteTransactionImage(imageID)
}

func DeleteTransactionImageForTransaction(transactionID, imageID int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.DeleteTransactionImageForTransaction(transactionID, imageID)
}

func GetImageStorageUsage() (*models.ImageStorageUsage, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetImageStorageUsage()
}

func CreateTag(name string, parentID *int64) (*models.Tag, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.CreateTag(name, parentID)
}

func GetTags() ([]models.Tag, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTags()
}

func UpdateTag(id int64, name string) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.UpdateTag(id, name)
}

func CreateTagByPath(path string) (*models.Tag, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.CreateTagByPath(path)
}

func DeleteTag(id int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.DeleteTag(id)
}

func GetTransactionTags(transactionID int64) ([]models.Tag, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTransactionTags(transactionID)
}

func AddTransactionTags(transactionID int64, tagIDs []int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.AddTransactionTags(transactionID, tagIDs)
}

func RemoveTransactionTag(transactionID, tagID int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.RemoveTransactionTag(transactionID, tagID)
}

func GetTagSummary(txType, startDate, endDate string) ([]models.TagSummary, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTagSummary(txType, startDate, endDate)
}

func getTagSummaryFiltered(txType, startDate, endDate, account string, tagIDs []int64) ([]models.TagSummary, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.getTagSummaryFiltered(txType, startDate, endDate, account, tagIDs)
}

func getTagSummaryFilteredContext(
	ctx context.Context,
	txType, startDate, endDate, account string,
	tagIDs []int64,
	options tagSummaryOptions,
) ([]models.TagSummary, bool, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, false, err
	}
	return s.getTagSummaryFilteredContext(ctx, txType, startDate, endDate, account, tagIDs, options)
}

func AnalyzeTransactions(req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	return AnalyzeTransactionsContext(context.Background(), req)
}

func AnalyzeTransactionsContext(ctx context.Context, req models.AnalysisRequest) (*models.AnalysisResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.AnalyzeTransactionsContext(ctx, req)
}

func GetTransactionLinks(transactionID int64) ([]models.LinkedTransactionResponse, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.GetTransactionLinks(transactionID)
}

func AddTransactionLink(parentID, childID int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.AddTransactionLink(parentID, childID)
}

func RemoveTransactionLink(transactionID, linkedID int64) error {
	s, err := newLegacyService()
	if err != nil {
		return err
	}
	return s.RemoveTransactionLink(transactionID, linkedID)
}

func AddAITransaction(ctx context.Context, req models.TransactionRequest, identity AITransactionIdentity) (*AITransactionResult, error) {
	s, err := newLegacyService()
	if err != nil {
		return nil, err
	}
	return s.AddAITransaction(ctx, req, identity)
}
