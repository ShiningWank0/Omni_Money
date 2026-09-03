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
		"prepare:",
		"name: Validate and test release",
		"build:",
		"needs: prepare",
		"attestations: write",
		"artifact-metadata: write",
		"release:",
		"needs: [prepare, build, attest]",
		"actions: read",
		"contents: write",
		"WAILS_VERSION: v2.11.0",
		"VITE_APP_VERSION: ${{ needs.prepare.outputs.version }}",
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

func TestDockerReleasePassesVersionBuildArg(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(".github", "workflows", "release-docker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, required := range []string{
		`--build-arg "VERSION=$APP_VERSION"`,
		"build-args: VERSION=${{ env.APP_VERSION }}",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Docker release workflow is missing version propagation %q", required)
		}
	}
}

func TestDesktopReleaseSharesPreparedVersionWithWails(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(".github", "workflows", "release-desktop.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	buildStart := strings.Index(workflow, "\n  build:\n")
	attestStart := strings.Index(workflow, "\n  attest:\n")
	if buildStart < 0 || attestStart <= buildStart {
		t.Fatal("release workflow build job is missing or out of order")
	}
	buildJob := workflow[buildStart:attestStart]
	for _, required := range []string{
		"APP_VERSION: ${{ needs.prepare.outputs.version }}",
		"VITE_APP_VERSION: ${{ needs.prepare.outputs.version }}",
		`-ldflags "-X main.version=${APP_VERSION}"`,
	} {
		if !strings.Contains(buildJob, required) {
			t.Errorf("desktop build job is missing shared prepared version wiring %q", required)
		}
	}
}

func TestCIUsesPinnedGoVulnerabilityScanner(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const command = "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."
	if count := strings.Count(string(contents), command); count != 1 {
		t.Fatalf("CI must run the pinned Go vulnerability scanner exactly once, got %d", count)
	}
}
