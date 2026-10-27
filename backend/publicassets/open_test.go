package publicassets

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func writeFixture(t *testing.T, filename, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireLink(t *testing.T, target, filename string) {
	t.Helper()
	if err := os.Symlink(target, filename); err != nil {
		t.Fatalf("cannot exercise required symlink boundary: %v", err)
	}
}

func TestOpenRejectsLinksAndUnsafeNames(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "assets", "ok.svg"), "public")
	writeFixture(t, filepath.Join(root, ".env"), "private")
	writeFixture(t, filepath.Join(root, ".private", "ok.svg"), "private")
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "key.svg"), "private")
	for name, target := range map[string]string{
		"relative.svg": "../.env", "absolute.svg": filepath.Join(root, ".env"),
		"outside.svg": filepath.Join(outside, "key.svg"), "directory": "../.private",
		"public.svg": "ok.svg", "chain.svg": "relative.svg", "broken.svg": "missing",
		"loop.svg": "loop.svg",
	} {
		requireLink(t, target, filepath.Join(root, "assets", name))
	}
	for _, name := range []string{"assets/relative.svg", "assets/absolute.svg", "assets/outside.svg", "assets/directory/ok.svg", "assets/public.svg", "assets/chain.svg", "assets/broken.svg", "assets/loop.svg", "assets", "../.env", "/assets/ok.svg", "assets/../.env", "assets/ok.svg:stream", `assets\ok.svg`, ".env"} {
		file, err := Open(root, name)
		if err == nil {
			_ = file.Close()
			t.Fatalf("accepted %q", name)
		}
	}
	file, err := Open(root, "assets/ok.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "public" {
		t.Fatalf("regular file: %q, %v", body, err)
	}
	alias := filepath.Join(t.TempDir(), "root-link")
	requireLink(t, root, alias)
	for _, linkedRoot := range []string{alias, alias + string(filepath.Separator)} {
		if file, err := Open(linkedRoot, "assets/ok.svg"); err == nil {
			_ = file.Close()
			t.Fatal("accepted linked public root")
		}
	}
}

func TestOpenDoesNotReadPrivateTargetDuringReplacement(t *testing.T) {
	root := t.TempDir()
	asset := filepath.Join(root, "assets", "current.svg")
	writeFixture(t, asset, "public")
	writeFixture(t, filepath.Join(root, ".env"), "private-marker")
	// The final component is atomically replaced. No pre-open pathname check
	// can establish the safety of a subsequent ordinary open.
	var writer sync.WaitGroup
	writer.Add(1)
	writerErrors := make(chan error, 1)
	go func() {
		defer writer.Done()
		for i := 0; i < 300; i++ {
			next := filepath.Join(root, "assets", "next")
			if err := os.Symlink("../.env", next); err != nil {
				writerErrors <- err
				return
			}
			if err := os.Rename(next, asset); err != nil {
				writerErrors <- err
				return
			}
			if err := os.WriteFile(next, []byte("public"), 0o600); err != nil {
				writerErrors <- err
				return
			}
			if err := os.Rename(next, asset); err != nil {
				writerErrors <- err
				return
			}
		}
	}()
	defer writer.Wait()
	for i := 0; i < 1000; i++ {
		file, err := Open(root, "assets/current.svg")
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil || string(body) != "public" {
			t.Fatalf("unexpected opened content: %q, %v", body, readErr)
		}
	}
	writer.Wait()
	select {
	case err := <-writerErrors:
		t.Fatal(err)
	default:
	}
}
