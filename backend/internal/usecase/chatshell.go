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
		case strings.ContainsRune(";&|<>$`(){}\\\n\r!#", r):
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

var (
	gitReadOnly   = []string{"status", "log", "diff", "show", "rev-parse", "blame", "ls-files", "grep", "describe"}
	gitBranchOpts = []string{"-a", "-r", "-v", "-vv", "--list", "--show-current", "--all", "--remotes"}
	fileReaders   = []string{"ls", "cat", "head", "tail", "wc", "grep", "rg", "pwd"}
	goReadOnly    = []string{"test", "vet", "list", "version", "env", "doc"}
	findWrites    = []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"}
	// flags that write a file, run another program or change configuration
	writingFlags = []string{"output", "o", "exec", "toolexec", "vettool", "coverprofile", "cpuprofile", "memprofile", "blockprofile",
		"mutexprofile", "trace", "outputdir", "overlay", "modfile", "pre", "pre-glob", "ext-diff", "open-files-in-pager", "w", "u", "O"}
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
		return slices.Contains(gitReadOnly, rest[0]) && !hasWritingFlag(rest[1:], true)
	case "go":
		return len(rest) > 0 && slices.Contains(goReadOnly, rest[0]) && !hasWritingFlag(rest[1:], true)
	case "find":
		for _, a := range rest {
			if slices.Contains(findWrites, a) {
				return false
			}
		}
		return true
	case "npm", "pnpm":
		return len(rest) > 0 && rest[0] == "ls" && !hasWritingFlag(rest[1:], true)
	case "npx":
		return slices.Equal(rest, []string{"tsc", "--noEmit"})
	}
	return slices.Contains(fileReaders, argv[0]) && !hasWritingFlag(rest, false)
}

func allIn(args, allowed []string) bool {
	for _, a := range args {
		if !slices.Contains(allowed, a) {
			return false
		}
	}
	return true
}

// hasWritingFlag looks at flag names; one-letter flags only count when short is set, as `grep -o` is harmless while `git -o` is not.
func hasWritingFlag(args []string, short bool) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if slices.Contains(writingFlags, name) && (short || len(name) > 1) {
			return true
		}
	}
	return false
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
