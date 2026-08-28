// Package vault owns the lifecycle of per-user encrypted ledger databases.
// It deliberately has no administration lookup API: callers must already have
// an authenticated user/vault binding and the unwrapped vault key.
package vault

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"omni_money/backend/database"
	"omni_money/backend/securedb"
)

var (
	ErrClosed          = errors.New("vault manager is closed")
	ErrDraining        = errors.New("user vault is being closed")
	ErrBindingMismatch = errors.New("vault binding or key does not match the open vault")
	ErrInvalidIdentity = errors.New("invalid user or vault identifier")
)

type openInstanceFunc func(path string, key securedb.RawKey) (*database.Instance, error)

type Manager struct {
	root    string
	open    openInstanceFunc
	mu      sync.Mutex
	entries map[string]*entry
	closing bool
}

type entry struct {
	userID         string
	vaultID        string
	keyFingerprint [sha256.Size]byte
	instance       *database.Instance
	references     int
	draining       bool
	idle           chan struct{}
}

// Lease keeps a user's vault open. A session or request owner must call
// Release exactly once. CloseUser and Close wait for all outstanding leases.
type Lease struct {
	manager  *Manager
	entry    *entry
	once     sync.Once
	mu       sync.RWMutex
	released bool
}

// NewManager creates a manager rooted at a server-private vault directory.
func NewManager(root string) (*Manager, error) {
	return newManager(root, database.OpenEncryptedInstance)
}

func newManager(root string, open openInstanceFunc) (*Manager, error) {
	if strings.TrimSpace(root) == "" || open == nil {
		return nil, fmt.Errorf("vault root and opener are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}
	return &Manager{
		root:    filepath.Clean(absolute),
		open:    open,
		entries: make(map[string]*entry),
	}, nil
}

// Acquire returns the already-open vault for the same binding, or opens it
// with the supplied DEK. A live entry is never returned for a different vault
// ID or key, even if the user ID is the same.
func (m *Manager) Acquire(userID, vaultID string, key securedb.RawKey) (*Lease, error) {
	if m == nil {
		key.Destroy()
		return nil, ErrClosed
	}
	if err := validateUserID(userID); err != nil {
		key.Destroy()
		return nil, err
	}
	if err := validateVaultID(vaultID); err != nil {
		key.Destroy()
		return nil, err
	}
	fingerprint := sha256.Sum256(key[:])
	defer key.Destroy()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrClosed
	}
	if current := m.entries[userID]; current != nil {
		if current.draining {
			return nil, ErrDraining
		}
		if current.vaultID != vaultID || subtle.ConstantTimeCompare(current.keyFingerprint[:], fingerprint[:]) != 1 {
			return nil, ErrBindingMismatch
		}
		if current.references == 0 {
			current.idle = make(chan struct{})
		}
		current.references++
		return &Lease{manager: m, entry: current}, nil
	}
	for boundUserID, current := range m.entries {
		if current.vaultID == vaultID && boundUserID != userID {
			return nil, ErrBindingMismatch
		}
	}

	path := filepath.Join(m.root, vaultID, "ledger.db")
	instance, err := m.open(path, key)
	if err != nil {
		return nil, fmt.Errorf("open user vault: %w", err)
	}
	current := &entry{
		userID:         userID,
		vaultID:        vaultID,
		keyFingerprint: fingerprint,
		instance:       instance,
		references:     1,
		idle:           make(chan struct{}),
	}
	m.entries[userID] = current
	return &Lease{manager: m, entry: current}, nil
}

// DB returns the leased connection pool, or nil after Release.
func (l *Lease) DB() *sql.DB {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released || l.entry == nil || l.entry.instance == nil {
		return nil
	}
	return l.entry.instance.DB()
}

// Instance returns the database instance for snapshot operations. It must not
// be retained after releasing the lease.
func (l *Lease) Instance() *database.Instance {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.released || l.entry == nil {
		return nil
	}
	return l.entry.instance
}

func (l *Lease) UserID() string {
	if l == nil || l.entry == nil {
		return ""
	}
	return l.entry.userID
}

func (l *Lease) VaultID() string {
	if l == nil || l.entry == nil {
		return ""
	}
	return l.entry.vaultID
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.mu.Lock()
		l.released = true
		l.mu.Unlock()
		m := l.manager
		if m == nil || l.entry == nil {
			return
		}
		m.mu.Lock()
		current := m.entries[l.entry.userID]
		if current == l.entry && current.references > 0 {
			current.references--
			if current.references == 0 {
				close(current.idle)
			}
		}
		m.mu.Unlock()
	})
}

// CloseUser prevents new leases for a user, waits for in-flight leases, then
// closes and zeroizes the SQLCipher opener owned by the database instance.
func (m *Manager) CloseUser(ctx context.Context, userID string) error {
	if m == nil {
		return nil
	}
	if err := validateUserID(userID); err != nil {
		return err
	}
	m.mu.Lock()
	current := m.entries[userID]
	if current == nil {
		m.mu.Unlock()
		return nil
	}
	current.draining = true
	idle := current.idle
	m.mu.Unlock()

	select {
	case <-idle:
	case <-ctx.Done():
		return ctx.Err()
	}

	m.mu.Lock()
	if m.entries[userID] != current || current.references != 0 {
		m.mu.Unlock()
		return ErrDraining
	}
	delete(m.entries, userID)
	m.mu.Unlock()
	return current.instance.Close()
}

// Close drains every vault and permanently prevents new acquisitions.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closing = true
	entries := make([]*entry, 0, len(m.entries))
	for _, current := range m.entries {
		current.draining = true
		entries = append(entries, current)
	}
	m.mu.Unlock()

	for _, current := range entries {
		select {
		case <-current.idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	for _, current := range entries {
		if m.entries[current.userID] == current {
			delete(m.entries, current.userID)
		}
	}
	m.mu.Unlock()
	var closeErrors []error
	for _, current := range entries {
		if err := current.instance.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func validateUserID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 {
		return ErrInvalidIdentity
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrInvalidIdentity
		}
	}
	return nil
}

func validateVaultID(value string) error {
	if len(value) < 16 || len(value) > 128 {
		return ErrInvalidIdentity
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ErrInvalidIdentity
	}
	return nil
}
