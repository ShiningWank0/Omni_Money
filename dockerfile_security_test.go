package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var pinnedImagePattern = regexp.MustCompile(`^[A-Za-z0-9./:_-]+@sha256:[0-9a-f]{64}$`)

func TestDockerBaseImagesUsePinnedSupportedVersions(t *testing.T) {
	contents, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	var images []string
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "FROM" {
			continue
		}
		if len(fields) < 2 || !pinnedImagePattern.MatchString(fields[1]) {
			t.Errorf("Dockerfile:%d base image must use tag@sha256: %s", lineNumber+1, line)
			continue
		}
		images = append(images, fields[1])
	}
	if len(images) != 3 {
		t.Fatalf("Dockerfile has %d pinned stages, want 3", len(images))
	}
	if !strings.HasPrefix(images[1], "golang:1.26.7-alpine@sha256:") {
		t.Errorf("backend builder must stay aligned with Go 1.26.7 on Alpine: %s", images[1])
	}
	if !strings.HasPrefix(images[2], "alpine:3.24@sha256:") {
		t.Errorf("runtime must use the reviewed Alpine 3.24 image: %s", images[2])
	}
}

func TestComposeAppliesRuntimeSandboxAndMultiVaultBoundaries(t *testing.T) {
	contents, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(contents)
	for _, required := range []string{
		"read_only: true",
		"cap_drop:\n      - ALL",
		"security_opt:\n      - no-new-privileges:true",
		`cpus: "${OMNI_CPU_LIMIT:-2.0}"`,
		`mem_limit: "${OMNI_MEMORY_LIMIT:-1g}"`,
		`pids_limit: "${OMNI_PIDS_LIMIT:-256}"`,
		"TMPDIR: /tmp",
		"SQLITE_TMPDIR: /tmp",
		"target: /app/data\n        read_only: false",
		"type: tmpfs\n        target: /tmp",
		`size: "${OMNI_TMPFS_SIZE:-128m}"`,
		"mode: 0o1777",
		"CONTROL_DB_PATH: /app/data/control/omni_control.db",
		"CONTROL_DB_ENCRYPTION_KEY_FILE: /run/secrets/omni_control_database_key",
		"VAULT_ROOT: /app/data/vaults",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose runtime sandbox is missing %q", required)
		}
	}
	if strings.Contains(compose, "INITIAL_ADMIN_SETUP_TOKEN_FILE") || strings.Contains(compose, "omni_initial_admin_setup_token") {
		t.Error("base compose must not retain the one-time initial administrator setup token")
	}
	bootstrapContents, err := os.ReadFile("compose.bootstrap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := string(bootstrapContents)
	for _, required := range []string{
		"INITIAL_ADMIN_SETUP_TOKEN_FILE: /run/secrets/omni_initial_admin_setup_token",
		"source: omni_initial_admin_setup_token",
		"target: omni_initial_admin_setup_token",
	} {
		if !strings.Contains(bootstrap, required) {
			t.Errorf("bootstrap compose overlay is missing %q", required)
		}
	}

	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dockerfile), "/app/snapshots") {
		t.Error("Dockerfile must not create the unused root-filesystem snapshot directory")
	}
	for _, forbidden := range []string{"AUTH_PASSWORD_HASH", "DB_PATH=/app/data/omni_money.db", "AI_HOST_IP", "/omni-totp"} {
		if strings.Contains(string(dockerfile), forbidden) || strings.Contains(compose, forbidden) {
			t.Errorf("multi-user runtime contains legacy/unscoped setting %q", forbidden)
		}
	}
}
