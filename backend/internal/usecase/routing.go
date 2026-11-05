package usecase

import (
	"path"
	"sort"
	"strings"

	"legos-factory/backend/internal/model"
)

var canonicalSpecialties = []string{SpecialtyTesting, SpecialtySecurity, SpecialtyBackend, SpecialtyFrontend, SpecialtyUIUX, SpecialtyArchitecture}

// MatchReviewers resolves declared reviewer routes against the changed files.
// Testing always reviews; the result is capped at maxSpecialists.
func MatchReviewers(routes []model.ReviewerRoute, files []string) []SpecialistPick {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	reasons := map[string]string{}
	for _, f := range sorted {
		f = strings.TrimPrefix(path.Clean(strings.ReplaceAll(f, "\\", "/")), "./")
		for _, r := range routes {
			if !routeMatches(r.Paths, f) {
				continue
			}
			why := "route " + strings.Join(r.Paths, ", ") + " matched " + f
			for _, name := range r.Reviewers {
				if _, ok := reasons[name]; !ok {
					reasons[name] = why
				}
			}
		}
	}
	reasons[SpecialtyTesting] = "always reviews tests against the acceptance criteria"

	var names []string
	known := map[string]bool{}
	for _, s := range canonicalSpecialties {
		known[s] = true
		if _, ok := reasons[s]; ok {
			names = append(names, s)
		}
	}
	var unknown []string
	for name := range reasons {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	names = append(names, unknown...)
	if len(names) > maxSpecialists {
		names = names[:maxSpecialists]
	}
	picks := make([]SpecialistPick, 0, len(names))
	for _, n := range names {
		picks = append(picks, SpecialistPick{Specialty: n, Reason: reasons[n]})
	}
	return picks
}

// ProposeRoutes expresses the SelectSpecialists heuristic as routes, one
// pattern per (top-level dir, extension), for a human to confirm and edit.
// A root-level pattern like "*.go" is a base-name pattern and matches at any depth.
func ProposeRoutes(files []string) []model.ReviewerRoute {
	type key struct{ dir, token string }
	byKey := map[key]map[string]bool{}
	for _, f := range files {
		f = strings.TrimPrefix(path.Clean(strings.ReplaceAll(f, "\\", "/")), "./")
		dir := ""
		if i := strings.Index(f, "/"); i >= 0 {
			dir = f[:i]
		}
		token := "*" + path.Ext(f)
		if path.Ext(f) == "" {
			token = path.Base(f)
		}
		for _, p := range SelectSpecialists([]string{f}) {
			if p.Specialty == SpecialtyTesting {
				continue
			}
			k := key{dir, token}
			if byKey[k] == nil {
				byKey[k] = map[string]bool{}
			}
			byKey[k][p.Specialty] = true
		}
	}

	type group struct {
		dir       string
		paths     []string
		reviewers []string
	}
	groups := map[string]*group{}
	var keys []key
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].dir != keys[j].dir {
			return keys[i].dir < keys[j].dir
		}
		return keys[i].token < keys[j].token
	})
	var order []string
	for _, k := range keys {
		var revs []string
		for _, s := range canonicalSpecialties {
			if byKey[k][s] {
				revs = append(revs, s)
			}
		}
		pattern := k.token
		if k.dir != "" {
			pattern = k.dir + "/**/" + k.token
		}
		id := k.dir + "\x00" + strings.Join(revs, ",")
		g, ok := groups[id]
		if !ok {
			g = &group{dir: k.dir, reviewers: revs}
			groups[id] = g
			order = append(order, id)
		}
		g.paths = append(g.paths, pattern)
	}
	routes := make([]model.ReviewerRoute, 0, len(order))
	for _, id := range order {
		routes = append(routes, model.ReviewerRoute{Paths: groups[id].paths, Reviewers: groups[id].reviewers})
	}
	return routes
}

func routeMatches(patterns []string, file string) bool {
	matched := false
	for _, p := range patterns {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if globMatch(neg, file) {
				return false
			}
			continue
		}
		if globMatch(p, file) {
			matched = true
		}
	}
	return matched
}

func globMatch(pattern, file string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if !strings.Contains(pattern, "/") {
		pattern = "**/" + pattern
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
