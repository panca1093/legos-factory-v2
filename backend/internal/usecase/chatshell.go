package usecase

import (
	"path"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

// ReadOnlyCommand reports whether a repo chat may run a shell command without asking: it must be one plain command
// (no chaining, pipes, redirection or substitution) whose argv is on the read-only allowlist and names no path
// outside the project or secret file. Anything else goes to the user for approval.
func ReadOnlyCommand(cmd string) bool {
	argv, ok := splitPlainCommand(cmd)
	return ok && ReadOnlyArgv(argv)
}

// repoShellReadOnly reports whether a repo chat's shell request is on the read-only allowlist; the command is a string
// (Claude's Bash) or an argv array (Codex).
func repoShellReadOnly(p model.PermissionRequest) bool {
	switch p.Tool {
	case "Bash", "execute":
	default:
		return false
	}
	switch v := p.Input["command"].(type) {
	case string:
		return ReadOnlyCommand(v)
	case []any:
		argv := make([]string, len(v))
		for i, a := range v {
			s, ok := a.(string)
			if !ok {
				return false
			}
			argv[i] = s
		}
		return ReadOnlyArgv(argv)
	}
	return false
}

// splitPlainCommand splits cmd into words, refusing every shell construct that could run or write something else.
func splitPlainCommand(cmd string) ([]string, bool) {
	var argv []string
	var cur strings.Builder
	inWord := false
	var quote rune
	flush := func() {
		if inWord {
			argv = append(argv, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for _, r := range cmd {
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '$', '`', '\\':
				return nil, false
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			flush()
		case strings.ContainsRune(";&|<>$`(){}\\\n\r!#*?[", r):
			return nil, false
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return argv, len(argv) > 0
}

// flagSet is the flags a read-only command may carry; anything not listed asks the user. Long flags match
// exactly (no abbreviations: git accepts `--open-files` for `--open-files-in-pager`). A cluster of short
// letters is allowed only when every letter is listed. words are single-dash words (find, go).
type flagSet struct {
	short string
	long  []string
	words []string
}

func (f flagSet) ok(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return true
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		switch {
		case strings.HasPrefix(a, "--"):
			if !slices.Contains(f.long, name) && !slices.Contains(f.words, strings.TrimPrefix(name, "-")) {
				return false
			}
		case f.words != nil:
			if !slices.Contains(f.words, strings.TrimPrefix(name, "-")) {
				return false
			}
		default:
			for _, r := range a[1:] {
				if (r < '0' || r > '9') && !strings.ContainsRune(f.short, r) {
					return false
				}
			}
		}
	}
	return true
}

var (
	gitFlags = map[string]flagSet{
		"status": {short: "sbu", long: []string{"--short", "--branch", "--porcelain", "--untracked-files", "--ignored"}},
		"log": {short: "npSG", long: []string{"--oneline", "--graph", "--stat", "--shortstat", "--numstat", "--name-only", "--name-status", "--decorate",
			"--all", "--no-merges", "--merges", "--follow", "--reverse", "--patch", "--max-count", "--since", "--until", "--author", "--grep",
			"--pretty", "--format", "--abbrev-commit", "--date", "--first-parent", "--no-color", "--no-ext-diff"}},
		"diff": {short: "pUwb", long: []string{"--stat", "--shortstat", "--numstat", "--name-only", "--name-status", "--cached", "--staged", "--no-color",
			"--patch", "--unified", "--ignore-all-space", "--ignore-space-change", "--no-ext-diff", "--minimal", "--summary"}},
		"show": {short: "ps", long: []string{"--stat", "--shortstat", "--numstat", "--name-only", "--name-status", "--no-color", "--format", "--pretty",
			"--oneline", "--patch", "--no-ext-diff", "--summary"}},
		"rev-parse": {long: []string{"--abbrev-ref", "--short", "--show-toplevel", "--is-inside-work-tree", "--verify", "--symbolic-full-name"}},
		"blame":     {short: "Lwsel", long: []string{"--porcelain", "--line-porcelain"}},
		"ls-files":  {short: "cmodsz", long: []string{"--cached", "--others", "--modified", "--deleted", "--stage", "--exclude-standard", "--full-name"}},
		"grep": {short: "niwvlLceEFABCHhIqapz", long: []string{"--line-number", "--ignore-case", "--word-regexp", "--invert-match", "--files-with-matches",
			"--files-without-match", "--count", "--extended-regexp", "--fixed-strings", "--context", "--after-context", "--before-context",
			"--heading", "--break", "--full-name", "--no-color", "--untracked"}},
		"describe": {long: []string{"--tags", "--always", "--abbrev", "--long", "--dirty"}},
	}
	gitBranchOpts = []string{"-a", "-r", "-v", "-vv", "--list", "--show-current", "--all", "--remotes"}

	cmdFlags = map[string]flagSet{
		"ls":   {short: "lahARtrSdFps1i", long: []string{"--all", "--almost-all", "--human-readable", "--recursive"}},
		"cat":  {short: "nbsAETv", long: []string{"--number", "--number-nonblank", "--squeeze-blank", "--show-all"}},
		"head": {short: "ncqv", long: []string{"--lines", "--bytes", "--quiet", "--verbose"}},
		"tail": {short: "ncqv", long: []string{"--lines", "--bytes", "--quiet", "--verbose"}},
		"wc":   {short: "lwcmL", long: []string{"--lines", "--words", "--bytes", "--chars"}},
		"pwd":  {short: "LP"},
		"grep": {short: "niwvlLceEFGABCHhIqarRosxzm", long: []string{"--line-number", "--ignore-case", "--word-regexp", "--invert-match",
			"--files-with-matches", "--files-without-match", "--count", "--regexp", "--extended-regexp", "--fixed-strings", "--context",
			"--after-context", "--before-context", "--recursive", "--no-filename", "--with-filename", "--only-matching", "--quiet",
			"--max-count", "--include", "--exclude", "--exclude-dir", "--color"}},
		"rg": {short: "niwvlceFABCgtTsSoHIq", long: []string{"--line-number", "--ignore-case", "--word-regexp", "--invert-match", "--files-with-matches",
			"--count", "--regexp", "--fixed-strings", "--context", "--after-context", "--before-context", "--glob", "--type", "--type-not",
			"--files", "--hidden", "--no-ignore", "--max-count", "--only-matching", "--smart-case", "--case-sensitive", "--no-heading",
			"--heading", "--color", "--json", "--max-depth"}},
		"find": {words: []string{"name", "iname", "type", "maxdepth", "mindepth", "path", "ipath", "not", "and", "or", "newer", "size", "mtime",
			"empty", "print", "print0", "prune", "regex", "iregex", "perm", "user", "group", "mmin", "atime", "ctime", "readable", "writable", "executable"}},
		"npm":  {long: []string{"--depth", "--json", "--all", "--long"}},
		"pnpm": {long: []string{"--depth", "--json", "--long"}},
	}

	goFlags = map[string]flagSet{
		"test":    {words: []string{"run", "v", "count", "short", "race", "timeout", "json", "p", "parallel", "failfast", "list", "tags"}},
		"vet":     {words: []string{"v", "tags", "json"}},
		"list":    {words: []string{"m", "json", "f", "e", "deps", "tags", "test"}},
		"version": {words: []string{"m", "v"}},
		"env":     {words: []string{"json"}},
		"doc":     {words: []string{"all", "src", "short", "u", "c", "cmd"}},
	}
)

// ReadOnlyArgv reports whether one already-split command is on the read-only allowlist.
func ReadOnlyArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	rest := argv[1:]
	for _, a := range rest {
		if !safeArg(a) {
			return false
		}
	}
	switch argv[0] {
	case "git":
		switch {
		case len(rest) == 0:
			return false
		case rest[0] == "branch":
			return allIn(rest[1:], gitBranchOpts)
		case rest[0] == "remote":
			return slices.Equal(rest[1:], []string{"-v"})
		}
		fs, ok := gitFlags[rest[0]]
		return ok && fs.ok(rest[1:])
	case "go":
		if len(rest) == 0 {
			return false
		}
		fs, ok := goFlags[rest[0]]
		return ok && fs.ok(rest[1:])
	case "npm", "pnpm":
		return len(rest) > 0 && rest[0] == "ls" && cmdFlags[argv[0]].ok(rest[1:])
	}
	fs, ok := cmdFlags[argv[0]]
	return ok && fs.ok(rest)
}

func allIn(args, allowed []string) bool {
	for _, a := range args {
		if !slices.Contains(allowed, a) {
			return false
		}
	}
	return true
}

// safeArg refuses a word that points outside the project, into .git, or at a common secret file.
func safeArg(a string) bool {
	if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") {
		return false
	}
	if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") && (strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~")) {
		return false
	}
	for _, seg := range strings.Split(a, "/") {
		if seg == ".." || seg == ".git" {
			return false
		}
	}
	base := path.Base(a)
	return !(strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasPrefix(base, "id_rsa"))
}
