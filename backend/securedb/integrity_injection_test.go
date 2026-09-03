package securedb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type integrityInjectionMode struct {
	queryErr error
	value    driver.Value
	nextErr  error
	closeErr error
}

type integrityInjectionConnector struct{ mode integrityInjectionMode }

func (c integrityInjectionConnector) Connect(context.Context) (driver.Conn, error) {
	return integrityInjectionConn{mode: c.mode}, nil
}

func (integrityInjectionConnector) Driver() driver.Driver { return integrityInjectionDriver{} }

type integrityInjectionDriver struct{}

func (integrityInjectionDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("integrity injection driver requires OpenDB")
}

type integrityInjectionConn struct{ mode integrityInjectionMode }

func (integrityInjectionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (integrityInjectionConn) Close() error              { return nil }
func (integrityInjectionConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c integrityInjectionConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.mode.queryErr != nil {
		return nil, c.mode.queryErr
	}
	return &integrityInjectionRows{mode: c.mode}, nil
}

type integrityInjectionRows struct {
	mode    integrityInjectionMode
	yielded bool
	closed  bool
}

func (r *integrityInjectionRows) Columns() []string { return []string{"integrity"} }
func (r *integrityInjectionRows) Close() error {
	r.closed = true
	return r.mode.closeErr
}
func (r *integrityInjectionRows) Next(dest []driver.Value) error {
	if r.yielded {
		return io.EOF
	}
	if r.mode.nextErr != nil {
		r.yielded = true
		return r.mode.nextErr
	}
	r.yielded = true
	dest[0] = r.mode.value
	return nil
}

func openIntegrityInjectionDB(t *testing.T, mode integrityInjectionMode) *sql.DB {
	t.Helper()
	return sql.OpenDB(integrityInjectionConnector{mode: mode})
}

func checkIntegrityInjection(t *testing.T, mode integrityInjectionMode) error {
	t.Helper()
	db := openIntegrityInjectionDB(t, mode)
	defer db.Close()
	return NewPlainOpener().CheckIntegrity(context.Background(), db)
}

func TestCheckIntegrityOnlyNormalMismatchIsContent(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       integrityInjectionMode
		wantDetail string
		wantIs     error
	}{
		{name: "integrity mismatch", mode: integrityInjectionMode{value: "page 1"}, wantDetail: "page 1", wantIs: ErrSQLiteIntegrity},
		{name: "no rows", mode: integrityInjectionMode{nextErr: io.EOF}, wantDetail: ""},
		{name: "null result", mode: integrityInjectionMode{value: nil}, wantDetail: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkIntegrityInjection(t, test.mode)
			if test.wantDetail != "" {
				var mismatch *IntegrityMismatchError
				if !errors.As(err, &mismatch) {
					t.Fatalf("error = %v, want IntegrityMismatchError", err)
				}
				if !errors.Is(err, test.wantIs) || !strings.Contains(err.Error(), test.wantDetail) {
					t.Fatalf("error = %v, want sentinel/detail", err)
				}
				return
			}
			var mismatch *IntegrityMismatchError
			if errors.As(err, &mismatch) {
				t.Fatalf("error = %v, unexpected IntegrityMismatchError", err)
			}
		})
	}
}

func TestCheckIntegrityPreservesOperationalFailuresAndCloseErrors(t *testing.T) {
	queryErr := errors.New("query failure")
	scanErrClose := errors.New("scan close failure")
	nextErr := errors.New("rows iteration failure")
	closeErr := errors.New("rows close failure")
	for _, test := range []struct {
		name string
		mode integrityInjectionMode
		want []error
	}{
		{name: "query", mode: integrityInjectionMode{queryErr: queryErr}, want: []error{queryErr}},
		{name: "scan and early close", mode: integrityInjectionMode{value: nil, closeErr: scanErrClose}, want: []error{scanErrClose}},
		{name: "rows err", mode: integrityInjectionMode{nextErr: nextErr}, want: []error{nextErr}},
		{name: "rows close", mode: integrityInjectionMode{value: "not ok", closeErr: closeErr}, want: []error{closeErr}},
		{name: "context", mode: integrityInjectionMode{queryErr: context.Canceled}, want: []error{context.Canceled}},
		{name: "bad connection", mode: integrityInjectionMode{queryErr: driver.ErrBadConn}, want: []error{driver.ErrBadConn}},
		{name: "busy", mode: integrityInjectionMode{queryErr: sqlite3.Error{Code: sqlite3.ErrBusy}}, want: []error{sqlite3.Error{Code: sqlite3.ErrBusy}}},
		{name: "I/O", mode: integrityInjectionMode{queryErr: sqlite3.Error{Code: sqlite3.ErrIoErr}}, want: []error{sqlite3.Error{Code: sqlite3.ErrIoErr}}},
		{name: "corrupt", mode: integrityInjectionMode{queryErr: sqlite3.Error{Code: sqlite3.ErrCorrupt}}, want: []error{sqlite3.Error{Code: sqlite3.ErrCorrupt}}},
		{name: "notadb", mode: integrityInjectionMode{queryErr: sqlite3.Error{Code: sqlite3.ErrNotADB}}, want: []error{sqlite3.Error{Code: sqlite3.ErrNotADB}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkIntegrityInjection(t, test.mode)
			if err == nil {
				t.Fatal("CheckIntegrity unexpectedly succeeded")
			}
			for _, want := range test.want {
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want errors.Is(..., %v)", err, want)
				}
			}
			var mismatch *IntegrityMismatchError
			if errors.As(err, &mismatch) {
				t.Fatalf("error = %v, unexpected IntegrityMismatchError", err)
			}
		})
	}
}
