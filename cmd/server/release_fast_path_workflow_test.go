// Tests for issue #1677: release fast-path workflow.
//
// These tests gate the workflow config (not Go code) by parsing the YAML
// files as text and asserting structural invariants. They follow the same
// "config gate" pattern as openapi_completeness_test.go.
//
//  1. .github/workflows/release-fast-path.yml MUST exist and own the
//     push.tags trigger for v-tags, with the two execution branches
//     (re-tag-via-crane on SHA match, fallback to deploy.yml otherwise).
//  2. .github/workflows/deploy.yml MUST NOT trigger on push.tags any
//     more — the fast-path workflow owns tag pushes to avoid double-fire.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	fastPathWorkflowRel = "../../.github/workflows/release-fast-path.yml"
	deployWorkflowRel   = "../../.github/workflows/deploy.yml"
)

func TestReleaseFastPathWorkflowExists(t *testing.T) {
	abs, _ := filepath.Abs(fastPathWorkflowRel)
	raw, err := os.ReadFile(fastPathWorkflowRel)
	if err != nil {
		t.Fatalf("issue #1677: release-fast-path.yml missing at %s: %v", abs, err)
	}
	src := string(raw)

	// Trigger: push.tags matching semver v-tags.
	triggerRe := regexp.MustCompile(`(?m)^\s*tags:\s*\[\s*['"]v\[0-9\]\+\.\[0-9\]\+\.\[0-9\]\+['"]\s*\]`)
	if !triggerRe.MatchString(src) {
		t.Errorf("release-fast-path.yml: missing required push.tags trigger 'v[0-9]+.[0-9]+.[0-9]+'")
	}

	// Permissions: needs packages:write to re-tag in GHCR, contents:read for
	// checkout, and actions:write so the fallback `gh workflow run deploy.yml`
	// dispatch is allowed (issue #1702 — fallback returned 403 without it).
	for _, perm := range []string{"packages: write", "contents: read", "actions: write"} {
		if !strings.Contains(src, perm) {
			t.Errorf("release-fast-path.yml: missing required permission %q", perm)
		}
	}

	// Required markers covering both execution branches:
	//   - re-tag path: install crane, read :edge revision label, apply new tags
	//   - fallback path: dispatch the existing deploy.yml pipeline
	required := []string{
		"imjasonh/setup-crane",              // crane install action
		"org.opencontainers.image.revision", // label inspected on :edge
		"ghcr.io/kpa-clawbot/corescope",     // image ref
		":edge",                             // source tag we copy from
		"crane tag",                         // metadata-only retag
		"workflow run deploy.yml",           // fallback dispatch
	}
	for _, need := range required {
		if !strings.Contains(src, need) {
			t.Errorf("release-fast-path.yml: missing required marker %q (issue #1677 fix-path)", need)
		}
	}
}

func TestDeployWorkflowNoLongerTriggersOnTags(t *testing.T) {
	raw, err := os.ReadFile(deployWorkflowRel)
	if err != nil {
		t.Fatalf("deploy.yml: %v", err)
	}
	// Extract the top-level `on:` block: from `^on:` up to the next
	// top-level YAML key (line that starts in column 0 with a letter).
	blockRe := regexp.MustCompile(`(?ms)^on:\s*\n(.*?)\n([a-zA-Z][a-zA-Z0-9_-]*:)`)
	m := blockRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("deploy.yml: could not locate top-level on: block")
	}
	onBlock := m[1]
	if regexp.MustCompile(`(?m)^\s*tags:\s*\[`).MatchString(onBlock) {
		t.Errorf("deploy.yml: on: block still triggers on push.tags; the fast-path workflow (release-fast-path.yml) must own tag pushes to avoid double-fire (issue #1677).\non-block was:\n%s", onBlock)
	}
}
