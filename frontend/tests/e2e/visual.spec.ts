import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
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
  usage,
} from "../fixtures/workspace";
const output = path.resolve("../docs/screenshots/app");
async function fixture(page: Page, role: string = user.role) {
  await page.route("**/api/v1/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    let body: unknown = {};
    if (p.endsWith("/auth/session"))
      body = { user: { ...user, role }, mode: "test" };
    else if (p.endsWith("/projects")) body = { projects };
    else if (p.endsWith("/work-orders")) body = { work_orders: orders };
    else if (p.endsWith("/overview")) body = overview;
    else if (p.endsWith("/usage/summary")) body = usage;
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
  { name: "small", width: 375, height: 812 },
];
for (const viewport of viewports)
  test(`app layout ${viewport.name}`, async ({ browser }) => {
    test.setTimeout(180_000);
    const context = await browser.newContext({
      viewport: { width: viewport.width, height: viewport.height },
      reducedMotion: "reduce",
    });
    const app = await context.newPage();
    await app.clock.setFixedTime(new Date("2026-10-24T01:00:00Z"));
    await fixture(app);
    const errors: string[] = [];
    app.on("pageerror", (e) => errors.push(e.message));
    async function capture(name: string) {
      await ready(app);
      await app.evaluate(() => window.scrollTo(0, 0));
      const folder = path.join(output, viewport.name);
      mkdirSync(folder, { recursive: true });
      await app.screenshot({
        path: path.join(folder, name + ".png"),
        animations: "disabled",
        fullPage: true,
      });
      const overflow = await app.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth + 1,
      );
      expect(overflow, `${name} must not overflow page horizontally`).toBe(
        false,
      );
    }
    await app.goto("/");
    await ready(app);
    await expect(
      app.locator("[aria-labelledby=user-activity] .heat i"),
    ).toHaveCount(usage.activity.length);
    await expect(app.locator(".agent-row").first()).toContainText("Builder");
    await capture("01-overview");
    await app.getByRole("button", { name: "Cumulative" }).click();
    await capture("01-overview-cumulative");
    await app.goto("/factory");
    await capture("02-factory-board");
    if (viewport.width > 720) {
      await expect(app.locator(".topnav")).toBeVisible();
      await expect(app.locator(".tabbar")).toBeHidden();
    } else {
      await expect(app.locator(".topnav")).toBeHidden();
      await expect(app.locator(".tabbar")).toBeVisible();
    }
    await expect(
      app.locator(".topnav a.active, .tabbar a.active").first(),
    ).toHaveText("Factory");
    await capture("03-factory-list");
    await app.goto("/factory/LF-128");
    await capture("04-blueprint");
    await app.goto("/factory/LF-127?tab=graph");
    await capture("05-task-graph");
    await app.locator('[data-node="implementation"]').click();
    await capture("06-task-details");
    await app.goto("/factory/LF-127?tab=activity");
    await capture("07-activity");
    await app.goto("/factory/LF-125?tab=quality");
    await capture("08-quality");
    await app.locator("[data-check]").first().click();
    await capture("09-evidence");
    await app.goto("/factory/LF-125?tab=changes");
    await capture("10-changes-pr");
    await app.goto("/factory?new=1");
    await expect(app.getByRole("dialog")).toBeVisible();
    await capture("11-new-order-intent");
    await app.fill("#wo-title", "Make payment retries idempotent");
    await app.fill("#wo-objective", orders[0].objective);
    await app.fill("#wo-acceptance", orders[0].acceptance);
    await app.getByRole("button", { name: "Continue", exact: true }).click();
    await expect(
      app.getByRole("heading", { name: "Choose the hands. Choose the eyes." }),
    ).toBeVisible();
    await capture("12-new-order-crew");
    await app.getByRole("button", { name: "Continue", exact: true }).click();
    await expect(
      app
        .getByRole("dialog")
        .getByRole("heading", { name: "Make payment retries idempotent" }),
    ).toBeVisible();
    await capture("13-new-order-review");
    await app.goto("/settings");
    await capture("14-settings");
    await app.goto("/knowledge");
    await capture("15-knowledge");
    await app.goto("/");
    await app.getByRole("button", { name: "Switch theme" }).click();
    await capture("16-overview-dark");
    if (viewport.width <= 720) {
      await app.getByRole("button", { name: "Show sidebar" }).click();
      await capture("17-mobile-navigation");
    }
    expect(errors).toEqual([]);
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
