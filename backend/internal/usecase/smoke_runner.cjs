// Factory-owned Playwright smoke runner. argv[2] is a JSON spec file; stdout gets one JSON result array.
const fs = require("fs");
const path = require("path");
const spec = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const { chromium } = require(require.resolve("playwright-core", { paths: [spec.playwrightDir] }));

const overlayProbe = () => {
  const portal = document.querySelector("nextjs-portal");
  if (!portal) return "";
  const root = portal.shadowRoot;
  if (!root) return "";
  const dialog = root.querySelector("[data-nextjs-dialog], [data-nextjs-error-overlay], [data-nextjs-dialog-overlay]");
  if (!dialog) return "";
  const text = (dialog.innerText || "").trim().split("\n").slice(0, 4).join(" | ");
  return text || "error overlay";
};

async function launch() {
  const attempts = [];
  if (spec.chromePath) attempts.push({ executablePath: spec.chromePath });
  attempts.push({ channel: "chrome" }, {});
  let last;
  for (const o of attempts) {
    try {
      return await chromium.launch({ headless: true, ...o });
    } catch (e) {
      last = e;
    }
  }
  throw new Error("no usable browser (set LEGOS_SMOKE_CHROME or run `npx playwright install chromium` in the factory frontend): " + String(last).split("\n")[0]);
}

async function login(page, l) {
  const loginPath = new URL(l.path, spec.baseURL).pathname;
  let last;
  for (let attempt = 0; attempt < 3; attempt++) {
    await page.goto(spec.baseURL + l.path, { waitUntil: "load", timeout: spec.navTimeoutMs });
    await page.waitForLoadState("networkidle", { timeout: 10000 }).catch(() => {});
    await page.waitForTimeout(1000); // a form submitted before React hydrates falls back to a native GET
    const user = page.locator("input:not([type=password]):not([type=hidden]):not([type=checkbox]):not([type=radio])").first();
    await user.fill(l.user, { timeout: 15000 });
    await page.locator("input[type=password]").first().fill(l.password, { timeout: 15000 });
    await page.locator("button[type=submit]").first().click({ timeout: 15000 });
    try {
      await page.waitForURL((u) => u.pathname !== loginPath, { timeout: 12000 });
      return;
    } catch (e) {
      last = e;
    }
  }
  throw last;
}

async function visit(ctx, route) {
  const page = await ctx.newPage();
  const problems = [];
  page.on("pageerror", (e) => problems.push("uncaught error: " + String(e.message || e).split("\n")[0]));
  page.on("console", (m) => {
    if (m.type() === "error" && !/Failed to load resource/.test(m.text())) problems.push("console error: " + m.text().split("\n")[0].slice(0, 300));
  });
  page.on("response", (r) => {
    if (r.status() >= 500) problems.push(`HTTP ${r.status()} ${new URL(r.url()).pathname}`);
  });
  let status = 0;
  try {
    const resp = await page.goto(spec.baseURL + route, { waitUntil: "load", timeout: spec.navTimeoutMs });
    status = resp ? resp.status() : 0;
    await page.waitForLoadState("networkidle", { timeout: 8000 }).catch(() => {});
    await page.waitForTimeout(800);
    if (spec.login && new URL(page.url()).pathname === new URL(spec.login.path, spec.baseURL).pathname && route.split("?")[0] !== spec.login.path) {
      problems.push("redirected to the login page (session was not kept)");
    }
    const overlay = await page.evaluate(overlayProbe).catch(() => "");
    if (overlay) problems.push("Next error overlay: " + overlay.slice(0, 300));
  } catch (e) {
    problems.push("navigation failed: " + String(e.message || e).split("\n")[0]);
  }
  const shot = path.join(spec.outDir, route.replace(/[^a-zA-Z0-9]+/g, "_").replace(/^_|_$/g, "") + ".png");
  await page.screenshot({ path: shot, fullPage: false }).catch(() => {});
  await page.close();
  return { path: route, status, ok: problems.length === 0, problems: [...new Set(problems)], screenshot: fs.existsSync(shot) ? shot : "" };
}

(async () => {
  fs.mkdirSync(spec.outDir, { recursive: true });
  const browser = await launch();
  const out = [];
  try {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    if (spec.login) {
      const page = await ctx.newPage();
      try {
        await login(page, spec.login);
      } catch (e) {
        await page.screenshot({ path: path.join(spec.outDir, "login-failed.png") }).catch(() => {});
        console.log(JSON.stringify({ loginError: String(e.message || e).split("\n")[0] + " (url " + page.url() + ")", results: [] }));
        return;
      } finally {
        await page.close();
      }
    }
    for (const r of spec.routes) out.push(await visit(ctx, r));
  } finally {
    await browser.close();
  }
  console.log(JSON.stringify({ results: out }));
})().catch((e) => {
  console.log(JSON.stringify({ fatal: String(e.message || e).split("\n")[0], results: [] }));
});
