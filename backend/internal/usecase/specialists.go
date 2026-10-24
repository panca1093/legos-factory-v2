package usecase

import (
	"path"
	"strings"
)

// Specialist Inspectors. Selection is deterministic from the changed files:
// Testing always reviews; Security, Backend, Frontend and UI/UX join when
// the diff touches their area; Architecture joins for changes spanning many
// top-level areas or core contracts. At most maxSpecialists run, in this
// priority order, so inspection cost stays bounded.

const (
	SpecialtyTesting      = "testing"
	SpecialtySecurity     = "security"
	SpecialtyBackend      = "backend"
	SpecialtyFrontend     = "frontend"
	SpecialtyUIUX         = "ui_ux"
	SpecialtyArchitecture = "architecture"

	maxSpecialists = 3
)

var (
	backendExt  = map[string]bool{".go": true, ".py": true, ".java": true, ".kt": true, ".rs": true, ".rb": true, ".sql": true, ".php": true, ".cs": true}
	frontendExt = map[string]bool{".vue": true, ".tsx": true, ".jsx": true, ".svelte": true}
	uiExt       = map[string]bool{".vue": true, ".css": true, ".scss": true, ".html": true, ".svg": true, ".tsx": true, ".jsx": true}
	securityKey = []string{"auth", "token", "secret", "crypt", "password", "passwd", "session", "oidc", "oauth", "permission", "rbac", "credential", "jwt", ".env", "dockerfile"}
	coreKey     = []string{"migration", "schema", "ports", "interface", "contract", "openapi", "proto"}
)

// SelectSpecialists returns the specialties to run for files, each with the
// reason it was chosen.
func SelectSpecialists(files []string) []SpecialistPick {
	reasons := map[string]string{SpecialtyTesting: "always reviews tests against the acceptance criteria"}
	top := map[string]bool{}
	for _, f := range files {
		lower := strings.ToLower(f)
		ext := path.Ext(lower)
		dir, _, _ := strings.Cut(f, "/")
		top[dir] = true
		note := func(s, why string) {
			if _, ok := reasons[s]; !ok {
				reasons[s] = why + ": " + f
			}
		}
		if hasAny(lower, securityKey) {
			note(SpecialtySecurity, "touches security-sensitive code")
		}
		if backendExt[ext] {
			note(SpecialtyBackend, "changes backend code")
		}
		if frontendExt[ext] || (ext == ".ts" && strings.Contains(lower, "src/")) {
			note(SpecialtyFrontend, "changes frontend code")
		}
		if uiExt[ext] || strings.Contains(lower, "component") || strings.Contains(lower, "view") {
			note(SpecialtyUIUX, "changes user-facing UI")
		}
		if hasAny(lower, coreKey) {
			note(SpecialtyArchitecture, "changes a core contract")
		}
	}
	if len(top) >= 3 {
		if _, ok := reasons[SpecialtyArchitecture]; !ok {
			reasons[SpecialtyArchitecture] = "change spans many areas"
		}
	}
	var picks []SpecialistPick
	for _, s := range []string{SpecialtyTesting, SpecialtySecurity, SpecialtyBackend, SpecialtyFrontend, SpecialtyUIUX, SpecialtyArchitecture} {
		if why, ok := reasons[s]; ok && len(picks) < maxSpecialists {
			picks = append(picks, SpecialistPick{Specialty: s, Reason: why})
		}
	}
	return picks
}

type SpecialistPick struct {
	Specialty string `json:"specialty"`
	Reason    string `json:"reason"`
}

func hasAny(s string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

var specialtyFocus = map[string]string{
	SpecialtyTesting:      "Do the tests verify the acceptance criteria through behavior rather than mocks or implementation details? Are edge cases covered?",
	SpecialtySecurity:     "Authentication, authorization, secret handling, injection, unsafe input handling and data exposure.",
	SpecialtyBackend:      "Correctness, error handling, concurrency, resource cleanup and API contract changes in server-side code.",
	SpecialtyFrontend:     "Component state, data fetching, error states, type safety and regressions in client code.",
	SpecialtyUIUX:         "Usability, accessibility (labels, focus, contrast), responsive layout and copy clarity.",
	SpecialtyArchitecture: "Module boundaries, dependency direction, migrations and long-term maintainability.",
}
