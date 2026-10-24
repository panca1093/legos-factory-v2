import { test, expect, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import {
  projects,
  orders,
  overview,
  user,
  models,
  settings,
  tasks,
  runs,
  checks,
  findings,
  events,
} from "../fixtures/workspace";
const output = path.resolve("../docs/screenshots/frontend-rebuild");
async function fixture(page: Page, role: string = user.role) {
  await page.route("**/api/v1/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    let body: unknown = {};
    if (p.endsWith("/auth/session"))
      body = { user: { ...user, role }, mode: "test" };
    else if (p.endsWith("/projects")) body = { projects };
    else if (p.endsWith("/work-orders")) body = { work_orders: orders };
    else if (p.endsWith("/overview")) body = overview;
    else if (p.endsWith("/models")) body = { models };
    else if (p.endsWith("/settings")) body = settings;
    else if (p.endsWith("/connections"))
      body = {
        credentials_configured: true,
        connections: [
          {
            id: "conn-gh",
            provider: "github",
            name: "Atlas GitHub",
            scope: "shared",
            owner_id: "admin",
            base_url: "https://api.github.com",
            status: "active",
            account: "factory-bot",
            verified_at: "2026-10-24T00:00:00Z",
            has_credential: true,
            created_at: "2026-10-24T00:00:00Z",
          },
        ],
      };
    else if (p.endsWith("/events")) {
      await route.fulfill({
        contentType: "text/event-stream",
        body: ": test-only visual fixture\n\n",
      });
      return;
    } else if (p.endsWith("/activity")) body = { events };
    else if (p.endsWith("/quality")) body = { checks };
    else if (p.endsWith("/findings")) body = { findings };
    else {
      const id = p.split("/").at(-1);
      body = { work_order: orders.find((w) => w.id === id), plan: tasks, runs };
    }
    await route.fulfill({ json: body });
  });
}
async function ready(page: Page) {
  await page.locator("main h1,main h2").first().waitFor();
  await page.evaluate(() => document.fonts.ready);
  await expect(page.locator("body")).not.toContainText("Starting factory…");
  await expect(page.locator("main")).not.toContainText(
    "Loading workspace settings…",
  );
  await expect(page.locator("main")).not.toContainText("Loading work order…");
}
const viewports = [
  { name: "desktop", width: 1440, height: 1110 },
  { name: "tablet", width: 1024, height: 900 },
  { name: "mobile", width: 390, height: 844 },
];
for (const viewport of viewports)
  test(`visual comparison ${viewport.name}`, async ({ browser }) => {
    test.setTimeout(180_000);
    const context = await browser.newContext({
      viewport: { width: viewport.width, height: viewport.height },
      reducedMotion: "reduce",
    });
    const app = await context.newPage(),
      reference = await context.newPage();
    await app.clock.setFixedTime(new Date("2026-10-24T01:00:00Z"));
    await fixture(app);
    const errors: string[] = [];
    app.on("pageerror", (e) => errors.push(e.message));
    const metadata: unknown[] = [];
    async function capture(name: string) {
      await ready(app);
      await reference.locator("#main").waitFor();
      await app.evaluate(() => window.scrollTo(0, 0));
      await reference.evaluate(() => window.scrollTo(0, 0));
      for (const [kind, page] of [
        ["app", app],
        ["reference", reference],
      ] as const) {
        const folder = path.join(output, viewport.name, kind);
        mkdirSync(folder, { recursive: true });
        await page.screenshot({
          path: path.join(folder, name + ".png"),
          animations: "disabled",
        });
      }
      const overflow = await app.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth + 1,
      );
      expect(overflow, `${name} must not overflow page horizontally`).toBe(
        false,
      );
      metadata.push({
        state: name,
        viewport: { width: viewport.width, height: viewport.height },
        reference: `${viewport.name}/reference/${name}.png`,
        rebuilt: `${viewport.name}/app/${name}.png`,
      });
    }
    async function freshReference() {
      await reference.goto("http://127.0.0.1:15174");
      await reference.evaluate(() =>
        localStorage.removeItem("legos-factory-v2"),
      );
      await reference.reload();
    }
    async function referenceOrder(id: string, tab: string) {
      await freshReference();
      await reference.evaluate(
        ({ id, tab }) => {
          document
            .querySelector<HTMLElement>(
              '[data-action="nav"][data-page="factory"]',
            )!
            .click();
          document
            .querySelector<HTMLElement>(
              `[data-action="open"][data-id="${id}"]`,
            )!
            .click();
          document
            .querySelector<HTMLElement>(
              `[data-action="tab"][data-tab="${tab}"]`,
            )!
            .click();
        },
        { id, tab },
      );
    }
    await freshReference();
    await app.goto("/");
    await ready(app);
    await capture("01-overview");
    expect(
      await app
        .locator("body")
        .evaluate((e) => getComputedStyle(e).backgroundColor),
    ).toBe(
      await reference
        .locator("body")
        .evaluate((e) => getComputedStyle(e).backgroundColor),
    );
    expect((await app.locator(".sidebar").boundingBox())?.width).toBe(
      (await reference.locator(".sidebar").boundingBox())?.width,
    );
    if (viewport.name === "desktop") {
      const a = await app.locator(".floor").boundingBox(),
        r = await reference.locator(".floor").boundingBox();
      expect(Math.abs(a!.x - r!.x)).toBeLessThanOrEqual(2);
      expect(Math.abs(a!.y - r!.y)).toBeLessThanOrEqual(8);
      expect(Math.abs(a!.width - r!.width)).toBeLessThanOrEqual(2);
    }
    await reference.evaluate(() =>
      document
        .querySelector<HTMLElement>('[data-action="nav"][data-page="factory"]')!
        .click(),
    );
    await app.goto("/factory");
    await capture("02-factory-board");
    await reference.evaluate(() =>
      document
        .querySelector<HTMLElement>('[data-action="view"][data-view="list"]')
        ?.click(),
    );
    await app.getByRole("button", { name: "List view" }).click();
    await capture("03-factory-list");
    await referenceOrder("LF-128", "plan");
    await app.goto("/factory/LF-128");
    await capture("04-blueprint");
    await referenceOrder("LF-127", "graph");
    await app.goto("/factory/LF-127?tab=graph");
    await capture("05-task-graph");
    await reference
      .locator('[data-action="node"][data-node="implementation"]')
      .click();
    await app.locator('[data-node="implementation"]').click();
    await capture("06-task-details");
    await referenceOrder("LF-127", "activity");
    await app.goto("/factory/LF-127?tab=activity");
    await capture("07-activity");
    await referenceOrder("LF-125", "qa");
    await app.goto("/factory/LF-125?tab=quality");
    await capture("08-quality");
    await reference.evaluate(() =>
      document.querySelector<HTMLElement>('[data-action="evidence"]')?.click(),
    );
    if (!(await reference.getByRole("dialog").count()))
      await reference.evaluate(() =>
        document
          .querySelector<HTMLElement>('[data-action="quality-contract"]')
          ?.click(),
      );
    await app.locator("[data-check]").first().click();
    await capture("09-evidence");
    await referenceOrder("LF-125", "changes");
    await app.goto("/factory/LF-125?tab=changes");
    await capture("10-changes-pr");
    await freshReference();
    await reference.evaluate(() =>
      document
        .querySelector<HTMLElement>('[data-action="nav"][data-page="factory"]')!
        .click(),
    );
    await reference.locator('[data-action="new"]').first().click();
    await app.goto("/factory?new=1");
    await expect(app.getByRole("dialog")).toBeVisible();
    await capture("11-new-order-intent");
    await reference.fill("#new-title", "Make payment retries idempotent");
    await reference.fill("#new-brief", orders[0].objective);
    await reference.fill("#new-criteria", orders[0].acceptance);
    await reference.locator('[data-action="new-next"]').click();
    await app.fill("#wo-title", "Make payment retries idempotent");
    await app.fill("#wo-objective", orders[0].objective);
    await app.fill("#wo-acceptance", orders[0].acceptance);
    await app.getByRole("button", { name: "Continue", exact: true }).click();
    await expect(
      app.getByRole("heading", { name: "Choose the hands. Choose the eyes." }),
    ).toBeVisible();
    await capture("12-new-order-crew");
    await reference.locator('[data-action="new-next"]').click();
    await app.getByRole("button", { name: "Continue", exact: true }).click();
    await expect(
      app
        .getByRole("dialog")
        .getByRole("heading", { name: "Make payment retries idempotent" }),
    ).toBeVisible();
    await capture("13-new-order-review");
    await freshReference();
    await reference.evaluate(() =>
      document
        .querySelector<HTMLElement>(
          '[data-action="nav"][data-page="settings"]',
        )!
        .click(),
    );
    await app.goto("/settings");
    await capture("14-settings");
    await freshReference();
    await reference.evaluate(() =>
      document
        .querySelector<HTMLElement>('[data-action="nav"][data-page="catalog"]')!
        .click(),
    );
    await app.goto("/knowledge");
    await capture("15-knowledge");
    await freshReference();
    await reference.locator('[data-action="theme"]').click();
    await app.goto("/");
    await app.getByRole("button", { name: "Switch theme" }).click();
    await capture("16-overview-dark");
    if (viewport.name === "mobile") {
      await reference.locator('button[data-action="menu"]').click();
      await app.getByRole("button", { name: "Toggle navigation" }).click();
      await capture("17-mobile-navigation");
    }
    expect(errors).toEqual([]);
    writeFileSync(
      path.join(output, viewport.name, "comparison-manifest.json"),
      JSON.stringify(
        {
          fixtures:
            "Test-only API responses; production store contains no sample data.",
          pairs: metadata,
        },
        null,
        2,
      ),
    );
    await context.close();
  });
test("browser permissions, unavailable settings, dialog keyboard handling and error states", async ({
  page,
}) => {
  await fixture(page, "VIEWER");
  await page.goto("/factory");
  await expect(
    page.getByRole("button", { name: "New work order", exact: true }),
  ).toHaveCount(0);
  await page.goto("/factory/LF-125?tab=changes");
  await expect(
    page.getByRole("button", { name: "Approve", exact: true }),
  ).toBeDisabled();
  await page.goto("/settings");
  await expect(page.locator("#set-architect")).toBeDisabled();
  await page.goto("/settings?tab=integrations");
  await expect(page.getByText("Atlas GitHub")).toBeVisible();
  await expect(page.getByText("Verified", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Test" })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Add connection" }),
  ).toHaveCount(0);
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveAttribute("aria-modal", "true");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
  await page.unrouteAll({ behavior: "wait" });
  await page.route("**/api/v1/**", (route) =>
    new URL(route.request().url()).pathname.endsWith("/auth/session")
      ? route.fulfill({ json: { user, mode: "test" } })
      : route.fulfill({
          status: 503,
          json: {
            error: {
              code: "UNAVAILABLE",
              message: "Workspace temporarily unavailable",
            },
          },
        }),
  );
  await page.goto("/");
  await expect(page.getByRole("alert").first()).toContainText(
    "Workspace temporarily unavailable",
  );
});

test("settings initialization failure cannot overwrite saved configuration", async ({
  page,
}) => {
  await fixture(page);
  await page.route("**/api/v1/settings", (route) =>
    route.fulfill({
      status: 503,
      json: {
        error: {
          code: "UNAVAILABLE",
          message: "Settings temporarily unavailable",
        },
      },
    }),
  );
  await page.goto("/settings");
  await expect(page.getByRole("alert")).toContainText(
    "Settings temporarily unavailable",
  );
  await expect(page.locator("#set-architect")).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Save settings", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Try again", exact: true }),
  ).toBeVisible();
});
test("expired authenticated requests return to sign-in and remove mutation controls", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/settings");
  await expect(page.locator("#set-architect")).toBeEnabled();
  await page.route("**/api/v1/settings", (route) =>
    route.fulfill({
      status: 401,
      json: { error: { code: "UNAUTHENTICATED", message: "Session expired" } },
    }),
  );
  await page
    .getByRole("button", { name: "Save settings", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Save settings", exact: true }),
  ).toHaveCount(0);
});
