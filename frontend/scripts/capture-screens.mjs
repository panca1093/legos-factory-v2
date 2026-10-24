import { spawnSync } from "node:child_process";
const result = spawnSync("npm", ["run", "e2e", "--", "visual.spec.ts"], {
  cwd: new URL("../", import.meta.url),
  stdio: "inherit",
});
process.exit(result.status ?? 1);
