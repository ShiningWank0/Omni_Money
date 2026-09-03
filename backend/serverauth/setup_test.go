//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package serverauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAuthorizerReadsPrivateFileAndKeepsOnlyDigest(t *testing.T) {
	directory := safeSetupTestDir(t)
	path := filepath.Join(directory, "setup-token")
	token := strings.Repeat("s", 48)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
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
	directory := safeSetupTestDir(t)
	for name, content := range map[string]string{
		"short":      "too-short",
		"whitespace": strings.Repeat("a", 32) + " b",
		"double-eol": strings.Repeat("a", 32) + "\n\n",
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSetupAuthorizer(path); err == nil {
			t.Fatalf("ambiguous setup token %q was accepted", name)
		}
	}
	public := filepath.Join(directory, "public")
	if err := os.WriteFile(public, []byte(strings.Repeat("p", 48)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSetupAuthorizer(public); err == nil {
		t.Fatal("public setup token file was accepted")
	}
}

func TestSetupAuthorizerRequiresAbsoluteCleanPath(t *testing.T) {
	directory := safeSetupTestDir(t)
	path := writeSetupToken(t, directory, "setup-token")

	if _, err := LoadSetupAuthorizer(filepath.Base(path)); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative setup token path was not rejected: %v", err)
	}
	unclean := directory + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(path)
	if _, err := LoadSetupAuthorizer(unclean); err == nil || !strings.Contains(err.Error(), "clean") {
		t.Fatalf("unclean setup token path was not rejected: %v", err)
	}
}

func TestSetupAuthorizerRejectsWritableParentDirectory(t *testing.T) {
	directory := safeSetupTestDir(t)
	path := writeSetupToken(t, directory, "setup-token")
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	if _, err := LoadSetupAuthorizer(path); err == nil || !strings.Contains(err.Error(), "writable by group or other") {
		t.Fatalf("token below group-writable parent was not rejected: %v", err)
	}
}

func TestSetupAuthorizerAcceptsProtectedLexicalSymlink(t *testing.T) {
	directory := safeSetupTestDir(t)
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSetupToken(t, target, "setup-token")
	link := filepath.Join(directory, "protected-link")
	if err := os.Symlink("target", link); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSetupAuthorizer(filepath.Join(link, "setup-token")); err != nil {
		t.Fatalf("token through a protected symlink was rejected: %v", err)
	}
}

func TestSetupAuthorizerRejectsWritableResolvedSymlinkTarget(t *testing.T) {
	directory := safeSetupTestDir(t)
	target := filepath.Join(directory, "writable-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSetupToken(t, target, "setup-token")
	if err := os.Chmod(target, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o700) })
	link := filepath.Join(directory, "protected-link")
	if err := os.Symlink("writable-target", link); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSetupAuthorizer(filepath.Join(link, "setup-token")); err == nil || !strings.Contains(err.Error(), "writable by group or other") {
		t.Fatalf("token through symlink to a writable target was not rejected: %v", err)
	}
}

func TestSetupAuthorizerRejectsSymlinkAndNonRegularLeaf(t *testing.T) {
	directory := safeSetupTestDir(t)
	target := writeSetupToken(t, directory, "target")
	link := filepath.Join(directory, "link")
	if err := os.Symlink(filepath.Base(target), link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSetupAuthorizer(link); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symbolic-link setup token leaf was not rejected: %v", err)
	}

	nonRegular := filepath.Join(directory, "directory-leaf")
	if err := os.Mkdir(nonRegular, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSetupAuthorizer(nonRegular); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("non-regular setup token leaf was not rejected: %v", err)
	}
}

func TestTrustedSetupTokenUID(t *testing.T) {
	effectiveUID := uint64(os.Geteuid())
	if !trustedSetupTokenUID(0) {
		t.Fatal("root was not accepted as a trusted setup token owner")
	}
	if !trustedSetupTokenUID(effectiveUID) {
		t.Fatal("effective server UID was not accepted as a trusted setup token owner")
	}
	foreignUID := effectiveUID + 1
	if foreignUID == 0 || foreignUID == effectiveUID {
		foreignUID = 1
		if foreignUID == effectiveUID {
			foreignUID = 2
		}
	}
	if trustedSetupTokenUID(foreignUID) {
		t.Fatal("foreign UID was accepted as a trusted setup token owner")
	}
}

func safeSetupTestDir(t *testing.T) string {
	t.Helper()
	// Prefer the test runner's private temporary tree. Linux normally places
	// it below shared /tmp, so use the protected user home only when the same
	// production ancestor validation rejects that temporary tree.
	base := t.TempDir()
	if err := validateSetupTokenDirectoryChain(base, true); err != nil {
		base, err = os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.MkdirTemp(base, ".omni-setup-token-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	directory, err = filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeSetupToken(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("s", 48)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
