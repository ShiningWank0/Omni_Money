package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
)

func TestIssueRotateListAndRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	firstRandom := bytes.Repeat([]byte{0x11}, 32)
	firstToken := base64.RawURLEncoding.EncodeToString(firstRandom)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	environment := commandEnvironment{stdout: stdout, stderr: stderr, now: func() time.Time { return now }, random: bytes.NewReader(firstRandom)}
	exitCode := run([]string{
		"issue", "--file", path, "--id", "agent-1",
		"--expires-at", now.Add(24 * time.Hour).Format(time.RFC3339),
		"--scope", "analysis:summary", "--scope", "console:relay",
		"--account", "現金", "--max-analysis-days", "30", "--max-results", "100",
		"--tag-id", "10", "--tag-id", "20",
		"--analysis-start-date", "2026-01-01", "--analysis-end-date", "2026-12-31",
	}, environment)
	if exitCode != 0 {
		t.Fatalf("issue exit=%d stderr=%q", exitCode, stderr.String())
	}
	if stdout.String() != firstToken+"\n" {
		t.Fatalf("issue stdout=%q, want raw token only", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"action":"issue"`) || strings.Contains(stderr.String(), firstToken) {
		t.Fatalf("issue audit is missing or unsafe: %q", stderr.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(firstToken)) || !bytes.Contains(content, []byte(aicredentials.HashToken(firstToken))) {
		t.Fatal("credential file did not contain only the token hash")
	}
	if mode := fileMode(t, path); mode != 0o600 {
		t.Fatalf("credential mode=%04o, want 0600", mode)
	}

	store, err := aicredentials.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := store.Authenticate(firstToken, now)
	if err != nil {
		t.Fatalf("issued token failed authentication: %v", err)
	}
	if !issued.AllowsTag(10) || issued.AllowsTag(30) {
		t.Fatalf("issued tag allowlist = %#v", issued.AllowedTagIDs)
	}

	listOutput := &bytes.Buffer{}
	if exit := run([]string{"list", "--file", path}, commandEnvironment{stdout: listOutput, stderr: stderr}); exit != 0 {
		t.Fatalf("list exit=%d stderr=%q", exit, stderr.String())
	}
	if strings.Contains(listOutput.String(), firstToken) || !strings.Contains(listOutput.String(), `"agent-1"`) {
		t.Fatalf("unsafe or incomplete list output: %s", listOutput.String())
	}

	secondRandom := bytes.Repeat([]byte{0x22}, 32)
	secondToken := base64.RawURLEncoding.EncodeToString(secondRandom)
	stdout.Reset()
	stderr.Reset()
	environment = commandEnvironment{stdout: stdout, stderr: stderr, now: func() time.Time { return now }, random: bytes.NewReader(secondRandom)}
	if exit := run([]string{
		"rotate", "--file", path, "--id", "agent-1",
		"--expires-at", now.Add(48 * time.Hour).Format(time.RFC3339),
	}, environment); exit != 0 {
		t.Fatalf("rotate exit=%d stderr=%q", exit, stderr.String())
	}
	if stdout.String() != secondToken+"\n" {
		t.Fatalf("rotate stdout=%q, want raw token only", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"action":"rotate"`) || strings.Contains(stderr.String(), secondToken) {
		t.Fatalf("rotate audit is missing or unsafe: %q", stderr.String())
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(firstToken, now); err == nil {
		t.Fatal("old token remained valid after rotate")
	}
	if _, err := store.Authenticate(secondToken, now); err != nil {
		t.Fatalf("rotated token failed: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := run([]string{"revoke", "--file", path, "--id", "agent-1"}, commandEnvironment{stdout: stdout, stderr: stderr}); exit != 0 {
		t.Fatalf("revoke exit=%d stderr=%q", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("revoke exposed stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"action":"revoke"`) || strings.Contains(stderr.String(), secondToken) {
		t.Fatalf("revoke audit is missing or unsafe: %q", stderr.String())
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(secondToken, now); err == nil {
		t.Fatal("revoked token remained valid")
	}
}

func TestIssueDoesNotPrintTokenWhenWriteFails(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	environment := commandEnvironment{
		stdout: stdout,
		stderr: stderr,
		now:    func() time.Time { return now },
		random: bytes.NewReader(bytes.Repeat([]byte{0x33}, 32)),
	}
	exit := run([]string{
		"issue", "--file", filepath.Join(t.TempDir(), "missing", "credentials.json"), "--id", "agent-1",
		"--expires-at", now.Add(time.Hour).Format(time.RFC3339),
		"--scope", "analysis:summary", "--account", "cash",
		"--analysis-start-date", "2026-01-01", "--analysis-end-date", "2026-12-31",
	}, environment)
	if exit == 0 {
		t.Fatal("issue unexpectedly succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("token was printed before durable write: %q", stdout.String())
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
