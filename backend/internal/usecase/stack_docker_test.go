//go:build docker

package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// Run with: go test -tags docker ./internal/usecase -run TestRealDockerStack
func TestRealDockerStack(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	dir := t.TempDir()
	compose := "services:\n  postgres:\n    image: postgres:16-alpine\n    environment: {POSTGRES_USER: t, POSTGRES_PASSWORD: t, POSTGRES_DB: t}\n    ports: [\"5432:5432\"]\n    healthcheck: {test: [\"CMD-SHELL\", \"pg_isready -U t\"], interval: 1s, retries: 30}\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(compose))
	p := model.Profile{
		Sources:  []model.ProfileSource{{Path: "docker-compose.yml", SHA256: hex.EncodeToString(sum[:])}},
		Services: &model.ProfileServices{Compose: "docker-compose.yml", Names: []string{"postgres"}, Ports: map[string]int{"postgres": 5432}},
		Database: &model.ProfileDatabase{Service: "postgres", AdminURL: "postgres://t:t@{{postgres}}/t?sslmode=disable"},
		Env:      map[string]string{"DATABASE_URL": "postgres://t:t@{{postgres}}/{{db}}"},
	}
	m := newStackMgr(nil, t.TempDir())
	project := "itest_" + strings.ToLower(time.Now().Format("150405"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = m.stop(context.Background(), project) })
	l, err := m.acquire(ctx, project, "run_docker_itest", dir, p)
	if err != nil {
		t.Fatal(err)
	}
	env, err := resolveEnv(p.Env, l.values(false))
	if err != nil || !strings.Contains(env[0], "/run_docker_itest") || strings.Contains(env[0], ":5432/") {
		t.Fatalf("env=%v err=%v", env, err)
	}
	out, err := m.docker.Run(ctx, "compose", "-p", stackName(project), "exec", "-T", "postgres", "psql", "postgres://t:t@127.0.0.1:5432/run_docker_itest", "-tAc", "select 1")
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("run database not usable: %q %v", out, err)
	}
	l.release()
	if _, err := m.docker.Run(ctx, "compose", "-p", stackName(project), "exec", "-T", "postgres", "psql", "postgres://t:t@127.0.0.1:5432/run_docker_itest", "-c", "select 1"); err == nil {
		t.Fatal("database survived release")
	}
	if err := m.stop(ctx, project); err != nil {
		t.Fatal(err)
	}
}
