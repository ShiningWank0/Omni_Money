//go:build server

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"omni_money/backend/aitransport"
	"omni_money/backend/api"
	"omni_money/backend/atrest"
	"omni_money/backend/config"
	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
	"omni_money/backend/serverauth"
	"omni_money/backend/vault"
)

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

type serverRuntime struct {
	server          *http.Server
	sessions        *middleware.SessionManager
	vaults          *vault.Manager
	control         *control.Store
	setup           *serverauth.SetupAuthorizer
	shutdownTimeout time.Duration
	tlsConfigured   bool
	shutdownOnce    sync.Once
	shutdownErr     error
}

type shutdownHooks struct {
	httpShutdown  func(context.Context) error
	httpClose     func() error
	sessionsClose func(context.Context) error
	vaultsClose   func(context.Context) error
	controlClose  func() error
	setupDestroy  func()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("サーバー停止: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	serverConfig, err := config.ServerConfigFromEnv()
	if err != nil {
		return fmt.Errorf("サーバー設定が無効です: %w", err)
	}
	sessionConfig, err := middleware.SessionConfigFromEnv()
	if err != nil {
		return fmt.Errorf("セッション設定が無効です: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := newServerRuntime(ctx, serverConfig, sessionConfig)
	if err != nil {
		return err
	}
	return runtime.serve(ctx)
}

func newServerRuntime(
	ctx context.Context,
	serverConfig config.ServerConfig,
	sessionConfig middleware.SessionConfig,
) (_ *serverRuntime, err error) {
	runtime := &serverRuntime{shutdownTimeout: serverConfig.ShutdownTimeout}
	completed := false
	defer func() {
		if !completed {
			_ = runtime.shutdown()
		}
	}()

	atRestStatus, err := atrest.RequireServerProtection(
		serverConfig.ControlDBPath,
		serverConfig.DataAtRestMode,
		serverConfig.DataAtRestAttestationFile,
		time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("control DBの保存時保護contractが無効です: %w", err)
	}
	if err := validateVaultPlacement(serverConfig.VaultRoot, atRestStatus, serverConfig); err != nil {
		return nil, err
	}
	controlKeyInside, err := resolvedPathWithin(atRestStatus.DataRoot, serverConfig.ControlDBEncryptionKeyFile)
	if err != nil {
		return nil, fmt.Errorf("control DB key placementを確認できません: %w", err)
	}
	if controlKeyInside {
		return nil, errors.New("CONTROL_DB_ENCRYPTION_KEY_FILE must be outside the attested financial data root")
	}
	log.Printf("保存時保護contract確認 (provider=%s key_id=%s)", atRestStatus.Provider, atRestStatus.KeyID)

	controlKey, err := securedb.ReadRawKeyFile(serverConfig.ControlDBEncryptionKeyFile)
	if err != nil {
		return nil, fmt.Errorf("control DB SQLCipher鍵ファイルが無効です: %w", err)
	}
	opener := securedb.NewEncryptedOpener(controlKey)
	controlKey.Destroy()
	controlStore, err := control.Open(ctx, opener, serverConfig.ControlDBPath)
	if err != nil {
		return nil, fmt.Errorf("control DBを開けません: %w", err)
	}
	runtime.control = controlStore

	bootstrapped, err := controlStore.IsBootstrapped(ctx)
	if err != nil {
		return nil, fmt.Errorf("initial admin状態を確認できません: %w", err)
	}
	if !bootstrapped && serverConfig.InitialAdminSetupTokenFile != "" {
		setupInside, resolveErr := resolvedPathWithin(atRestStatus.DataRoot, serverConfig.InitialAdminSetupTokenFile)
		if resolveErr != nil {
			return nil, fmt.Errorf("initial admin setup token placementを確認できません: %w", resolveErr)
		}
		if setupInside {
			return nil, errors.New("INITIAL_ADMIN_SETUP_TOKEN_FILE must be outside the attested financial data root")
		}
	}
	runtime.setup, err = initialSetupAuthorizer(bootstrapped, serverConfig.InitialAdminSetupTokenFile)
	if err != nil {
		return nil, err
	}

	vaultManager, err := vault.NewManager(serverConfig.VaultRoot)
	if err != nil {
		return nil, fmt.Errorf("vault managerを初期化できません: %w", err)
	}
	runtime.vaults = vaultManager
	runtime.sessions = middleware.NewSessionManagerWithConfig(sessionConfig)

	accountService, err := serverauth.NewService(serverauth.Dependencies{
		Store:            controlStore,
		OpenSession:      openVaultSession(vaultManager, runtime.sessions),
		Sessions:         runtime.sessions,
		Vaults:           vaultManager,
		Setup:            runtime.setup,
		MaxConcurrentKDF: serverConfig.AuthKDFConcurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("server authenticationを初期化できません: %w", err)
	}
	publicHandler, err := api.NewServerRouter(api.ServerDependencies{
		Accounts: accountService,
		Sessions: runtime.sessions,
		Control:  controlStore,
	})
	if err != nil {
		return nil, fmt.Errorf("production routerを初期化できません: %w", err)
	}

	if err := validateProductionListener(serverConfig); err != nil {
		return nil, fmt.Errorf("公開Webの待受設定が安全ではありません: %w", err)
	}
	publicTLSConfig, err := aitransport.BuildPublicServerTLSConfig(serverConfig.TLSCertFile, serverConfig.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("公開WebのTLS設定が無効です: %w", err)
	}
	runtime.server = &http.Server{
		Addr:              net.JoinHostPort(serverConfig.ListenHost, serverConfig.Port),
		Handler:           publicHandler,
		TLSConfig:         publicTLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	runtime.tlsConfigured = publicTLSConfig != nil
	completed = true
	return runtime, nil
}

func validateProductionListener(serverConfig config.ServerConfig) error {
	if serverConfig.TLSCertFile != "" {
		return nil
	}
	exposureHost := serverConfig.WebTransport.ExternalHost
	if exposureHost == "" {
		exposureHost = serverConfig.ListenHost
	}
	if config.IsLoopbackHost(exposureHost) {
		return nil
	}
	if serverConfig.WebTransport.AllowInsecureHTTP {
		return errors.New("ALLOW_INSECURE_HTTP cannot permit non-loopback plaintext in the production multi-user server")
	}
	// ServerConfigFromEnv has already forbidden ALLOW_INSECURE_HTTP here. Keep
	// the proxy validator as defense in depth for programmatically built configs.
	return middleware.ValidatePublicListenerSecurity(serverConfig.ListenHost, false)
}

func initialSetupAuthorizer(bootstrapped bool, path string) (*serverauth.SetupAuthorizer, error) {
	if bootstrapped {
		// Do not read or retain a bootstrap secret after the atomic zero-user
		// transition has completed, even if an operator left the path configured.
		return nil, nil
	}
	if path == "" {
		return nil, errors.New("INITIAL_ADMIN_SETUP_TOKEN_FILE is required until the initial administrator has been created")
	}
	setup, err := serverauth.LoadSetupAuthorizer(path)
	if err != nil {
		return nil, fmt.Errorf("initial admin setup tokenが無効です: %w", err)
	}
	return setup, nil
}

func validateVaultPlacement(root string, status atrest.Status, serverConfig config.ServerConfig) error {
	if !pathWithin(status.DataRoot, root) || filepath.Clean(root) == filepath.Clean(status.DataRoot) {
		return errors.New("VAULT_ROOT must be a dedicated directory below the attested financial data root")
	}
	// Reuse the at-rest path validator with a synthetic leaf. This checks every
	// existing component for symlinks and unsafe permissions while still allowing
	// a fresh deployment whose vault directory has not been created yet.
	probe := filepath.Join(root, ".omni-money-vault-placement.db")
	vaultStatus, err := atrest.RequireServerProtection(
		probe,
		serverConfig.DataAtRestMode,
		serverConfig.DataAtRestAttestationFile,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("vault rootの保存時保護contractが無効です: %w", err)
	}
	if filepath.Clean(vaultStatus.DataRoot) != filepath.Clean(status.DataRoot) {
		return errors.New("control DB and vault root must use the same attested financial data root")
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func resolvedPathWithin(root, candidate string) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return false, fmt.Errorf("resolve protected data root: %w", err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return false, fmt.Errorf("resolve secret path: %w", err)
	}
	return pathWithin(resolvedRoot, resolvedCandidate), nil
}

func openVaultSession(manager *vault.Manager, sessions *middleware.SessionManager) serverauth.OpenSessionFunc {
	return func(user control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
		if manager == nil || sessions == nil || key == nil {
			if key != nil {
				key.Destroy()
			}
			return nil, serverauth.ErrServiceUnavailable
		}
		defer key.Destroy()
		root, err := manager.Acquire(user.ID, vaultID, *key)
		if err != nil {
			return nil, err
		}
		session, err := sessions.CreateVaultSession(user, root)
		if err != nil {
			root.Release()
			return nil, err
		}
		return session, nil
	}
}

func (runtime *serverRuntime) serve(ctx context.Context) error {
	if runtime == nil || runtime.server == nil {
		return errors.New("server runtime is unavailable")
	}
	serveErrors := make(chan error, 1)
	go func() {
		if runtime.tlsConfigured {
			log.Printf("Omni Money v%s 公開Web起動 (TLS): %s", version, runtime.server.Addr)
			serveErrors <- runtime.server.ListenAndServeTLS("", "")
			return
		}
		log.Printf("Omni Money v%s 公開Web起動 (HTTP): %s", version, runtime.server.Addr)
		serveErrors <- runtime.server.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	shutdownErr := runtime.shutdown()
	return errors.Join(serveErr, shutdownErr)
}

func (runtime *serverRuntime) shutdown() error {
	if runtime == nil {
		return nil
	}
	runtime.shutdownOnce.Do(func() {
		hooks := shutdownHooks{}
		if runtime.server != nil {
			hooks.httpShutdown = runtime.server.Shutdown
			hooks.httpClose = runtime.server.Close
		}
		if runtime.sessions != nil {
			hooks.sessionsClose = runtime.sessions.CloseContext
		}
		if runtime.vaults != nil {
			hooks.vaultsClose = runtime.vaults.Close
		}
		if runtime.control != nil {
			hooks.controlClose = runtime.control.Close
		}
		if runtime.setup != nil {
			hooks.setupDestroy = runtime.setup.Destroy
		}
		runtime.shutdownErr = executeShutdown(runtime.shutdownTimeout, hooks)
	})
	return runtime.shutdownErr
}

// executeShutdown keeps the ownership order explicit and unit-testable:
// network drain, session root release, vault child drain/key destruction,
// control opener destruction, then setup-token digest destruction.
func executeShutdown(timeout time.Duration, hooks shutdownHooks) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var shutdownErrors []error
	if hooks.httpShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := hooks.httpShutdown(ctx)
		cancel()
		if err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown HTTP server: %w", err))
			if hooks.httpClose != nil {
				if closeErr := hooks.httpClose(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
					shutdownErrors = append(shutdownErrors, fmt.Errorf("force close HTTP server: %w", closeErr))
				}
			}
		}
	}
	if hooks.sessionsClose != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := hooks.sessionsClose(ctx)
		cancel()
		if err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("close sessions: %w", err))
		}
	}
	if hooks.vaultsClose != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := hooks.vaultsClose(ctx)
		cancel()
		if err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("close vault manager: %w", err))
		}
	}
	if hooks.controlClose != nil {
		if err := hooks.controlClose(); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("close control store: %w", err))
		}
	}
	if hooks.setupDestroy != nil {
		hooks.setupDestroy()
	}
	return errors.Join(shutdownErrors...)
}
