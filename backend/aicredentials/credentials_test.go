package aicredentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken   = "0123456789abcdef0123456789abcdef"
	secondToken = "abcdef0123456789abcdef0123456789"
	testTokenID = "agent-primary"
	testAccount = "現金"
)

func validTestFile() *File {
	notBefore := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	return &File{
		Version: CurrentVersion,
		Credentials: []Credential{{
			ID:                testTokenID,
			TokenSHA256:       HashToken(testToken),
			NotBefore:         notBefore,
			ExpiresAt:         notBefore.Add(30 * 24 * time.Hour),
			Scopes:            []string{"analysis:summary", "console:relay"},
			Accounts:          []string{testAccount},
			AllowedTagIDs:     []int64{10, 20},
			MaxAnalysisDays:   30,
			MaxResults:        100,
			AnalysisStartDate: "2026-01-01",
			AnalysisEndDate:   "2026-12-31",
		}},
	}
}

func TestFileValidateAndCredentialHelpers(t *testing.T) {
	document := validTestFile()
	if err := document.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	credential := &document.Credentials[0]
	if !credential.HasScope("analysis:summary") || credential.HasScope("analysis:memo") {
		t.Fatalf("unexpected scopes: %#v", credential.Scopes)
	}
	if !credential.AllowsAccount(testAccount) || credential.AllowsAccount("銀行") {
		t.Fatalf("unexpected accounts: %#v", credential.Accounts)
	}
	if !credential.AllowsTag(10) || credential.AllowsTag(30) {
		t.Fatalf("unexpected allowed tags: %#v", credential.AllowedTagIDs)
	}
	if !credential.AllowConsoleRelay {
		t.Fatal("AllowConsoleRelay = false, want true")
	}
	if credential.MaxTransactionsPerDay != DefaultMaxTransactionsPerDay {
		t.Fatalf("MaxTransactionsPerDay = %d, want backward-compatible default %d", credential.MaxTransactionsPerDay, DefaultMaxTransactionsPerDay)
	}
}

func TestFileValidateRejectsInvalidCredentials(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*File)
	}{
		{name: "version", mutate: func(f *File) { f.Version = 2 }},
		{name: "missing credentials array", mutate: func(f *File) { f.Credentials = nil }},
		{name: "id", mutate: func(f *File) { f.Credentials[0].ID = "../agent" }},
		{name: "uppercase hash", mutate: func(f *File) { f.Credentials[0].TokenSHA256 = strings.ToUpper(f.Credentials[0].TokenSHA256) }},
		{name: "missing not before", mutate: func(f *File) { f.Credentials[0].NotBefore = time.Time{} }},
		{name: "missing expiry", mutate: func(f *File) { f.Credentials[0].ExpiresAt = time.Time{} }},
		{name: "expiry order", mutate: func(f *File) { f.Credentials[0].ExpiresAt = f.Credentials[0].NotBefore }},
		{name: "lifetime", mutate: func(f *File) {
			f.Credentials[0].ExpiresAt = f.Credentials[0].NotBefore.Add(MaxCredentialLifetime + time.Second)
		}},
		{name: "missing scopes", mutate: func(f *File) { f.Credentials[0].Scopes = nil }},
		{name: "unsupported scope", mutate: func(f *File) { f.Credentials[0].Scopes = []string{"admin"} }},
		{name: "duplicate scope", mutate: func(f *File) { f.Credentials[0].Scopes = []string{"analysis:summary", "analysis:summary"} }},
		{name: "detail scope dependency", mutate: func(f *File) { f.Credentials[0].Scopes = []string{"analysis:transactions"} }},
		{name: "memo scope dependency", mutate: func(f *File) { f.Credentials[0].Scopes = []string{"analysis:summary", "analysis:memo"} }},
		{name: "missing accounts", mutate: func(f *File) { f.Credentials[0].Accounts = nil }},
		{name: "blank account", mutate: func(f *File) { f.Credentials[0].Accounts = []string{" "} }},
		{name: "wildcard", mutate: func(f *File) { f.Credentials[0].Accounts = []string{"*"} }},
		{name: "invalid tag id", mutate: func(f *File) { f.Credentials[0].AllowedTagIDs = []int64{0} }},
		{name: "duplicate tag id", mutate: func(f *File) { f.Credentials[0].AllowedTagIDs = []int64{10, 10} }},
		{name: "too many tag ids", mutate: func(f *File) {
			f.Credentials[0].AllowedTagIDs = make([]int64, MaxAllowedTagIDs+1)
			for i := range f.Credentials[0].AllowedTagIDs {
				f.Credentials[0].AllowedTagIDs[i] = int64(i + 1)
			}
		}},
		{name: "analysis days low", mutate: func(f *File) { f.Credentials[0].MaxAnalysisDays = 0 }},
		{name: "analysis days high", mutate: func(f *File) { f.Credentials[0].MaxAnalysisDays = 367 }},
		{name: "results low", mutate: func(f *File) { f.Credentials[0].MaxResults = 0 }},
		{name: "results high", mutate: func(f *File) { f.Credentials[0].MaxResults = 501 }},
		{name: "transaction quota low", mutate: func(f *File) { f.Credentials[0].MaxTransactionsPerDay = -1 }},
		{name: "transaction quota high", mutate: func(f *File) { f.Credentials[0].MaxTransactionsPerDay = MaxTransactionsPerDay + 1 }},
		{name: "analysis dates missing", mutate: func(f *File) { f.Credentials[0].AnalysisStartDate = ""; f.Credentials[0].AnalysisEndDate = "" }},
		{name: "analysis date partial", mutate: func(f *File) { f.Credentials[0].AnalysisEndDate = "" }},
		{name: "analysis date format", mutate: func(f *File) { f.Credentials[0].AnalysisStartDate = "2026/01/01" }},
		{name: "analysis date order", mutate: func(f *File) { f.Credentials[0].AnalysisStartDate = "2027-01-01" }},
		{name: "duplicate id", mutate: func(f *File) { f.Credentials = append(f.Credentials, f.Credentials[0]) }},
		{name: "duplicate hash", mutate: func(f *File) {
			second := f.Credentials[0]
			second.ID = "second-agent"
			f.Credentials = append(f.Credentials, second)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := validTestFile()
			tt.mutate(document)
			if err := document.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want rejection")
			}
		})
	}
}

func TestLoadFileStrictJSONAndPermissions(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "credentials.json")
	if err := WriteFileAtomic(validPath, validTestFile()); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	if _, err := LoadFile(validPath); err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	if err := os.Chmod(validPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(validPath); err != nil {
		t.Fatalf("LoadFile() rejected Docker-secret-style read bits: %v", err)
	}
	if err := os.Chmod(validPath, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(validPath); err == nil {
		t.Fatal("LoadFile() accepted group-writable file")
	}

	unknownPath := filepath.Join(directory, "unknown.json")
	unknown := `{"version":1,"credentials":[],"unexpected":true}`
	if err := os.WriteFile(unknownPath, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(unknownPath); err == nil {
		t.Fatal("LoadFile() accepted unknown field")
	}

	multiplePath := filepath.Join(directory, "multiple.json")
	if err := os.WriteFile(multiplePath, []byte(`{"version":1,"credentials":[]} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(multiplePath); err == nil {
		t.Fatal("LoadFile() accepted multiple JSON values")
	}

	duplicatePath := filepath.Join(directory, "duplicate.json")
	duplicate := `{"version":1,"version":1,"credentials":[]}`
	if err := os.WriteFile(duplicatePath, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(duplicatePath); err == nil {
		t.Fatal("LoadFile() accepted duplicate JSON field")
	}
}

func TestVersionOneDailyQuotaDistinguishesOmittedAndExplicitZero(t *testing.T) {
	document := validTestFile()
	document.Credentials[0].MaxTransactionsPerDay = 77
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()

	omittedPath := filepath.Join(directory, "omitted.json")
	omitted := bytes.Replace(encoded, []byte(`,"max_transactions_per_day":77`), nil, 1)
	if bytes.Equal(omitted, encoded) {
		t.Fatal("test fixture did not remove max_transactions_per_day")
	}
	if err := os.WriteFile(omittedPath, omitted, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(omittedPath)
	if err != nil {
		t.Fatalf("legacy omitted quota was rejected: %v", err)
	}
	if loaded.Credentials[0].MaxTransactionsPerDay != DefaultMaxTransactionsPerDay {
		t.Fatalf("legacy quota = %d, want %d", loaded.Credentials[0].MaxTransactionsPerDay, DefaultMaxTransactionsPerDay)
	}

	explicitZeroPath := filepath.Join(directory, "zero.json")
	explicitZero := bytes.Replace(encoded, []byte(`"max_transactions_per_day":77`), []byte(`"max_transactions_per_day":0`), 1)
	if err := os.WriteFile(explicitZeroPath, explicitZero, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(explicitZeroPath); err == nil {
		t.Fatal("explicit zero daily quota was accepted")
	}
}

func TestLoadFileRejectsSymlinkNonRegularAndOversize(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := WriteFileAtomic(target, validTestFile()); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := LoadFile(symlink); err == nil {
		t.Fatal("LoadFile() accepted symlink")
	}
	if _, err := LoadFile(directory); err == nil {
		t.Fatal("LoadFile() accepted directory")
	}

	oversize := filepath.Join(directory, "oversize.json")
	if err := os.WriteFile(oversize, make([]byte, MaxFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(oversize); err == nil {
		t.Fatal("LoadFile() accepted oversized file")
	}
}

func TestStoreAuthenticateAndDefensiveCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	document := validTestFile()
	if err := WriteFileAtomic(path, document); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	credential := document.Credentials[0]

	if _, err := store.Authenticate(testToken, credential.NotBefore.Add(-time.Second)); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("pre-not_before error = %v", err)
	}
	got, err := store.Authenticate(testToken, credential.NotBefore)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.ID != testTokenID {
		t.Fatalf("credential ID = %q", got.ID)
	}
	if _, err := store.Authenticate(testToken, credential.ExpiresAt); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("at-expiry error = %v", err)
	}
	if _, err := store.Authenticate("wrong-token", credential.NotBefore); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("wrong-token error = %v", err)
	}

	got.Accounts[0] = "mutated"
	again, err := store.Authenticate(testToken, credential.NotBefore)
	if err != nil || again.Accounts[0] != testAccount {
		t.Fatalf("store snapshot was mutated: %#v, %v", again, err)
	}
}

func TestReloadPublishesAtomicallyAndPreservesOldOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	document := validTestFile()
	if err := WriteFileAtomic(path, document); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := document.Credentials[0].NotBefore.Add(time.Hour)

	replacement := validTestFile()
	replacement.Credentials[0].TokenSHA256 = HashToken(secondToken)
	if err := WriteFileAtomic(path, replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if _, err := store.Authenticate(testToken, now); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("old token remained valid: %v", err)
	}
	if _, err := store.Authenticate(secondToken, now); err != nil {
		t.Fatalf("new token failed: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"credentials":[{"unknown":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("Reload() accepted invalid replacement")
	}
	if _, err := store.Authenticate(secondToken, now); err != nil {
		t.Fatalf("invalid reload replaced prior snapshot: %v", err)
	}
}

func TestStoreConcurrentReloadAndAuthenticate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	document := validTestFile()
	if err := WriteFileAtomic(path, document); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := document.Credentials[0].NotBefore.Add(time.Hour)

	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 50; j++ {
				_, _ = store.Authenticate(testToken, now)
				_ = store.List()
			}
		}()
	}
	for i := 0; i < 5; i++ {
		if err := store.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}
