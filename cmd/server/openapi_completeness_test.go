// Package main: openapi completeness gate.
//
// Phase 1 of issue #1670: enforce that every `/api/*` route registered via
// `*.HandleFunc("/api/...", ...)` in cmd/server/*.go (non-_test) has a
// corresponding entry in the OpenAPI spec map declared in
// cmd/server/openapi.go (the `routeDescriptions` map literal).
//
// Ratchet pattern:
//   - On first land, the spec covers only a subset of handlers. The full
//     missing list is "frozen" into cmd/server/openapi_known_gaps.json.
//   - The test FAILS when a NEW HandleFunc("/api/...") is added without
//     either (a) adding the route to openapi.go, or (b) appending it to
//     openapi_known_gaps.json.
//   - It also FAILS if any entry in openapi_known_gaps.json is now covered
//     by openapi.go (the allowlist must shrink as Phase 2 backfills land).
//
// Phase 2 (the actual backfill of ~18 routes into openapi.go) is tracked
// in a separate issue per the triage on #1670. This file is the gate
// that ensures the gap does not GROW while Phase 2 is in progress.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const knownGapsFile = "openapi_known_gaps.json"

// collectHandlerRoutes walks every non-_test .go file in cmd/server/ and
// returns the set of string-literal first args to any `*.HandleFunc(...)`
// or `*.Handle(...)` call whose value starts with "/api/".
//
// Both forms are used in cmd/server/routes.go: bare handlers use
// `r.HandleFunc("/api/...", fn)`, while handlers wrapped in auth
// middleware use `r.Handle("/api/...", wrapped).Methods("...")`. The
// completeness gate MUST consider both — anything less lets the
// gorilla-style chained routes slip past the ratchet.
func collectHandlerRoutes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{} // route -> "file:line"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/server dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			if sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle" {
				return true
			}
			if len(call.Args) < 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if !strings.HasPrefix(v, "/api/") {
				return true
			}
			pos := fset.Position(lit.Pos())
			if _, exists := out[v]; !exists {
				out[v] = pos.String()
			}
			return true
		})
	}
	return out
}

// strconvUnquote strips Go string-literal quoting without pulling strconv
// into the import list (keeps the file's imports lean).
func strconvUnquote(s string) (string, error) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

// collectSpecRoutes returns the set of "/api/..." paths declared in the
// routeDescriptions() map in openapi.go. Keys are "METHOD /path"; we strip
// the method and take just the path.
func collectSpecRoutes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for k := range routeDescriptions() {
		// key shape: "GET /api/foo" — split once on space.
		idx := strings.IndexByte(k, ' ')
		if idx < 0 {
			continue
		}
		path := k[idx+1:]
		if strings.HasPrefix(path, "/api/") {
			out[path] = true
		}
	}
	return out
}

// loadKnownGaps returns the allowlist of currently-known-missing routes.
// Missing file is treated as an empty allowlist (the initial RED state).
func loadKnownGaps(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	b, err := os.ReadFile(knownGapsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read %s: %v", knownGapsFile, err)
	}
	var payload struct {
		Routes []string `json:"routes"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("parse %s: %v", knownGapsFile, err)
	}
	for _, r := range payload.Routes {
		out[r] = true
	}
	return out
}

// TestOpenAPICompleteness is the ratchet gate for issue #1670.
func TestOpenAPICompleteness(t *testing.T) {
	handlers := collectHandlerRoutes(t)
	spec := collectSpecRoutes(t)
	gaps := loadKnownGaps(t)

	// 1. Find routes registered via HandleFunc but missing from spec AND
	//    not in the allowlist — these are new regressions.
	var newMissing []string
	for route := range handlers {
		if spec[route] {
			continue
		}
		if gaps[route] {
			continue
		}
		newMissing = append(newMissing, route)
	}
	sort.Strings(newMissing)

	// 2. Find allowlist entries that are now covered by the spec — the
	//    allowlist must shrink, not stay stale.
	var stale []string
	for route := range gaps {
		if spec[route] {
			stale = append(stale, route)
		}
	}
	sort.Strings(stale)

	// 3. (Diagnostic only) Total current gap count, for visibility.
	var currentGaps []string
	for route := range handlers {
		if !spec[route] {
			currentGaps = append(currentGaps, route)
		}
	}
	sort.Strings(currentGaps)
	t.Logf("openapi spec covers %d/%d /api/ handler routes; %d in allowlist; %d total gaps remain",
		len(handlers)-len(currentGaps), len(handlers), len(gaps), len(currentGaps))

	if len(newMissing) > 0 {
		t.Errorf("\n%d /api/ route(s) registered in cmd/server but NOT in openapi.go spec AND NOT in %s:\n  - %s\n\nFix one of:\n  a) Add the route to routeDescriptions() in cmd/server/openapi.go (preferred — Phase 2 of #1670)\n  b) Append the route to cmd/server/%s (ratchet — only if Phase 2 backfill is genuinely deferred)\n",
			len(newMissing), knownGapsFile, strings.Join(newMissing, "\n  - "), knownGapsFile)
	}

	if len(stale) > 0 {
		t.Errorf("\n%d route(s) in %s are now covered by openapi.go and must be REMOVED from the allowlist (ratchet must shrink):\n  - %s\n",
			len(stale), knownGapsFile, strings.Join(stale, "\n  - "))
	}
}
