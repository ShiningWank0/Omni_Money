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

func TestComposeAppliesRuntimeSandbox(t *testing.T) {
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
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose runtime sandbox is missing %q", required)
		}
	}

	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dockerfile), "/app/snapshots") {
		t.Error("Dockerfile must not create the unused root-filesystem snapshot directory")
	}
}
