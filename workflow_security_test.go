package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var actionCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestGitHubActionsArePinnedAndCheckoutDropsCredentials(t *testing.T) {
	workflowPaths, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(workflowPaths) == 0 {
		t.Fatal("no GitHub Actions workflows found")
	}

	for _, workflowPath := range workflowPaths {
		contents, err := os.ReadFile(workflowPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(contents), "\n")
		for index, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "uses:") {
				continue
			}
			action := strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:"))
			if commentIndex := strings.Index(action, " #"); commentIndex >= 0 {
				action = action[:commentIndex]
			}
			separator := strings.LastIndex(action, "@")
			if separator <= 0 || !actionCommitPattern.MatchString(action[separator+1:]) {
				t.Errorf("%s:%d action is not pinned to a full commit SHA: %s", workflowPath, index+1, trimmed)
			}
			if !strings.HasPrefix(action, "actions/checkout@") {
				continue
			}
			end := min(index+7, len(lines))
			if !strings.Contains(strings.Join(lines[index+1:end], "\n"), "persist-credentials: false") {
				t.Errorf("%s:%d checkout must disable persisted credentials", workflowPath, index+1)
			}
		}
	}
}

func TestDesktopReleaseUsesLeastPrivilegeAndReproducibleTools(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(".github", "workflows", "release-desktop.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, required := range []string{
		"permissions: {}",
		"prepare:\n    permissions:\n      contents: read",
		"build:\n    needs: prepare",
		"attestations: write\n      artifact-metadata: write",
		"release:\n    needs: [prepare, build, attest]",
		"actions: read\n      contents: write",
		"WAILS_VERSION: v2.11.0",
		"github.com/wailsapp/wails/v2/cmd/wails@$WAILS_VERSION",
		"SHA256SUMS",
		"actions/attest@",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing security control %q", required)
		}
	}
	if strings.Count(workflow, "contents: read") < 3 {
		t.Error("prepare, build, and attest jobs must retain read-only contents access")
	}
	for _, forbidden := range []string{"cmd/wails@latest", `xattr -cr`} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow contains forbidden pattern %q", forbidden)
		}
	}
}
