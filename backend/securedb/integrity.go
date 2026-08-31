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

func (o *Opener) CheckIntegrity(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is not open")
	}
	if o.Encrypted() {
		rows, err := db.QueryContext(ctx, "PRAGMA cipher_integrity_check")
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCipherIntegrity, err)
		}
		defer rows.Close()
		if rows.Next() {
			var detail string
			_ = rows.Scan(&detail)
			return fmt.Errorf("%w: %s", ErrCipherIntegrity, detail)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrCipherIntegrity, err)
		}
	}
	return expectSQLiteIntegrity(ctx, db)
}

func expectSQLiteIntegrity(ctx context.Context, db databaseQueryer) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("%w: %w", ErrSQLiteIntegrity, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: %s", ErrSQLiteIntegrity, result)
	}
	return nil
}

func expectForeignKeyIntegrity(ctx context.Context, db databaseQueryer) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%w: foreign_key_check: %w", ErrSQLiteIntegrity, err)
	}
	defer rows.Close()
	if rows.Next() {
		values := make([]any, 4)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("%w: foreign_key_check: %v", ErrSQLiteIntegrity, err)
		}
		return fmt.Errorf("%w: foreign key violation in table %v row %v", ErrSQLiteIntegrity, values[0], values[1])
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: foreign_key_check: %w", ErrSQLiteIntegrity, err)
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
