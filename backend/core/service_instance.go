package core

import (
	"database/sql"
	"errors"

	"omni_money/backend/database"
)

// ErrServiceUnavailable is returned when a Service has no live database
// instance. Server callers must treat this as a failed request; an explicit
// Service never falls back to the package-level Desktop database.
var ErrServiceUnavailable = errors.New("core service database instance is unavailable")

// Service binds all business operations to exactly one database instance.
// It is safe to create multiple Services for independent user vaults.
type Service struct {
	instance  *database.Instance
	db        *sql.DB
	legacy    bool
	snapshot  func()
	available func() bool
}

// NewService creates a business service for one explicit database instance.
// The instance remains owned by the caller and must outlive the Service.
func NewService(instance *database.Instance) (*Service, error) {
	return newService(instance, nil)
}

// NewGuardedService binds business operations to an instance and a lifecycle
// guard. It is intended for resource owners such as the server vault manager,
// where retaining a Service after its request lease ends must fail closed.
func NewGuardedService(instance *database.Instance, available func() bool) (*Service, error) {
	if available == nil || !available() {
		return nil, ErrServiceUnavailable
	}
	return newService(instance, available)
}

func newService(instance *database.Instance, available func() bool) (*Service, error) {
	if instance == nil {
		return nil, ErrServiceUnavailable
	}
	db := instance.DB()
	if db == nil {
		return nil, ErrServiceUnavailable
	}
	return &Service{
		instance:  instance,
		db:        db,
		snapshot:  instance.StartAutoSnapshot,
		available: available,
	}, nil
}

// database returns the bound handle only while its explicit Instance is still
// live. Checking Instance.DB on every operation prevents a closed request
// vault from silently using the Desktop/default database.
func (s *Service) database() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, ErrServiceUnavailable
	}
	if s.available != nil && !s.available() {
		return nil, ErrServiceUnavailable
	}
	if s.instance != nil && s.instance.DB() != s.db {
		return nil, ErrServiceUnavailable
	}
	if s.instance == nil && !s.legacy {
		return nil, ErrServiceUnavailable
	}
	return s.db, nil
}

func (s *Service) autoSnapshot() {
	if _, err := s.database(); err != nil || s.snapshot == nil {
		return
	}
	s.snapshot()
}
