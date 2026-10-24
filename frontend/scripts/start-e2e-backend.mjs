import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, openSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
const data = mkdtempSync(path.join(tmpdir(), "lf-rebuild-e2e-data-"));
const root = fileURLToPath(new URL("../../", import.meta.url));
const log = openSync(path.join(data, "server.log"), "a");
execFileSync(
  "go",
  ["build", "-o", path.join(data, "factory"), "./cmd/factory"],
  {
    cwd: path.join(root, "backend"),
    env: { ...process.env, GOROOT: "/opt/homebrew/Cellar/go/1.26.3/libexec" },
    stdio: ["ignore", log, log],
  },
);
const child = spawn(path.join(data, "factory"), [], {
  cwd: path.join(root, "backend"),
  stdio: ["ignore", log, log],
  env: {
    ...process.env,
    GOROOT: "/opt/homebrew/Cellar/go/1.26.3/libexec",
    FACTORY_ADDR: "127.0.0.1:18081",
    FACTORY_DATA_DIR: data,
    FACTORY_AUTH_MODE: "dev",
    FACTORY_DEV_USER_NAME: "Saifullah Panca",
    FACTORY_OPENCODE_URL: "",
    FACTORY_OPENCODE_PASSWORD: "",
    FACTORY_FRONTEND_ORIGIN: "http://127.0.0.1:15173",
  },
});
for (const signal of ["SIGTERM", "SIGINT"])
  process.on(signal, () => {
    try {
      child.kill(signal);
    } catch {
      /* The child may already have exited. */
    }
  });
child.on("exit", (code) => {
  rmSync(data, { recursive: true, force: true });
  process.exit(code ?? 0);
});
