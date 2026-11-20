package publicassets

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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
	// Replace the final component while reads continue. Unix rename is atomic;
	// Windows may temporarily deny replacement while a reader holds a handle.
	// Neither behavior makes a pathname check before an ordinary open safe.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	writerDone := make(chan struct{})
	writerErrors := make(chan error, 1)
	var replacements atomic.Int32
	go func() {
		defer close(writerDone)
		for i := 0; i < 300; i++ {
			if err := ctx.Err(); err != nil {
				writerErrors <- err
				return
			}
			next := filepath.Join(root, "assets", "next")
			if err := os.Symlink("../.env", next); err != nil {
				writerErrors <- err
				return
			}
			if err := renameAssetForTest(ctx, next, asset); err != nil {
				writerErrors <- err
				return
			}
			replacements.Add(1)
			if err := os.WriteFile(next, []byte("public"), 0o600); err != nil {
				writerErrors <- err
				return
			}
			if err := renameAssetForTest(ctx, next, asset); err != nil {
				writerErrors <- err
				return
			}
			replacements.Add(1)
		}
	}()
	defer func() {
		cancel()
		<-writerDone
	}()
reads:
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("file replacement did not finish while concurrent reads were active")
		case <-writerDone:
			if i >= 1000 {
				break reads
			}
		default:
		}
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
	select {
	case err := <-writerErrors:
		t.Fatal(err)
	default:
	}
	if got := replacements.Load(); got != 600 {
		t.Fatalf("completed %d replacements, want all 600", got)
	}
	// A permanently failing Open must not satisfy the security assertion merely
	// because every attempted read was rejected during the replacement loop.
	file, err := Open(root, "assets/current.svg")
	if err != nil {
		t.Fatalf("open final public file: %v", err)
	}
	body, readErr := io.ReadAll(file)
	_ = file.Close()
	if readErr != nil || string(body) != "public" {
		t.Fatalf("final public content: %q, %v", body, readErr)
	}
}
