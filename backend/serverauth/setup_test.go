//go:build !windows

package serverauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAuthorizerReadsPrivateFileAndKeepsOnlyDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup-token")
	token := strings.Repeat("s", 48)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	authorizer, err := LoadSetupAuthorizer(path)
	if err != nil {
		t.Fatal(err)
	}
	if !authorizer.Authorize([]byte(token)) {
		t.Fatal("valid setup token was rejected")
	}
	if authorizer.Authorize([]byte(token + "x")) {
		t.Fatal("invalid setup token was accepted")
	}
	authorizer.Destroy()
	if authorizer.Authorize([]byte(token)) {
		t.Fatal("destroyed setup authorizer still accepted its token")
	}
}

func TestSetupAuthorizerRejectsUnsafeOrAmbiguousFiles(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{
		"short":      "too-short",
		"whitespace": strings.Repeat("a", 32) + " b",
		"double-eol": strings.Repeat("a", 32) + "\n\n",
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSetupAuthorizer(path); err == nil {
			t.Fatalf("ambiguous setup token %q was accepted", name)
		}
	}
	public := filepath.Join(directory, "public")
	if err := os.WriteFile(public, []byte(strings.Repeat("p", 48)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSetupAuthorizer(public); err == nil {
		t.Fatal("public setup token file was accepted")
	}
}
