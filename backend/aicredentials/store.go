package aicredentials

import (
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type snapshot struct {
	credentials []Credential
}

// Store holds an immutable credential snapshot. Reload validates a complete new
// document before atomically publishing it, so readers never observe partial or
// invalid configuration.
type Store struct {
	path     string
	reloadMu sync.Mutex
	current  atomic.Pointer[snapshot]
}

// NewStore constructs a Store only after the initial credential file has been
// securely loaded and fully validated.
func NewStore(path string) (*Store, error) {
	store := &Store{path: path}
	if err := store.Reload(); err != nil {
		return nil, err
	}
	return store, nil
}

// Reload validates a complete replacement file and publishes it atomically.
// When loading fails, the previously active snapshot is preserved.
func (s *Store) Reload() error {
	if s == nil {
		return errors.New("credential store is nil")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	document, err := LoadFile(s.path)
	if err != nil {
		return err
	}
	credentials := make([]Credential, len(document.Credentials))
	for i := range document.Credentials {
		credentials[i] = document.Credentials[i].clone()
	}
	s.current.Store(&snapshot{credentials: credentials})
	return nil
}

// Authenticate hashes rawToken and compares it against every configured hash
// using constant-time comparison. Unknown, inactive, and expired credentials
// intentionally return the same error.
func (s *Store) Authenticate(rawToken string, now time.Time) (*Credential, error) {
	if s == nil || rawToken == "" {
		return nil, ErrAuthenticationFailed
	}
	active := s.current.Load()
	if active == nil {
		return nil, ErrAuthenticationFailed
	}

	tokenHash := sha256.Sum256([]byte(rawToken))
	matched := -1
	for i := range active.credentials {
		match := tokenHashMatches(tokenHash, &active.credentials[i])
		if match == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return nil, ErrAuthenticationFailed
	}
	credential := &active.credentials[matched]
	if now.Before(credential.NotBefore) || !now.Before(credential.ExpiresAt) {
		return nil, ErrAuthenticationFailed
	}
	cloned := credential.clone()
	return &cloned, nil
}

// List returns defensive copies of the currently active credentials.
func (s *Store) List() []Credential {
	if s == nil {
		return nil
	}
	active := s.current.Load()
	if active == nil {
		return nil
	}
	credentials := make([]Credential, len(active.credentials))
	for i := range active.credentials {
		credentials[i] = active.credentials[i].clone()
	}
	return credentials
}
