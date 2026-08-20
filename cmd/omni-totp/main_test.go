package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/authn"
)

func TestWriteSecretFileCreatesConfidentialBase32File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "omni-totp.secret")
	secret := []byte("12345678901234567890")

	if err := writeSecretFile(path, secret); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions=%o, want 600", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := authn.DecodeTOTPSecret(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(secret) {
		t.Fatalf("decoded secret=%q, want %q", decoded, secret)
	}
}

func TestWriteSecretFileNeverOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(path, []byte("12345678901234567890")); err == nil {
		t.Fatal("existing file was accepted")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep-me\n" {
		t.Fatalf("existing file changed to %q", content)
	}
}

func TestWriteSecretFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("keep-target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(path, []byte("12345678901234567890")); err == nil {
		t.Fatal("symlink was accepted")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep-target\n" {
		t.Fatalf("symlink target changed to %q", content)
	}
}

func TestBuildOTPAuthURIEncodesIssuerAndAccount(t *testing.T) {
	seed, err := authn.EncodeTOTPSecret([]byte("12345678901234567890"))
	if err != nil {
		t.Fatal(err)
	}
	uri, err := buildOTPAuthURI("Omni / Money", "alice@example.com/admin", seed)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Fatalf("unexpected URI=%s", uri)
	}
	if parsed.Query().Get("issuer") != "Omni / Money" || parsed.Query().Get("secret") != seed {
		t.Fatalf("unexpected query in URI=%s", uri)
	}
	if !strings.Contains(parsed.EscapedPath(), "%2F") {
		t.Fatalf("account slash was not escaped in URI=%s", uri)
	}
}

func TestBuildOTPAuthURIRejectsUnsafeLabels(t *testing.T) {
	seed, _ := authn.EncodeTOTPSecret([]byte("12345678901234567890"))
	for _, test := range []struct {
		issuer, account string
	}{
		{"", "admin"},
		{"Omni\nMoney", "admin"},
		{"Omni Money", ""},
	} {
		if _, err := buildOTPAuthURI(test.issuer, test.account, seed); err == nil {
			t.Errorf("unsafe labels accepted: %#v", test)
		}
	}
}
