package securedb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const RequiredSQLCipherVersion = "4.18.0"

var (
	ErrDestroyed            = errors.New("secure database opener is destroyed")
	ErrCipherUnavailable    = errors.New("required SQLCipher support is unavailable")
	ErrCipherVersion        = errors.New("unexpected SQLCipher version")
	ErrCipherProvider       = errors.New("unexpected SQLCipher crypto provider")
	ErrCipherMemorySecurity = errors.New("SQLCipher memory security is unavailable")
	ErrCipherInactive       = errors.New("SQLCipher encryption is not active")
	ErrConnectionConfig     = errors.New("secure database connection configuration failed")
)

type Purpose uint8

const (
	Writable Purpose = iota
	Snapshot
)

type Opener struct {
	mu        sync.RWMutex
	key       RawKey
	encrypted bool
	destroyed bool
}

func NewPlainOpener() *Opener { return &Opener{} }

func NewEncryptedOpener(key RawKey) *Opener {
	return &Opener{key: key, encrypted: true}
}

func (o *Opener) Encrypted() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.encrypted && !o.destroyed
}

func (o *Opener) String() string {
	return fmt.Sprintf("securedb.Opener{encrypted:%t}", o.Encrypted())
}

func (o *Opener) GoString() string { return o.String() }

func (o *Opener) Destroy() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.key.Destroy()
	o.destroyed = true
}

func (o *Opener) copyKey() (RawKey, error) {
	if o == nil {
		return RawKey{}, ErrDestroyed
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.destroyed || !o.encrypted {
		return RawKey{}, ErrDestroyed
	}
	return o.key, nil
}

func (o *Opener) Open(ctx context.Context, path string, purpose Purpose) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if o == nil {
		return nil, ErrDestroyed
	}
	if !o.Encrypted() {
		return openPlain(path, purpose)
	}

	query := url.Values{}
	if purpose == Snapshot {
		query.Set("mode", "rw")
	}
	dsnURL := &url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	db := sql.OpenDB(&encryptedConnector{opener: o, dsn: dsnURL.String(), purpose: purpose})
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openPlain(path string, purpose Purpose) (*sql.DB, error) {
	query := url.Values{}
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "ON")
	query.Set("_synchronous", "FULL")
	if purpose == Snapshot {
		query.Set("mode", "rw")
		query.Set("_journal_mode", "DELETE")
	} else {
		query.Set("_journal_mode", "WAL")
	}
	dsnURL := &url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	return sql.Open("sqlite3", dsnURL.String())
}

type encryptedConnector struct {
	opener  *Opener
	dsn     string
	purpose Purpose
}

func (c *encryptedConnector) Connect(context.Context) (driver.Conn, error) {
	key, err := c.opener.copyKey()
	if err != nil {
		return nil, err
	}
	defer key.Destroy()

	sqliteDriver := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		if err := applyRawKey(conn, key); err != nil {
			return fmt.Errorf("apply SQLCipher key: %w", ErrConnectionConfig)
		}
		if _, err := conn.Exec("PRAGMA cipher_memory_security = ON", nil); err != nil {
			return ErrCipherMemorySecurity
		}
		version, err := driverQueryString(conn, "PRAGMA cipher_version")
		if err != nil || strings.TrimSpace(version) == "" {
			return ErrCipherUnavailable
		}
		if strings.TrimSpace(version) != RequiredSQLCipherVersion {
			return fmt.Errorf("%w: required %s", ErrCipherVersion, RequiredSQLCipherVersion)
		}
		provider, err := driverQueryString(conn, "PRAGMA cipher_provider")
		if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider)), "openssl") {
			return ErrCipherProvider
		}
		memorySecurity, err := driverQueryInt64(conn, "PRAGMA cipher_memory_security")
		if err != nil || memorySecurity != 1 {
			return ErrCipherMemorySecurity
		}
		statements := []string{
			"PRAGMA busy_timeout = 5000",
			"PRAGMA foreign_keys = ON",
			"PRAGMA temp_store = MEMORY",
		}
		if c.purpose == Snapshot {
			statements = append(statements, "PRAGMA journal_mode = DELETE")
		} else {
			statements = append(statements, "PRAGMA journal_mode = WAL")
		}
		statements = append(statements, "PRAGMA synchronous = FULL")
		for _, statement := range statements {
			if _, err := conn.Exec(statement, nil); err != nil {
				return ErrConnectionConfig
			}
		}
		status, err := driverQueryInt64(conn, "PRAGMA cipher_status")
		if err != nil || status != 1 {
			return ErrCipherInactive
		}
		if _, err := driverQueryInt64(conn, "SELECT count(*) FROM sqlite_schema"); err != nil {
			return fmt.Errorf("verify SQLCipher key: %w", err)
		}
		return nil
	}}
	return sqliteDriver.Open(c.dsn)
}

func (c *encryptedConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }

func applyRawKey(conn *sqlite3.SQLiteConn, key RawKey) error {
	const prefix = `PRAGMA key = "x'`
	const suffix = `'"`
	command := make([]byte, len(prefix)+hex.EncodedLen(len(key))+len(suffix))
	copy(command, prefix)
	hex.Encode(command[len(prefix):len(prefix)+64], key[:])
	copy(command[len(prefix)+64:], suffix)
	defer clear(command)
	_, err := conn.Exec(string(command), nil)
	return err
}

func driverQueryString(conn *sqlite3.SQLiteConn, query string) (string, error) {
	value, err := driverQueryValue(conn, query)
	if err != nil {
		return "", err
	}
	result, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("unexpected SQLite result type %T", value)
	}
	return result, nil
}

func driverQueryInt64(conn *sqlite3.SQLiteConn, query string) (int64, error) {
	value, err := driverQueryValue(conn, query)
	if err != nil {
		return 0, err
	}
	result, ok := value.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected SQLite result type %T", value)
	}
	return result, nil
}

func driverQueryValue(conn *sqlite3.SQLiteConn, query string) (driver.Value, error) {
	rows, err := conn.Query(query, nil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrCipherUnavailable
		}
		return nil, err
	}
	return values[0], nil
}
