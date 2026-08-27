package audithmac

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFixedAccountReferenceAndKeyIDVector(t *testing.T) {
	master := make([]byte, 32)
	for index := range master {
		master[index] = byte(index)
	}
	document := &keyringDocument{
		Version:    CurrentVersion,
		CurrentKey: base64.RawURLEncoding.EncodeToString(master),
	}
	snapshot, err := buildSnapshot(document, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	references := (Snapshot{data: snapshot}).AccountReferences("現金", time.Now().UTC())
	if got, want := references.Current.KeyID, "ak1-ffb1a6aa723e3eaecb492b6f667d060a"; got != want {
		t.Fatalf("key ID = %q, want %q", got, want)
	}
	if got, want := references.Current.HMACSHA256, "01b3ab6bfa544d6e195f109dc916986439dc9975b3072addb52d1d4a26d62ca4"; got != want {
		t.Fatalf("account HMAC = %q, want %q", got, want)
	}
	other := (Snapshot{data: snapshot}).AccountReferences("普通預金", time.Now().UTC())
	if other.Current.HMACSHA256 == references.Current.HMACSHA256 {
		t.Fatal("different accounts produced the same reference")
	}
}

func TestRotationOverlapAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	firstRaw := bytes.Repeat([]byte{0x11}, 32)
	firstStatus, err := InitializeFile(path, bytes.NewReader(firstRaw))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	secondRaw := bytes.Repeat([]byte{0x22}, 32)
	rotatedStatus, err := RotateFile(path, bytes.NewReader(secondRaw), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedStatus.CurrentKeyID == firstStatus.CurrentKeyID || rotatedStatus.PreviousKeyID != firstStatus.CurrentKeyID {
		t.Fatalf("rotation status = %#v, initial = %#v", rotatedStatus, firstStatus)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	during := store.Snapshot().AccountReferences("cash", now.Add(30*time.Minute))
	if during.Previous == nil || during.Previous.KeyID != firstStatus.CurrentKeyID {
		t.Fatalf("overlap reference = %#v", during)
	}
	after := store.Snapshot().AccountReferences("cash", now.Add(time.Hour))
	if after.Previous != nil || after.Current != during.Current {
		t.Fatalf("expired overlap reference = %#v, during = %#v", after, during)
	}
	if _, err := RetirePreviousFile(path, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().AccountReferences("cash", now).Previous; got != nil {
		t.Fatalf("retired previous reference = %#v", got)
	}
}

func TestStrictKeyringValidation(t *testing.T) {
	validKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	otherKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32))
	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown field", content: `{"version":1,"current_key":"` + validKey + `","unknown":true}`},
		{name: "duplicate field", content: `{"version":1,"version":1,"current_key":"` + validKey + `"}`},
		{name: "trailing JSON", content: `{"version":1,"current_key":"` + validKey + `"} {}`},
		{name: "short key", content: `{"version":1,"current_key":"` + base64.RawURLEncoding.EncodeToString([]byte("short")) + `"}`},
		{name: "padded key", content: `{"version":1,"current_key":"` + validKey + `="}`},
		{name: "wrong version", content: `{"version":2,"current_key":"` + validKey + `"}`},
		{name: "same previous", content: `{"version":1,"current_key":"` + validKey + `","previous":{"key":"` + validKey + `","emit_until":"` + time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339) + `"}}`},
		{name: "overlap too long", content: `{"version":1,"current_key":"` + validKey + `","previous":{"key":"` + otherKey + `","emit_until":"` + time.Now().UTC().Add(MaxOverlap+time.Hour).Truncate(time.Second).Format(time.RFC3339) + `"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := decodeDocument([]byte(test.content))
			if err == nil {
				_, err = buildSnapshot(document, time.Now().UTC())
			}
			if err == nil {
				t.Fatal("invalid keyring was accepted")
			}
			if strings.Contains(err.Error(), validKey) {
				t.Fatal("validation error exposed key material")
			}
		})
	}
}

func TestInvalidReloadRetainsLastSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x44}, 32))); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot().AccountReferences("cash", time.Now().UTC()).Current
	if err := os.WriteFile(path, []byte(`{"version":1,"current_key":"too-short"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("invalid reload succeeded")
	}
	after := store.Snapshot().AccountReferences("cash", time.Now().UTC()).Current
	if after != before {
		t.Fatalf("invalid reload changed snapshot: before=%#v after=%#v", before, after)
	}
}

func TestKeyringRejectsUnsafeFileKindsAndPermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "audit-keyring.json")
	if _, err := InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x48}, 32))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("world-readable host keyring was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "audit-keyring-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(link); err == nil {
		t.Fatal("symlinked keyring was accepted")
	}
	if _, err := NewStore(directory); err == nil {
		t.Fatal("directory keyring was accepted")
	}
	oversized := filepath.Join(directory, "oversized-keyring.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{'x'}, int(MaxFileSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(oversized); err == nil {
		t.Fatal("oversized keyring was accepted")
	}
}

func TestInitializeNeverOverwritesExistingKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x49}, 32))); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x50}, 32))); err == nil {
		t.Fatal("second initialization overwrote the keyring")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed initialization changed the existing keyring")
	}
}

func TestConcurrentSnapshotsRemainInternallyConsistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x51}, 32))); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1000; iteration++ {
				reference := store.Snapshot().AccountReferences("cash", time.Now().UTC()).Current
				if !strings.HasPrefix(reference.KeyID, "ak1-") || len(reference.HMACSHA256) != 64 {
					t.Errorf("incomplete reference: %#v", reference)
					return
				}
			}
		}()
	}
	for iteration := 0; iteration < 20; iteration++ {
		document := &keyringDocument{
			Version:    CurrentVersion,
			CurrentKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(iteration + 80)}, 32)),
		}
		if err := writeDocumentAtomic(path, document, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := store.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}
