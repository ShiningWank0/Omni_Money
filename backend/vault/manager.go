// Package vault owns the lifecycle of per-user encrypted ledger databases.
// It deliberately has no administration lookup API: callers must already have
// an authenticated user/vault binding and the unwrapped vault key.
package vault

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/securedb"
)

var (
	ErrClosed          = errors.New("vault manager is closed")
	ErrDraining        = errors.New("user vault is being closed")
	ErrBindingMismatch = errors.New("vault binding or key does not match the open vault")
	ErrInvalidIdentity = errors.New("invalid user or vault identifier")
	ErrLeaseReleased   = errors.New("vault lease is already released or cannot be borrowed")
)

type openInstanceFunc func(path string, key securedb.RawKey) (*database.Instance, error)

type Manager struct {
	root    string
	open    openInstanceFunc
	mu      sync.Mutex
	entries map[string]*entry
	byVault map[string]*entry
	closing bool
}

type entry struct {
	userID         string
	vaultID        string
	keyFingerprint [sha256.Size]byte
	instance       *database.Instance
	ready          chan struct{}
	openErr        error
	references     int
	rootReferences int
	draining       bool
	idle           chan struct{}
	closing        chan struct{}
	closeErr       error
}

// Lease keeps a user's vault open. A session or request owner must call
// Release when finished; repeated calls are safe. CloseUser and Close wait for
// all outstanding leases.
type Lease struct {
	manager *Manager
	entry   *entry
	state   *leaseState
	root    bool
}

// leaseState is pointer-owned so even an accidental value copy of the
// exported Lease shares exactly-once release and liveness state.
type leaseState struct {
	once     sync.Once
	mu       sync.RWMutex
	released bool
}

func newLease(manager *Manager, current *entry, root bool) *Lease {
	return &Lease{manager: manager, entry: current, state: &leaseState{}, root: root}
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
	absolute = filepath.Clean(absolute)
	if err := ensurePrivateDirectory(absolute, true); err != nil {
		return nil, fmt.Errorf("prepare vault root: %w", err)
	}
	return &Manager{
		root:    absolute,
		open:    open,
		entries: make(map[string]*entry),
		byVault: make(map[string]*entry),
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

	for {
		m.mu.Lock()
		if m.closing {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		if current := m.entries[userID]; current != nil {
			if current.draining {
				m.mu.Unlock()
				return nil, ErrDraining
			}
			if current.vaultID != vaultID || subtle.ConstantTimeCompare(current.keyFingerprint[:], fingerprint[:]) != 1 {
				m.mu.Unlock()
				return nil, ErrBindingMismatch
			}
			if current.ready != nil {
				ready := current.ready
				m.mu.Unlock()
				<-ready
				m.mu.Lock()
				if current.openErr != nil {
					err := current.openErr
					m.mu.Unlock()
					return nil, err
				}
				if m.closing {
					m.mu.Unlock()
					return nil, ErrClosed
				}
				if current.draining || m.entries[userID] != current || current.instance == nil {
					m.mu.Unlock()
					return nil, ErrDraining
				}
				// Take the waiter's reference before releasing Manager.mu. This is
				// the linearization point with CloseUser: the closer either observes
				// this reference or marks the entry draining first.
				m.addReferenceLocked(current, true)
				m.mu.Unlock()
				return newLease(m, current, true), nil
			}
			if current.instance == nil {
				m.mu.Unlock()
				return nil, ErrDraining
			}
			m.addReferenceLocked(current, true)
			m.mu.Unlock()
			return newLease(m, current, true), nil
		}
		if current := m.byVault[vaultID]; current != nil && current.userID != userID {
			m.mu.Unlock()
			return nil, ErrBindingMismatch
		}

		current := &entry{
			userID:         userID,
			vaultID:        vaultID,
			keyFingerprint: fingerprint,
			ready:          make(chan struct{}),
			idle:           closedSignal(),
		}
		m.entries[userID] = current
		m.byVault[vaultID] = current
		m.mu.Unlock()

		path, err := prepareVaultPath(m.root, vaultID)
		if err != nil {
			return nil, m.finishOpening(current, nil, fmt.Errorf("prepare user vault: %w", err))
		}
		instance, err := m.open(path, key)
		if err != nil {
			return nil, m.finishOpening(current, nil, fmt.Errorf("open user vault: %w", err))
		}
		if instance == nil {
			return nil, m.finishOpening(current, nil, errors.New("open user vault: opener returned a nil instance"))
		}
		if err := validateVaultPath(m.root, vaultID, path); err != nil {
			_ = instance.Close()
			return nil, m.finishOpening(current, nil, fmt.Errorf("validate opened user vault: %w", err))
		}
		if err := m.finishOpening(current, instance, nil); err != nil {
			return nil, err
		}
		return newLease(m, current, true), nil
	}
}

// finishOpening publishes an open result and wakes every same-user waiter.
// The expensive opener runs outside Manager.mu; the reservation itself keeps
// conflicting bindings from opening the same user or vault concurrently.
func (m *Manager) finishOpening(current *entry, instance *database.Instance, openErr error) error {
	m.mu.Lock()
	current.instance = instance
	current.openErr = openErr
	ready := current.ready
	current.ready = nil
	terminal := current.draining || m.closing
	if openErr != nil && !terminal {
		if m.entries[current.userID] == current {
			delete(m.entries, current.userID)
		}
		if m.byVault[current.vaultID] == current {
			delete(m.byVault, current.vaultID)
		}
	}
	if openErr != nil {
		clear(current.keyFingerprint[:])
	}
	if ready != nil {
		close(ready)
	}
	if openErr != nil {
		m.mu.Unlock()
		if terminal {
			_ = m.closeEntry(context.Background(), current)
		}
		return openErr
	}
	if m.closing {
		m.mu.Unlock()
		return errors.Join(ErrClosed, m.closeEntry(context.Background(), current))
	}
	if current.draining {
		m.mu.Unlock()
		return errors.Join(ErrDraining, m.closeEntry(context.Background(), current))
	}
	m.addReferenceLocked(current, true)
	m.mu.Unlock()
	return nil
}

func (m *Manager) addReferenceLocked(current *entry, root bool) {
	if current.references == 0 {
		current.idle = make(chan struct{})
	}
	current.references++
	if root {
		current.rootReferences++
	}
}

// Service returns a request-bound business service. The underlying database
// instance is deliberately not exposed: callers cannot close or restore it
// behind Manager's reference tracking, and every operation fails closed once
// this lease has been released.
func (l *Lease) Service() (*core.Service, error) {
	if l == nil {
		return nil, ErrLeaseReleased
	}
	if l.state == nil {
		return nil, ErrLeaseReleased
	}
	l.state.mu.RLock()
	if l.state.released || l.manager == nil || l.entry == nil || l.entry.instance == nil {
		l.state.mu.RUnlock()
		return nil, ErrLeaseReleased
	}
	instance := l.entry.instance
	l.state.mu.RUnlock()
	return core.NewGuardedService(instance, l.isLive)
}

func (l *Lease) isLive() bool {
	if l == nil || l.state == nil {
		return false
	}
	l.state.mu.RLock()
	defer l.state.mu.RUnlock()
	return !l.state.released && l.manager != nil && l.entry != nil && l.entry.instance != nil
}

// Borrow creates a request-scoped child lease from a live session root lease.
// Releasing the root prevents future borrows but does not revoke children that
// were already issued; CloseUser and Close wait for those children to finish.
func (l *Lease) Borrow() (*Lease, error) {
	if l == nil || l.state == nil {
		return nil, ErrLeaseReleased
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.state.released || !l.root || l.manager == nil || l.entry == nil {
		return nil, ErrLeaseReleased
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrClosed
	}
	current := m.entries[l.entry.userID]
	if current != l.entry || current.draining || current.ready != nil || current.instance == nil {
		return nil, ErrDraining
	}
	m.addReferenceLocked(current, false)
	return newLease(m, current, false), nil
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
	if l == nil || l.state == nil {
		return
	}
	l.state.once.Do(func() {
		l.state.mu.Lock()
		l.state.released = true
		m := l.manager
		root := l.root
		if m == nil || l.entry == nil {
			l.state.mu.Unlock()
			return
		}
		m.mu.Lock()
		l.state.mu.Unlock()
		current := m.entries[l.entry.userID]
		autoClose := false
		if current == l.entry && current.references > 0 {
			current.references--
			if root && current.rootReferences > 0 {
				current.rootReferences--
				if current.rootReferences == 0 && !current.draining {
					// A root lease represents an unlocked session. Once the last
					// root disappears, reject new acquisitions immediately and
					// close after any already-borrowed request children finish.
					current.draining = true
					autoClose = true
				}
			}
			if current.references == 0 {
				close(current.idle)
			}
		}
		m.mu.Unlock()
		if autoClose {
			go func() { _ = m.closeEntry(context.Background(), current) }()
		}
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
	m.mu.Unlock()
	return m.closeEntry(ctx, current)
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

	var closeErrors []error
	for _, current := range entries {
		if err := m.closeEntry(ctx, current); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func (m *Manager) closeEntry(ctx context.Context, current *entry) error {
	for {
		m.mu.Lock()
		if m.entries[current.userID] != current {
			closeErr := current.closeErr
			m.mu.Unlock()
			return closeErr
		}
		ready := current.ready
		idle := current.idle
		closing := current.closing
		m.mu.Unlock()
		if ready != nil {
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if closing != nil {
			select {
			case <-closing:
				m.mu.Lock()
				closeErr := current.closeErr
				m.mu.Unlock()
				return closeErr
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		m.mu.Lock()
		if m.entries[current.userID] != current || current.ready != nil || current.references != 0 || current.closing != nil {
			m.mu.Unlock()
			continue
		}
		current.closing = make(chan struct{})
		instance := current.instance
		current.instance = nil
		m.mu.Unlock()

		var closeErr error
		if instance != nil {
			closeErr = instance.Close()
		}
		m.mu.Lock()
		current.closeErr = closeErr
		if m.entries[current.userID] == current {
			delete(m.entries, current.userID)
		}
		if m.byVault[current.vaultID] == current {
			delete(m.byVault, current.vaultID)
		}
		clear(current.keyFingerprint[:])
		close(current.closing)
		m.mu.Unlock()
		return closeErr
	}
}

func closedSignal() chan struct{} {
	signal := make(chan struct{})
	close(signal)
	return signal
}

func prepareVaultPath(root, vaultID string) (string, error) {
	if err := ensurePrivateDirectory(root, false); err != nil {
		return "", err
	}
	vaultDir := filepath.Join(root, vaultID)
	if err := ensurePrivateDirectory(vaultDir, true); err != nil {
		return "", err
	}
	ledgerPath := filepath.Join(vaultDir, "ledger.db")
	if err := validateContainedPath(root, ledgerPath); err != nil {
		return "", err
	}
	if info, err := os.Lstat(ledgerPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("vault ledger path must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect vault ledger: %w", err)
	}
	return ledgerPath, nil
}

func validateVaultPath(root, vaultID, ledgerPath string) error {
	if err := ensurePrivateDirectory(root, false); err != nil {
		return err
	}
	vaultDir := filepath.Join(root, vaultID)
	if err := ensurePrivateDirectory(vaultDir, false); err != nil {
		return err
	}
	want := filepath.Join(vaultDir, "ledger.db")
	if filepath.Clean(ledgerPath) != filepath.Clean(want) {
		return errors.New("vault opener returned outside the reserved ledger path")
	}
	if err := validateContainedPath(root, ledgerPath); err != nil {
		return err
	}
	info, err := os.Lstat(ledgerPath)
	if err != nil {
		return fmt.Errorf("inspect opened vault ledger: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("opened vault ledger path must be a regular file")
	}
	return nil
}

func ensurePrivateDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("create private directory: %w", err)
		}
		if err := os.Chmod(path, 0700); err != nil { // #nosec G302 -- vault directories must be owner-only.
			return fmt.Errorf("secure private directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("vault directory must be a real directory")
	}
	if info.Mode().Perm() != 0700 {
		return fmt.Errorf("vault directory permissions must be 0700, got %04o", info.Mode().Perm())
	}
	return nil
}

func validateContainedPath(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return fmt.Errorf("resolve vault ledger path: %w", err)
	}
	if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("vault ledger path escapes the configured root")
	}
	return nil
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
