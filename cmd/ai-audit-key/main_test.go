package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitRotateRetireNeverPrintRawKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	now := time.Now().UTC().Truncate(time.Second)
	random := bytes.NewReader(append(bytes.Repeat([]byte{0x71}, 32), bytes.Repeat([]byte{0x72}, 32)...))
	var stdout, stderr bytes.Buffer
	environment := commandEnvironment{
		stdout: &stdout,
		stderr: &stderr,
		now:    func() time.Time { return now },
		random: random,
	}
	if exit := run([]string{"init", "--file", path}, environment); exit != 0 {
		t.Fatalf("init exit=%d stderr=%q", exit, stderr.String())
	}
	assertStatusOnly(t, path, stdout.String())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("keyring mode=%04o, want 0600", got)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := run([]string{"rotate", "--file", path, "--overlap", "1h"}, environment); exit != 0 {
		t.Fatalf("rotate exit=%d stderr=%q", exit, stderr.String())
	}
	assertStatusOnly(t, path, stdout.String())
	var rotated statusOutput
	if err := json.Unmarshal(stdout.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.PreviousKeyID == "" || rotated.PreviousExpires != now.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("rotation status=%#v", rotated)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := run([]string{"retire", "--file", path}, environment); exit != 0 {
		t.Fatalf("retire exit=%d stderr=%q", exit, stderr.String())
	}
	assertStatusOnly(t, path, stdout.String())
	var retired statusOutput
	if err := json.Unmarshal(stdout.Bytes(), &retired); err != nil {
		t.Fatal(err)
	}
	if retired.PreviousKeyID != "" || retired.PreviousExpires != "" {
		t.Fatalf("retired status=%#v", retired)
	}
}

func assertStatusOnly(t *testing.T, path, output string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		CurrentKey string `json:"current_key"`
		Previous   *struct {
			Key string `json:"key"`
		} `json:"previous"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{document.CurrentKey} {
		if key != "" && strings.Contains(output, key) {
			t.Fatalf("CLI output exposed current key: %q", output)
		}
	}
	if document.Previous != nil && document.Previous.Key != "" && strings.Contains(output, document.Previous.Key) {
		t.Fatalf("CLI output exposed previous key: %q", output)
	}
	if !strings.Contains(output, `"current_key_id":"ak1-`) {
		t.Fatalf("CLI status missing non-secret key ID: %q", output)
	}
}
