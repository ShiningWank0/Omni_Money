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
	if !strings.HasPrefix(images[1], "golang:1.25.12-alpine3.24@sha256:") {
		t.Errorf("backend builder must stay aligned with Go 1.25.12 on Alpine 3.24: %s", images[1])
	}
	if !strings.HasPrefix(images[2], "alpine:3.24.1@sha256:") {
		t.Errorf("runtime must use the reviewed Alpine 3.24.1 image: %s", images[2])
	}
}
