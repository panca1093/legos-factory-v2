# Playbook: first live run (M1)

Goal: one real work order on a throwaway GitHub repo ends with a PR open and CI green.
Takes about 20 minutes, most of it waiting on the agent and CI.

## 0. Prerequisites

- Docker running (OrbStack is fine).
- `gh` logged in as `panca1093`.
- Go with the GOROOT fix: `export GOROOT=/opt/homebrew/Cellar/go/1.26.3/libexec`.

## 1. Create the testbed repo

A tiny private repo with one unimplemented function, a test for it, and a CI job named `test`.

```bash
mkdir -p ~/tmp/legos-factory-testbed/calc ~/tmp/legos-factory-testbed/.github/workflows && cd ~/tmp/legos-factory-testbed
```

```bash
cat > go.mod <<'EOF'
module testbed

go 1.26
EOF
cat > calc/calc.go <<'EOF'
package calc

// Sum returns the sum of xs.
func Sum(xs []int) int {
	panic("not implemented")
}
EOF
cat > calc/calc_test.go <<'EOF'
package calc

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Sum = %d, want 6", got)
	}
	if got := Sum(nil); got != 0 {
		t.Fatalf("Sum(nil) = %d, want 0", got)
	}
}
EOF
cat > .github/workflows/ci.yml <<'EOF'
name: ci
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go test ./...
EOF
```

```bash
git init -b main && git add . && git commit -m "init testbed" && gh repo create panca1093/legos-factory-testbed --private --source . --push
```

## 2. Credentials

1. GitHub → Settings → Developer settings → Fine-grained tokens → new token.
   Repository access: only `legos-factory-testbed`.
   Permissions: Contents read/write, Pull requests read/write, Commit statuses read, Checks read.
2. Claude subscription token: run `claude setup-token` and copy the output.
3. Put both in a private env file:

```bash
mkdir -p ~/.config/legos-factory && touch ~/.config/legos-factory/live.env && chmod 600 ~/.config/legos-factory/live.env && open -e ~/.config/legos-factory/live.env
```

Contents:

```
export LF_GITHUB_TOKEN=github_pat_...
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat...
```

## 3. Build

From the repo root (`legos-factory-v2`):

```bash
docker build -t legos-factory-sandbox:dev factory/sandbox
```

```bash
cd factory && export GOROOT=/opt/homebrew/Cellar/go/1.26.3/libexec && go build -o ~/bin/factory ./cmd/factory
```

Skip the image build if `docker images legos-factory-sandbox` already lists `dev`.

## 4. Run

From `factory/`, in one shell:

```bash
source ~/.config/legos-factory/live.env
```

```bash
WO=$(~/bin/factory create -db live.db -project examples/live-m1.json -objective "Implement calc.Sum so go test ./... passes") && echo $WO
```

```bash
~/bin/factory watch -db live.db -runtime docker -image legos-factory-sandbox:dev -project examples/live-m1.json -wo $WO
```

`watch` runs the builder, verify, pushes, opens the PR, then polls CI every 30s.
It prints each status change and exits on `ci_green` or a terminal status.
Ctrl-C is safe: rerun the same `watch` command and it resumes from the fact store.

Check status any time from another shell:

```bash
~/bin/factory status -db live.db -wo $WO
```

## 5. Pass / fail

| You see | Meaning | Next |
|---|---|---|
| `ci_green` + PR on GitHub | **M1 passed** | Merge the PR yourself (`max_level: 2`) |
| `ci_failed` then back to building | Resume path working | Let `watch` continue |
| `failed` / `awaiting_human` | Escalation (verify kept failing, agent or credential error, ...) | Send me the `watch` output and `live.db` path |
| `env ... is empty` at start | `live.env` not sourced in this shell | `source` it again |

## 6. Extra M0 checks (optional, while it runs)

```bash
docker stats --no-stream $(docker ps -q --filter ancestor=legos-factory-sandbox:dev)
```

Run it a few times during the build step and note the peak memory: that number goes into SPEC §10.

## 7. Clean up

```bash
rm live.db*
```

```bash
gh repo delete panca1093/legos-factory-testbed
```
