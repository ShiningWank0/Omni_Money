package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"omni_money/backend/core"
	"omni_money/backend/desktopaccount"
	"omni_money/backend/fileprivacy"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/models"
)

var ErrDesktopVaultChanged = errors.New("Desktop vault was locked or reopened while the operation was awaiting input")
var ErrDesktopCSVSelectionCanceled = errors.New("CSV選択がキャンセルされました")

type desktopVaultCoordinator interface {
	Status() desktopaccount.Status
	Setup(password []byte) ([]byte, error)
	MigrateLegacy(password []byte) ([]byte, error)
	AcknowledgeRecovery() error
	Unlock(password []byte) error
	Recover(recoverySecret, newPassword []byte) ([]byte, error)
	ChangePassword(currentPassword, newPassword []byte) error
	RotateRecovery(currentPassword, recoverySecret []byte) error
	Service() (*desktopaccount.ServiceLease, error)
	CreateSnapshot() (string, error)
	ListSnapshots() ([]string, error)
	RestoreSnapshot(name string) error
	Lock() error
	Close() error
}

// DesktopVaultRecoveryResponse returns non-secret status plus a one-time
// recovery code. The code must be saved outside Omni Money before continuing.
type DesktopVaultRecoveryResponse struct {
	Status       desktopaccount.Status `json:"status"`
	RecoveryCode string                `json:"recovery_code"`
}

// App is the Wails binding surface. It owns only the Desktop account
// coordinator; the encrypted database is opened only after explicit setup,
// unlock, or recovery.
type App struct {
	mu                 sync.Mutex
	ctx                context.Context
	coordinator        desktopVaultCoordinator
	generation         uint64
	chooseCSVDirectory func(context.Context) (string, error)
	chooseCSVFile      func(context.Context) (string, error)
}

// NewApp prepares the Desktop account coordinator without opening a vault.
func NewApp(dataRoot string) (*App, error) {
	coordinator, err := desktopaccount.New(dataRoot)
	if err != nil {
		return nil, err
	}
	return newAppWithCoordinator(coordinator), nil
}

func newAppWithCoordinator(coordinator desktopVaultCoordinator) *App {
	return &App{
		coordinator: coordinator,
		chooseCSVDirectory: func(ctx context.Context) (string, error) {
			return wailsRuntime.OpenDirectoryDialog(ctx, wailsRuntime.OpenDialogOptions{
				Title:                "CSVの保存先（暗号化されたボリューム）を選択",
				CanCreateDirectories: true,
				ResolvesAliases:      true,
			})
		},
		chooseCSVFile: func(ctx context.Context) (string, error) {
			return wailsRuntime.OpenFileDialog(ctx, wailsRuntime.OpenDialogOptions{
				Title:   "CSVバックアップを選択",
				Filters: []wailsRuntime.FileFilter{{DisplayName: "CSV", Pattern: "*.csv"}},
			})
		},
	}
}

// startup records the Wails context but deliberately leaves the vault locked.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
}

func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	coordinator := a.coordinator
	a.generation++
	a.ctx = nil
	a.mu.Unlock()
	if coordinator != nil {
		_ = coordinator.Close()
	}
}

func (a *App) borrowService() (*core.Service, func(), error) {
	if a == nil || a.coordinator == nil {
		return nil, nil, desktopaccount.ErrClosed
	}
	lease, err := a.coordinator.Service()
	if err != nil {
		return nil, nil, err
	}
	service, err := lease.Core()
	if err != nil {
		lease.Release()
		return nil, nil, err
	}
	return service, lease.Release, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func recoveryResponse(status desktopaccount.Status, secret []byte) DesktopVaultRecoveryResponse {
	return DesktopVaultRecoveryResponse{
		Status:       status,
		RecoveryCode: base64.RawURLEncoding.EncodeToString(secret),
	}
}

// GetDesktopVaultStatus is the only data-bearing call the frontend needs
// before unlock. Status contains no secret material and never opens the vault.
func (a *App) GetDesktopVaultStatus() desktopaccount.Status {
	if a == nil || a.coordinator == nil {
		return desktopaccount.Status{}
	}
	return a.coordinator.Status()
}

func (a *App) SetupDesktopVault(password string) (DesktopVaultRecoveryResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	passwordBytes := []byte(password)
	defer clearBytes(passwordBytes)
	secret, err := a.coordinator.Setup(passwordBytes)
	if err != nil {
		return DesktopVaultRecoveryResponse{}, err
	}
	defer clearBytes(secret)
	a.generation++
	return recoveryResponse(a.coordinator.Status(), secret), nil
}

// MigrateLegacyDesktopVault performs the explicit, journaled conversion of a
// historical plaintext Desktop database. The returned recovery code remains a
// pending delivery until AcknowledgeDesktopVaultRecovery succeeds, so the UI
// must not enter the financial application before that acknowledgement.
func (a *App) MigrateLegacyDesktopVault(password string) (DesktopVaultRecoveryResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	passwordBytes := []byte(password)
	defer clearBytes(passwordBytes)
	secret, err := a.coordinator.MigrateLegacy(passwordBytes)
	if err != nil {
		return DesktopVaultRecoveryResponse{}, err
	}
	defer clearBytes(secret)
	a.generation++
	return recoveryResponse(a.coordinator.Status(), secret), nil
}

// AcknowledgeDesktopVaultRecovery durably records that any pending migration
// recovery code was delivered. It is intentionally idempotent for setup and
// ordinary recovery, allowing the frontend to use one safe completion path.
func (a *App) AcknowledgeDesktopVaultRecovery() (desktopaccount.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.coordinator.AcknowledgeRecovery(); err != nil {
		return a.coordinator.Status(), err
	}
	a.generation++
	return a.coordinator.Status(), nil
}

func (a *App) UnlockDesktopVault(password string) (desktopaccount.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	passwordBytes := []byte(password)
	defer clearBytes(passwordBytes)
	if err := a.coordinator.Unlock(passwordBytes); err != nil {
		return a.coordinator.Status(), err
	}
	a.generation++
	return a.coordinator.Status(), nil
}

func (a *App) RecoverDesktopVault(recoveryCode, newPassword string) (DesktopVaultRecoveryResponse, error) {
	recoverySecret, err := base64.RawURLEncoding.Strict().DecodeString(recoveryCode)
	if err != nil || len(recoverySecret) != keyenvelope.RecoverySecretSize {
		clearBytes(recoverySecret)
		return DesktopVaultRecoveryResponse{}, keyenvelope.ErrAuthentication
	}
	defer clearBytes(recoverySecret)
	passwordBytes := []byte(newPassword)
	defer clearBytes(passwordBytes)

	a.mu.Lock()
	defer a.mu.Unlock()
	nextSecret, err := a.coordinator.Recover(recoverySecret, passwordBytes)
	if err != nil {
		return DesktopVaultRecoveryResponse{}, err
	}
	defer clearBytes(nextSecret)
	a.generation++
	return recoveryResponse(a.coordinator.Status(), nextSecret), nil
}

func (a *App) LockDesktopVault() (desktopaccount.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.coordinator.Lock(); err != nil {
		return a.coordinator.Status(), err
	}
	a.generation++
	return a.coordinator.Status(), nil
}

func (a *App) ChangeDesktopVaultPassword(currentPassword, newPassword string) (desktopaccount.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	currentBytes := []byte(currentPassword)
	newBytes := []byte(newPassword)
	defer clearBytes(currentBytes)
	defer clearBytes(newBytes)
	if err := a.coordinator.ChangePassword(currentBytes, newBytes); err != nil {
		return a.coordinator.Status(), err
	}
	return a.coordinator.Status(), nil
}

func (a *App) RotateDesktopVaultRecovery(currentPassword, recoveryCode string) (desktopaccount.Status, error) {
	secret, err := base64.RawURLEncoding.Strict().DecodeString(recoveryCode)
	if err != nil || len(secret) != keyenvelope.RecoverySecretSize {
		clearBytes(secret)
		return a.coordinator.Status(), keyenvelope.ErrInvalidRecoverySecret
	}
	defer clearBytes(secret)
	a.mu.Lock()
	defer a.mu.Unlock()
	passwordBytes := []byte(currentPassword)
	defer clearBytes(passwordBytes)
	if err := a.coordinator.RotateRecovery(passwordBytes, secret); err != nil {
		return a.coordinator.Status(), err
	}
	return a.coordinator.Status(), nil
}

// --- Accounts ---

func (a *App) GetAccounts() ([]string, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetAccounts()
}

// --- Transactions ---

func (a *App) GetTransactions(account string, search string) ([]models.TransactionResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTransactions(account, search)
}

func (a *App) AddTransaction(req models.TransactionRequest) (*models.TransactionResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.AddTransaction(req)
}

func (a *App) UpdateTransaction(id int64, req models.TransactionRequest) (*models.TransactionResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.UpdateTransaction(id, req)
}

func (a *App) DeleteTransaction(id int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.DeleteTransaction(id)
}

// --- Balances ---

func (a *App) GetBalanceHistory() (*models.BalanceHistoryResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetBalanceHistory()
}

func (a *App) GetBalanceHistoryFiltered(fundItems []string) (*models.BalanceHistoryResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetBalanceHistoryFiltered(fundItems)
}

// --- Items and settings ---

func (a *App) GetItems(account string) ([]string, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetItems(account)
}

func (a *App) GetCreditCardSettings() ([]string, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetCreditCardSettings()
}

func (a *App) SaveCreditCardSettings(items []string) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.SaveCreditCardSettings(items)
}

func (a *App) GetBankAccountSettings() ([]string, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetBankAccountSettings()
}

func (a *App) SaveBankAccountSettings(items []string) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.SaveBankAccountSettings(items)
}

// --- CSV ---

// BackupToCSVFile does not retain a service lease while the native directory
// dialog is open. It rejects the operation if a lock/reopen boundary occurred
// while the user was choosing a destination.
func (a *App) BackupToCSVFile() (string, error) {
	a.mu.Lock()
	if !a.coordinator.Status().Unlocked {
		a.mu.Unlock()
		return "", desktopaccount.ErrLocked
	}
	ctx := a.ctx
	generation := a.generation
	chooser := a.chooseCSVDirectory
	a.mu.Unlock()

	if chooser == nil {
		return "", errors.New("CSV directory chooser is unavailable")
	}
	destination, err := chooser(ctx)
	if err != nil {
		return "", fmt.Errorf("CSV保存先を選択できませんでした: %w", err)
	}
	if destination == "" {
		return "", ErrDesktopCSVSelectionCanceled
	}

	a.mu.Lock()
	if generation != a.generation || !a.coordinator.Status().Unlocked {
		a.mu.Unlock()
		return "", ErrDesktopVaultChanged
	}
	lease, err := a.coordinator.Service()
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	defer lease.Release()
	service, err := lease.Core()
	if err != nil {
		return "", err
	}
	return service.BackupToCSVDirectory(destination)
}

// BackupToCSVFull emits the stable normalized v3 CSV schema. BackupToCSVFile
// remains the default UI path and selects v3 automatically when extended data
// is present, while this method is available to integrations that need a
// schema-stable export for an empty ledger as well.
func (a *App) BackupToCSVFull() (string, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return "", err
	}
	defer release()
	return service.BackupToCSVFull()
}

func (a *App) ImportCSV(content string, mode string) (int, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return 0, err
	}
	defer release()
	return service.ImportCSV(content, mode)
}

// ImportCSVFile imports through a native file descriptor and is the preferred
// Desktop binding for v3 archives; the string method above remains for older
// Wails clients.
func (a *App) ImportCSVFile(mode string) (count int, retErr error) {
	a.mu.Lock()
	ctx := a.ctx
	chooser := a.chooseCSVFile
	if a.coordinator == nil || !a.coordinator.Status().Unlocked {
		a.mu.Unlock()
		return 0, desktopaccount.ErrLocked
	}
	generation := a.generation
	a.mu.Unlock()
	if chooser == nil {
		return 0, errors.New("CSV file chooser is unavailable")
	}
	path, err := chooser(ctx)
	if err != nil {
		return 0, fmt.Errorf("CSVファイルを選択できませんでした: %w", err)
	}
	if path == "" {
		return 0, ErrDesktopCSVSelectionCanceled
	}
	// Do not even open a picker-selected path after the vault has crossed a
	// lock/reopen boundary. Apart from avoiding unnecessary I/O, this keeps a
	// stale selection from creating a private snapshot that cannot be imported.
	a.mu.Lock()
	if generation != a.generation || a.coordinator == nil || !a.coordinator.Status().Unlocked {
		a.mu.Unlock()
		return 0, ErrDesktopVaultChanged
	}
	a.mu.Unlock()
	// Copy the picker result into a private, stable descriptor before borrowing
	// the vault service. The selected path is untrusted and may be replaced or
	// mutated while the user is choosing; the parser must never consume it
	// directly or let an external writer race the DB transaction.
	source, err := openDesktopCSVFile(path)
	if err != nil {
		return 0, err
	}
	snapshot, cleanupSnapshot, err := snapshotDesktopCSV(ctx, path, source)
	sourceCloseErr := source.Close()
	if err != nil {
		if sourceCloseErr != nil {
			return 0, fmt.Errorf("%w (CSV入力ファイルのcloseにも失敗しました: %v)", err, sourceCloseErr)
		}
		return 0, err
	}
	if sourceCloseErr != nil {
		if cleanupErr := cleanupSnapshot(); cleanupErr != nil {
			return 0, fmt.Errorf("CSV入力ファイルのcloseに失敗しました: %v (snapshot cleanupにも失敗しました: %v)", sourceCloseErr, cleanupErr)
		}
		return 0, fmt.Errorf("CSV入力ファイルのcloseに失敗しました: %w", sourceCloseErr)
	}
	defer func() {
		if cleanupErr := cleanupSnapshot(); cleanupErr != nil {
			if retErr == nil {
				count = 0
				retErr = fmt.Errorf("CSV一時ファイルのcleanupに失敗しました: %w", cleanupErr)
			} else {
				retErr = fmt.Errorf("%w (CSV一時ファイルのcleanupにも失敗しました: %v)", retErr, cleanupErr)
			}
		}
	}()
	a.mu.Lock()
	if generation != a.generation || a.coordinator == nil || !a.coordinator.Status().Unlocked {
		a.mu.Unlock()
		return 0, ErrDesktopVaultChanged
	}
	lease, err := a.coordinator.Service()
	a.mu.Unlock()
	if err != nil {
		return 0, err
	}
	defer lease.Release()
	service, err := lease.Core()
	if err != nil {
		return 0, err
	}
	return service.ImportCSVReaderContext(ctx, snapshot.File, mode)
}

type desktopCSVContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r desktopCSVContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func snapshotDesktopCSV(ctx context.Context, path string, source *os.File) (*fileprivacy.PrivateTempFile, func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return nil, nil, errors.New("CSV入力ファイルがありません")
	}
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf("CSVファイルパスが不正です: %w", err)
	}
	pathInfo, err := os.Lstat(clean)
	if err != nil {
		return nil, nil, fmt.Errorf("CSVファイルを検査できません: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, errors.New("CSVパスはsymlinkやデバイスではない通常ファイルで指定してください")
	}
	initial, err := source.Stat()
	if err != nil || !initial.Mode().IsRegular() || !os.SameFile(pathInfo, initial) {
		return nil, nil, errors.New("CSVファイルが選択後に置き換えられました")
	}
	if initial.Size() > core.MaxCSVImportBytes {
		return nil, nil, fmt.Errorf("CSV入力が上限%d bytesを超えました", core.MaxCSVImportBytes)
	}
	// Reserve the complete bounded-copy extent, rather than only the initial
	// stat size. The source may grow while it is being copied; the +1 probe byte
	// is written to the private snapshot before the copy is rejected. Charging
	// the full extent keeps that transient file inside the process-wide budget.
	reservationBytes := core.MaxCSVImportBytes + 1
	releaseBudget, ok := core.TryAcquireCSVTempBudget(reservationBytes)
	if !ok {
		return nil, nil, errors.New("CSV一時領域が混雑しています")
	}
	temp, err := fileprivacy.CreatePrivateTempFile("omni-money-csv-snapshot-")
	if err != nil {
		releaseBudget()
		return nil, nil, fmt.Errorf("CSV一時ファイル作成エラー: %w", err)
	}
	cleanup := func() error {
		cleanupErr := temp.Cleanup()
		releaseBudget()
		return cleanupErr
	}
	fail := func(operationErr error) (*fileprivacy.PrivateTempFile, func() error, error) {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, nil, fmt.Errorf("%w (CSV一時ファイルcleanupにも失敗しました: %v)", operationErr, cleanupErr)
		}
		return nil, nil, operationErr
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	firstDigest := sha256.New()
	limited := io.LimitReader(desktopCSVContextReader{ctx: ctx, r: source}, core.MaxCSVImportBytes+1)
	copied, err := io.Copy(io.MultiWriter(temp.File, firstDigest), limited)
	if err != nil || copied != initial.Size() {
		if err == nil {
			err = errors.New("CSVファイルがコピー中に変更されました")
		}
		return fail(fmt.Errorf("CSV一時ファイルへのコピーに失敗しました: %w", err))
	}
	if err := temp.File.Sync(); err != nil {
		return fail(err)
	}
	if _, err := temp.File.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	secondDigest := sha256.New()
	verified, err := io.Copy(secondDigest, io.LimitReader(desktopCSVContextReader{ctx: ctx, r: source}, core.MaxCSVImportBytes+1))
	if err != nil || verified != initial.Size() {
		if err == nil {
			err = errors.New("CSVファイルが検証中に変更されました")
		}
		return fail(err)
	}
	if !bytes.Equal(firstDigest.Sum(nil), secondDigest.Sum(nil)) {
		return fail(errors.New("CSVファイルの内容が検証中に変更されました"))
	}
	after, err := source.Stat()
	currentPath, pathErr := os.Lstat(clean)
	if err != nil || pathErr != nil || !os.SameFile(initial, after) || after.Size() != initial.Size() ||
		currentPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, currentPath) {
		return fail(errors.New("CSVファイルが選択後に置き換えられました"))
	}
	if info, err := temp.File.Stat(); err != nil || !info.Mode().IsRegular() || !fileprivacy.IsPrivate(temp.File, info) || info.Size() != initial.Size() {
		return fail(errors.New("CSV一時ファイルの検証に失敗しました"))
	}
	return temp, cleanup, nil
}

// openDesktopCSVFile binds the import to a regular file handle. The native
// picker returns a path, but opening that path directly would permit a later
// symlink/reparse-point swap (and would trigger path-taint findings). A pinned
// directory root plus an Lstat/handle identity comparison rejects symlinks,
// FIFOs/devices, and replacement races before any CSV bytes are consumed.
func openDesktopCSVFile(path string) (*os.File, error) {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("CSVファイルパスが不正です: %w", err)
	}
	directory, name := filepath.Split(clean)
	if directory == "" || name == "" || filepath.Base(name) != name {
		return nil, errors.New("CSVファイル名が不正です")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("CSVファイルの親ディレクトリを開けません: %w", err)
	}
	defer root.Close()
	entry, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("CSVファイルを検査できません: %w", err)
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return nil, errors.New("CSVパスはsymlinkやデバイスではない通常ファイルで指定してください")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("CSVファイルを開けません: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || !os.SameFile(entry, info) {
		_ = file.Close()
		if statErr != nil {
			return nil, fmt.Errorf("CSVファイルのidentity検証に失敗しました: %w", statErr)
		}
		return nil, errors.New("CSVファイルが選択後に置き換えられました")
	}
	return file, nil
}

// --- Snapshots ---

func (a *App) CreateSnapshot() (string, error) {
	return a.coordinator.CreateSnapshot()
}

func (a *App) ListSnapshots() ([]string, error) {
	return a.coordinator.ListSnapshots()
}

func (a *App) RestoreSnapshot(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.coordinator.RestoreSnapshot(name); err != nil {
		return err
	}
	a.generation++
	return nil
}

// --- Transaction images ---

func (a *App) AddTransactionImage(transactionID int64, img models.TransactionImageRequest) (*models.TransactionImageResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.AddTransactionImage(transactionID, img)
}

func (a *App) GetTransactionImages(transactionID int64) ([]models.TransactionImageResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTransactionImages(transactionID)
}

func (a *App) GetTransactionImagesPage(transactionID int64, cursor string, limit int) (*models.TransactionImagePage, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTransactionImagesPage(transactionID, cursor, limit)
}

func (a *App) DeleteTransactionImage(imageID int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.DeleteTransactionImage(imageID)
}

// --- Tags ---

func (a *App) CreateTag(name string, parentID *int64) (*models.Tag, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.CreateTag(name, parentID)
}

func (a *App) CreateTagByPath(path string) (*models.Tag, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.CreateTagByPath(path)
}

func (a *App) GetTags() ([]models.Tag, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTags()
}

func (a *App) GetTagDeleteImpact(id int64) (*models.TagDeleteImpact, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTagDeleteImpact(id)
}

func (a *App) UpdateTag(id int64, name string) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.UpdateTag(id, name)
}

func (a *App) DeleteTag(id int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.DeleteTag(id)
}

func (a *App) GetTransactionTags(transactionID int64) ([]models.Tag, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTransactionTags(transactionID)
}

func (a *App) AddTransactionTags(transactionID int64, tagIDs []int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.AddTransactionTags(transactionID, tagIDs)
}

func (a *App) RemoveTransactionTag(transactionID, tagID int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.RemoveTransactionTag(transactionID, tagID)
}

func (a *App) GetTagSummary(txType, startDate, endDate string) ([]models.TagSummary, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTagSummary(txType, startDate, endDate)
}

// --- Transaction links ---

func (a *App) GetTransactionLinks(transactionID int64) ([]models.LinkedTransactionResponse, error) {
	service, release, err := a.borrowService()
	if err != nil {
		return nil, err
	}
	defer release()
	return service.GetTransactionLinks(transactionID)
}

func (a *App) AddTransactionLink(parentID, childID int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.AddTransactionLink(parentID, childID)
}

func (a *App) RemoveTransactionLink(transactionID, linkedID int64) error {
	service, release, err := a.borrowService()
	if err != nil {
		return err
	}
	defer release()
	return service.RemoveTransactionLink(transactionID, linkedID)
}
