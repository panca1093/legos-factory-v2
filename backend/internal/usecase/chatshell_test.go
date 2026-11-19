package usecase

import (
	"testing"

	"legos-factory/backend/internal/model"
)

func TestReadOnlyCommand(t *testing.T) {
	cases := map[string]bool{
		"git status":                      true,
		"git log --oneline -n 5":          true,
		"git diff HEAD~1 -- backend/x.go": true,
		"git show HEAD:README.md":         true,
		"git branch":                      true,
		"git branch -a":                   true,
		"git branch -vv":                  true,
		"git remote -v":                   true,
		"git grep -n 'foo bar' backend":   true,
		"git rev-parse HEAD":              true,
		"go test ./...":                   true,
		"go vet ./internal/...":           true,
		"go list -m all":                  true,
		"go doc fmt.Println":              true,
		"ls -la backend":                  true,
		"cat backend/go.mod":              true,
		"head -n 20 README.md":            true,
		"grep -rn \"TODO\" backend":       true,
		"grep -o foo a.txt":               true,
		"rg -n 'a;b' backend":             true,
		"find . -name '*.go'":             true,
		"npm ls":                          true,
		"pnpm ls --depth 0":               true,
		"npx tsc --noEmit":                true,
		"pwd":                             true,

		"git status; rm -rf x":          false,
		"git status && rm -rf x":        false,
		"git status || true":            false,
		"git status | tee out":          false,
		"git status &":                  false,
		"cat a > b":                     false,
		"cat a >> b":                    false,
		"cat < a":                       false,
		"echo $(rm -rf x)":              false,
		"cat `rm x`":                    false,
		"cat \"$HOME/x\"":               false,
		"cat a\nrm b":                   false,
		"ls (x)":                        false,
		"find . -delete":                false,
		"find . -name x -exec rm {} +":  false,
		"find . -ok rm {} ;":            false,
		"find . -fprint out":            false,
		"git branch newbranch":          false,
		"git branch -D main":            false,
		"git remote add x y":            false,
		"git remote -v add":             false,
		"git commit -m x":               false,
		"git checkout main":             false,
		"git push":                      false,
		"git -c core.pager=x log":       false,
		"git diff --output=out":         false,
		"git diff --ext-diff":           false,
		"git grep -O foo":               false,
		"go build ./...":                false,
		"go test -o x ./...":            false,
		"go test -exec ./run ./...":     false,
		"go test -coverprofile=c.out":   false,
		"go env -w GOFLAGS=-x":          false,
		"go run main.go":                false,
		"go mod tidy":                   false,
		"rg --pre ./x foo":              false,
		"npm install":                   false,
		"npm ls; npm i":                 false,
		"npx tsc":                       false,
		"npx evil --noEmit":             false,
		"rm -rf x":                      false,
		"sed -i s/a/b/ file":            false,
		"cat /etc/passwd":               false,
		"cat ~/.ssh/id_rsa":             false,
		"cat ../other/x":                false,
		"cat .env":                      false,
		"cat backend/.env.local":        false,
		"cat key.pem":                   false,
		"cat .git/config":               false,
		"grep foo --file=/etc/passwd a": false,
		"cat 'unterminated":             false,
		"":                              false,
		"   ":                           false,
	}
	for cmd, want := range cases {
		if got := ReadOnlyCommand(cmd); got != want {
			t.Errorf("ReadOnlyCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestRepoShellReadOnlyReadsStringAndArgv(t *testing.T) {
	req := func(tool string, command any) model.PermissionRequest {
		return model.PermissionRequest{Server: model.RepoToolServer, Tool: tool, Input: map[string]any{"command": command}}
	}
	cases := []struct {
		p    model.PermissionRequest
		want bool
	}{
		{req("Bash", "git status"), true},
		{req("Bash", "git status; rm x"), false},
		{req("execute", []any{"git", "status"}), true},
		{req("execute", []any{"bash", "-lc", "git status"}), false},
		{req("execute", []any{"git", 1}), false},
		{req("Edit", "git status"), false},
		{model.PermissionRequest{Server: model.RepoToolServer, Tool: "Bash"}, false},
	}
	for _, c := range cases {
		if got := repoShellReadOnly(c.p); got != c.want {
			t.Errorf("%+v = %v, want %v", c.p, got, c.want)
		}
	}
}
