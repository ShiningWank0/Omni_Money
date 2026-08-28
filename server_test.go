//go:build server

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"omni_money/backend/config"
	"omni_money/backend/control"
	"omni_money/backend/securedb"
	"omni_money/backend/serverauth"
)

func TestExecuteShutdownUsesOwnershipOrder(t *testing.T) {
	var order []string
	record := func(name string) { order = append(order, name) }
	err := executeShutdown(time.Second, shutdownHooks{
		httpShutdown: func(context.Context) error {
			record("http")
			return nil
		},
		httpClose: func() error {
			record("force-http")
			return nil
		},
		sessionsClose: func(context.Context) error {
			record("sessions")
			return nil
		},
		vaultsClose: func(context.Context) error {
			record("vaults")
			return nil
		},
		controlClose: func() error {
			record("control")
			return nil
		},
		setupDestroy: func() { record("setup") },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http", "sessions", "vaults", "control", "setup"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestExecuteShutdownContinuesAfterErrors(t *testing.T) {
	var order []string
	record := func(name string) { order = append(order, name) }
	err := executeShutdown(time.Second, shutdownHooks{
		httpShutdown: func(context.Context) error {
			record("http")
			return errors.New("drain failed")
		},
		httpClose: func() error {
			record("force-http")
			return errors.New("force failed")
		},
		sessionsClose: func(context.Context) error {
			record("sessions")
			return nil
		},
		vaultsClose: func(context.Context) error {
			record("vaults")
			return errors.New("vault failed")
		},
		controlClose: func() error {
			record("control")
			return errors.New("control failed")
		},
		setupDestroy: func() { record("setup") },
	})
	if err == nil {
		t.Fatal("shutdown errors were dropped")
	}
	for _, fragment := range []string{"drain failed", "force failed", "vault failed", "control failed"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("shutdown error %q omits %q", err, fragment)
		}
	}
	want := []string{"http", "force-http", "sessions", "vaults", "control", "setup"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestPathWithinRequiresRealDescendantSpelling(t *testing.T) {
	if !pathWithin("/srv/omni", "/srv/omni/vaults") {
		t.Fatal("descendant rejected")
	}
	if pathWithin("/srv/omni", "/srv/omni-other/vaults") {
		t.Fatal("sibling prefix accepted")
	}
	if pathWithin("/srv/omni", "/srv/omni/../outside") {
		t.Fatal("cleaned escape accepted")
	}
}

func TestResolvedPathWithinFollowsParentSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "control.key")
	if err := os.WriteFile(secret, []byte("not-a-real-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	inside, err := resolvedPathWithin(root, filepath.Join(alias, "control.key"))
	if err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("secret reached through a parent symlink escaped containment detection")
	}
}

func TestExecuteShutdownContinuesAfterSessionTimeout(t *testing.T) {
	var order []string
	err := executeShutdown(time.Millisecond, shutdownHooks{
		sessionsClose: func(ctx context.Context) error {
			order = append(order, "sessions")
			<-ctx.Done()
			return ctx.Err()
		},
		vaultsClose: func(context.Context) error {
			order = append(order, "vaults")
			return nil
		},
		controlClose: func() error {
			order = append(order, "control")
			return nil
		},
		setupDestroy: func() { order = append(order, "setup") },
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("session timeout error = %v", err)
	}
	want := []string{"sessions", "vaults", "control", "setup"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order after session timeout = %v, want %v", order, want)
	}
}

func TestOpenVaultSessionFailsClosedAndDestroysKey(t *testing.T) {
	raw := make([]byte, securedb.RawKeySize)
	for index := range raw {
		raw[index] = 0x7f
	}
	key, err := securedb.NewRawKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	callback := openVaultSession(nil, nil)
	if _, err := callback(control.UserSummary{}, "", &key); !errors.Is(err, serverauth.ErrServiceUnavailable) {
		t.Fatalf("nil dependencies returned %v", err)
	}
	for index, value := range key {
		if value != 0 {
			t.Fatalf("key byte %d was retained", index)
		}
	}
}

func TestInitialSetupAuthorizerIgnoredAfterBootstrap(t *testing.T) {
	setup, err := initialSetupAuthorizer(true, "/path/that/must/not/be/read")
	if err != nil {
		t.Fatalf("bootstrapped server read obsolete setup token: %v", err)
	}
	if setup != nil {
		t.Fatal("bootstrapped server retained a setup authorizer")
	}

	if _, err := initialSetupAuthorizer(false, ""); err == nil || !strings.Contains(err.Error(), "INITIAL_ADMIN_SETUP_TOKEN_FILE") {
		t.Fatalf("fresh server accepted no setup token: %v", err)
	}
}

func TestValidateProductionListenerAcceptsEffectiveLoopback(t *testing.T) {
	serverConfig := config.ServerConfig{
		ListenHost: "0.0.0.0",
		WebTransport: config.WebTransportConfig{
			ListenHost:   "0.0.0.0",
			ExternalHost: "127.0.0.1",
		},
	}
	if err := validateProductionListener(serverConfig); err != nil {
		t.Fatalf("effective loopback rejected: %v", err)
	}
}
