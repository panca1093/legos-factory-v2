//go:build smoke

package usecase

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

const realServer = `require("http").createServer((q,s)=>{s.setHeader("content-type","text/html");
if(q.url==="/bad"){s.end("<script>throw new Error('kaboom')</script>bad");return}
s.end("<h1>"+q.url+"</h1>")}).listen(+process.env.PORT,"127.0.0.1")`

// Needs node, a Chrome (LEGOS_SMOKE_CHROME or the system one) and a playwright-core install (LEGOS_PLAYWRIGHT_DIR).
func TestRealSmokeBrowser(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil || os.Getenv("LEGOS_PLAYWRIGHT_DIR") == "" {
		t.Skip("needs node and LEGOS_PLAYWRIGHT_DIR")
	}
	r := newSmokeRig(t, []string{"web/app/ok/page.tsx", "web/app/bad/page.tsx"}, map[string]string{
		"web/app/ok/page.tsx": ``, "web/app/bad/page.tsx": ``,
	})
	r.f.PlaywrightDir = ""
	r.f.smoke = smokeDeps{}
	script := t.TempDir() + "/server.js"
	if err := os.WriteFile(script, []byte(realServer), 0o644); err != nil {
		t.Fatal(err)
	}
	p := model.Profile{Smoke: &model.Smoke{
		Start:      []model.SmokeProcess{{Name: "web", Command: "node " + script, PortEnv: "PORT", Ready: "/"}},
		Base:       "web",
		RoutesFrom: "web/app/**/page.tsx",
	}}
	_, res := r.f.runSmoke(context.Background(), r.wo, p)
	if res.Outcome != model.Fail || !strings.Contains(res.Output, "PASS /ok") || !strings.Contains(res.Output, "FAIL /bad") || !strings.Contains(res.Output, "kaboom") {
		t.Fatalf("%+v", res)
	}
}
