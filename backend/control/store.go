package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"omni_money/backend/securedb"
)

var (
	ErrEncryptionRequired  = errors.New("control database requires SQLCipher encryption")
	ErrStoreClosed         = errors.New("control store is closed")
	ErrAlreadyBootstrapped = errors.New("the first administrator already exists")
	ErrNotFound            = errors.New("control record not found")
	ErrConflict            = errors.New("control record conflicts with existing state")
	ErrForbidden           = errors.New("an active administrator is required")
	ErrSelfDisable         = errors.New("an administrator cannot disable their own account")
	ErrLastActiveAdmin     = errors.New("operation would remove the last active administrator")
	ErrInvitationInactive  = errors.New("invitation is not pending")
	ErrInvitationExpired   = errors.New("invitation has expired")
	ErrResetTicketInactive = errors.New("password reset ticket is not pending")
	ErrResetTicketExpired  = errors.New("password reset ticket has expired")
)

type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	opener *securedb.Opener
	closed bool
}

// Open opens an encrypted control-plane database and takes ownership of opener.
// A plaintext opener is always rejected here; only package tests can use the
// unexported relaxed path. The opener is destroyed on every failure path and by
// Close after all database connections have been closed. Callers must still
// destroy the source RawKey they passed by value to securedb.NewEncryptedOpener.
func Open(ctx context.Context, opener *securedb.Opener, path string) (*Store, error) {
	return openStore(ctx, opener, path, true)
}

func openStore(ctx context.Context, opener *securedb.Opener, path string, requireEncryption bool) (_ *Store, err error) {
	if opener == nil {
		return nil, ErrEncryptionRequired
	}
	openerOwned := true
	defer func() {
		if openerOwned {
			opener.Destroy()
		}
	}()
	if requireEncryption && !opener.Encrypted() {
		return nil, ErrEncryptionRequired
	}
	path, created, err := reservePrivateDatabasePath(path)
	if err != nil {
		return nil, err
	}
	db, err := opener.Open(ctx, path, securedb.Writable)
	if err != nil {
		if created {
			_ = os.Remove(path)
		}
		return nil, fmt.Errorf("open control database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := initializeSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil { // #nosec G302 -- control DB must be owner-only.
		_ = db.Close()
		return nil, fmt.Errorf("secure control database permissions: %w", err)
	}
	store := &Store{db: db, opener: opener}
	openerOwned = false
	return store, nil
}

func reservePrivateDatabasePath(path string) (string, bool, error) {
	if path == "" {
		return "", false, errors.New("control database path is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false, fmt.Errorf("resolve control database path: %w", err)
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", false, errors.New("control database cannot be placed at the filesystem root")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", false, fmt.Errorf("create control database directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return "", false, fmt.Errorf("inspect control database directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", false, errors.New("control database parent must be a real directory")
	}
	if parentInfo.Mode().Perm()&0077 != 0 {
		return "", false, errors.New("control database directory must be accessible only by its owner")
	}

	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", false, errors.New("control database path must be a regular file")
		}
		if err := os.Chmod(absolute, 0600); err != nil { // #nosec G302 -- control DB must be owner-only.
			return "", false, fmt.Errorf("secure existing control database: %w", err)
		}
		return absolute, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("inspect control database path: %w", err)
	}

	file, err := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- validated configured path.
	if err != nil {
		return "", false, fmt.Errorf("reserve control database path: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(absolute)
		return "", false, fmt.Errorf("close reserved control database: %w", err)
	}
	return absolute, true, nil
}

func (s *Store) database() (*sql.DB, error) {
	if s == nil {
		return nil, ErrStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return nil, ErrStoreClosed
	}
	return s.db, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.opener != nil {
		s.opener.Destroy()
		s.opener = nil
	}
	return err
}

// withImmediate serializes security-sensitive decisions across processes and
// connections. It is used for first-admin bootstrap, invitation consumption,
// and active-admin invariants where a deferred SQLite transaction can race.
func (s *Store) withImmediate(ctx context.Context, operation func(*sql.Conn) error) (err error) {
	db, err := s.database()
	if err != nil {
		return err
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire control database connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate control transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err = operation(connection); err != nil {
		return err
	}
	if _, err = connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit control transaction: %w", err)
	}
	return nil
}
