package securedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
)

type databaseQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var (
	ErrCipherIntegrity = errors.New("SQLCipher page authentication failed")
	ErrSQLiteIntegrity = errors.New("SQLite integrity check failed")
	// ErrPlaintextHeader marks a database whose first page exposes SQLite's
	// plaintext magic. Encrypted vault snapshots must never have this header.
	ErrPlaintextHeader = errors.New("database has a plaintext SQLite header")
)

// IntegrityMismatchError is returned only when an integrity pragma completed
// normally and reported a failing row/result.  Driver, query, scan, context,
// and rows-close failures are deliberately returned as their original errors
// so callers can distinguish damaged content from an unavailable database.
type IntegrityMismatchError struct {
	kind   error
	detail string
}

func (e *IntegrityMismatchError) Error() string {
	if e == nil {
		return "integrity check failed"
	}
	if e.kind == nil {
		return e.detail
	}
	if e.detail == "" {
		return e.kind.Error()
	}
	return fmt.Sprintf("%s: %s", e.kind, e.detail)
}

func (e *IntegrityMismatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.kind
}

func newIntegrityMismatch(kind error, detail string) error {
	return &IntegrityMismatchError{kind: kind, detail: detail}
}

// closeIntegrityRows preserves both iteration and driver Close failures. A
// Close can itself update Rows.Err, so inspect it before and after closing.
func closeIntegrityRows(rows *sql.Rows) error {
	if rows == nil {
		return nil
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	return errors.Join(iterationErr, closeErr, rows.Err())
}

func (o *Opener) CheckIntegrity(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if o.Encrypted() {
		rows, err := db.QueryContext(ctx, "PRAGMA cipher_integrity_check")
		if err != nil {
			return fmt.Errorf("cipher integrity query: %w", err)
		}
		if rows.Next() {
			var detail string
			if scanErr := rows.Scan(&detail); scanErr != nil {
				return errors.Join(fmt.Errorf("cipher integrity result scan: %w", scanErr), closeIntegrityRows(rows))
			}
			if rowsErr := closeIntegrityRows(rows); rowsErr != nil {
				return fmt.Errorf("cipher integrity rows: %w", rowsErr)
			}
			return newIntegrityMismatch(ErrCipherIntegrity, detail)
		}
		if rowsErr := closeIntegrityRows(rows); rowsErr != nil {
			return fmt.Errorf("cipher integrity rows: %w", rowsErr)
		}
	}
	return expectSQLiteIntegrity(ctx, db)
}

func expectSQLiteIntegrity(ctx context.Context, db databaseQueryer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("SQLite integrity query: %w", err)
	}
	if !rows.Next() {
		rowsErr := closeIntegrityRows(rows)
		if rowsErr != nil {
			return fmt.Errorf("SQLite integrity rows: %w", rowsErr)
		}
		return sql.ErrNoRows
	}
	var result string
	if err := rows.Scan(&result); err != nil {
		return errors.Join(fmt.Errorf("SQLite integrity result scan: %w", err), closeIntegrityRows(rows))
	}
	if rowsErr := closeIntegrityRows(rows); rowsErr != nil {
		return fmt.Errorf("SQLite integrity rows: %w", rowsErr)
	}
	if result != "ok" {
		return newIntegrityMismatch(ErrSQLiteIntegrity, result)
	}
	return nil
}

func expectForeignKeyIntegrity(ctx context.Context, db databaseQueryer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check query: %w", err)
	}
	if rows.Next() {
		values := make([]any, 4)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return errors.Join(fmt.Errorf("foreign_key_check result scan: %w", err), closeIntegrityRows(rows))
		}
		if rowsErr := closeIntegrityRows(rows); rowsErr != nil {
			return fmt.Errorf("foreign_key_check rows: %w", rowsErr)
		}
		return newIntegrityMismatch(ErrSQLiteIntegrity, fmt.Sprintf("foreign key violation in table %v row %v", values[0], values[1]))
	}
	if rowsErr := closeIntegrityRows(rows); rowsErr != nil {
		return fmt.Errorf("foreign_key_check rows: %w", rowsErr)
	}
	return nil
}

func RequireEncryptedHeader(path string) error {
	file, err := openNoFollowPrivateFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return RequireEncryptedHeaderFile(file)
}

// RequireEncryptedHeaderFile verifies the encrypted-header invariant through
// an already-open, caller-owned descriptor. It deliberately does not close the
// file. Descriptor-based callers use this form when a stable /proc/self/fd or
// /dev/fd pathname would otherwise be rejected by the no-follow path opener.
func RequireEncryptedHeaderFile(file *os.File) error {
	if file == nil {
		return errors.New("encrypted database header file is nil")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read encrypted database header: %w", err)
	}
	if string(header) == "SQLite format 3\x00" {
		return ErrPlaintextHeader
	}
	return nil
}

// Kept private for package-local compatibility with older focused tests.
func requireEncryptedHeaderFile(file *os.File) error {
	return RequireEncryptedHeaderFile(file)
}
