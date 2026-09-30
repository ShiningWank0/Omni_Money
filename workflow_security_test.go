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
			trimmed = strings.TrimPrefix(trimmed, "- ")
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
		`WAILS_VERSION="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v2)"`,
		"go-version-file: go.mod",
		"node-version-file: .node-version",
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
	for _, forbidden := range []string{"cmd/wails@latest", "WAILS_VERSION:", `xattr -cr`} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow contains forbidden pattern %q", forbidden)
		}
	}
}

func TestDockerPullRequestsCannotPublishImages(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(".github", "workflows", "release-docker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	start := strings.Index(workflow, "\n  build-check:\n")
	end := strings.Index(workflow, "\n  prepare:\n")
	if start < 0 || end <= start {
		t.Fatal("Docker PR build check is missing")
	}
	check := workflow[start:end]
	for _, required := range []string{"if: github.event_name == 'pull_request'", "contents: read", "push: false", "linux/amd64", "linux/arm64", "provenance: mode=max", "sbom: true"} {
		if !strings.Contains(check, required) {
			t.Errorf("Docker PR check is missing %q", required)
		}
	}
	for _, forbidden := range []string{"push: true", "login-action@", ": write", "secrets.", "attest-build-provenance@"} {
		if strings.Contains(check, forbidden) {
			t.Errorf("Docker PR check must not publish: %q", forbidden)
		}
	}
	prepare := workflow[end:strings.Index(workflow, "\n  build:\n")]
	if !strings.Contains(prepare, "if: github.event_name != 'pull_request'") {
		t.Error("release prepare must run for release pushes and rehearsals, never for PRs")
	}
	for _, action := range []string{"docker/setup-buildx-action", "docker/build-push-action"} {
		pins := regexp.MustCompile(regexp.QuoteMeta(action)+`@([0-9a-f]{40})`).FindAllStringSubmatch(workflow, -1)
		if len(pins) < 2 {
			t.Fatalf("%s must be exercised in PR and release builds", action)
		}
		for _, pin := range pins[1:] {
			if pin[1] != pins[0][1] {
				t.Errorf("%s must use the same pin in PR and release builds", action)
			}
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
		"build-args: VERSION=${{ env.APP_VERSION }}",
		"push-by-digest=true",
		`image-ref: ${{ needs.prepare.outputs.image }}@${{ steps.build.outputs.digest }}`,
		"TRIVY_PLATFORM: ${{ matrix.platform }}",
		"actions/attest-build-provenance@",
		"subject-digest: ${{ steps.publish.outputs.digest }}",
		"id-token: write",
		"attestations: write",
		"gh attestation verify",
		"--signer-workflow",
		"--source-ref",
		"--deny-self-hosted-runners",
		"--dry-run",
		"workflow_dispatch:",
		"rehearsal-staging",
		"if: github.event_name != 'workflow_dispatch'",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Docker release workflow is missing version or release-assurance control %q", required)
		}
	}
	if strings.Contains(workflow, "omni-money:release-scan") {
		t.Error("Docker release must scan the pushed digest instead of a separately rebuilt candidate")
	}
	if strings.Contains(workflow, "tags: ${{ needs.prepare.outputs.image }}") {
		t.Error("per-architecture images must be pushed by digest without release tags")
	}
	buildStart := strings.Index(workflow, "\n  build:\n")
	manifestStart := strings.Index(workflow, "\n  manifest:\n")
	if buildStart < 0 || manifestStart <= buildStart {
		t.Fatal("Docker release workflow build/manifest jobs are missing or out of order")
	}
	buildJob := workflow[buildStart:manifestStart]
	for _, forbidden := range []string{"push: true", "tags:"} {
		if strings.Contains(buildJob, forbidden) {
			t.Errorf("release build job must push by digest only; found %q", forbidden)
		}
	}
	attest := strings.Index(workflow, "Attest staged image provenance")
	verify := strings.Index(workflow, "Verify provenance before publishing release tags")
	publish := strings.Index(workflow, "Publish verified release tags")
	if attest < 0 || verify < 0 || publish < 0 || !(attest < verify && verify < publish) {
		t.Error("the staged attestation must be verified before any release tag is published")
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
	const command = "node scripts/security-reports.mjs govulncheck"
	if count := strings.Count(string(contents), command); count != 1 {
		t.Fatalf("CI must invoke the Go vulnerability report collector exactly once, got %d", count)
	}
	reporter, err := os.ReadFile(filepath.Join("scripts", "security-reports.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	const scanner = "golang.org/x/vuln/cmd/govulncheck@v1.6.0"
	if count := strings.Count(string(reporter), scanner); count != 1 {
		t.Fatalf("report collector must retain the pinned Go vulnerability scanner, got %d", count)
	}
}
