//go:build !windows

package securedb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLCipherImmutableReadOnlyOpensDescriptorPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "descriptor.db")
	opener, writable := requireSQLCipher(t, path, fixedKey(0x4d))
	t.Cleanup(opener.Destroy)
	if _, err := writable.Exec("CREATE TABLE sample (value TEXT NOT NULL); INSERT INTO sample(value) VALUES ('bound')"); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	anchor, err := os.Open(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	descriptorPath := ""
	for _, directory := range []string{"/proc/self/fd", "/dev/fd"} {
		candidate := fmt.Sprintf("%s/%d", directory, anchor.Fd())
		probe, probeErr := os.Open(candidate) // #nosec G304 -- generated from this process's live descriptor.
		if probeErr == nil {
			_ = probe.Close()
			descriptorPath = candidate
			break
		}
	}
	if descriptorPath == "" {
		t.Skip("stable descriptor path is unavailable")
	}

	readOnly, err := opener.Open(t.Context(), descriptorPath, ImmutableReadOnly)
	if err != nil {
		t.Fatalf("open SQLCipher descriptor path: %v", err)
	}
	defer readOnly.Close()
	var value string
	if err := readOnly.QueryRowContext(t.Context(), "SELECT value FROM sample").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "bound" {
		t.Fatalf("descriptor value=%q, want bound", value)
	}
	if _, err := readOnly.ExecContext(t.Context(), "INSERT INTO sample(value) VALUES ('write')"); err == nil {
		t.Fatal("immutable read-only SQLCipher descriptor accepted a write")
	}
}
