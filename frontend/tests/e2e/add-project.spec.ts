import { expect, test, type Page } from "@playwright/test";
import {
  projects,
  orders,
  overview,
  user,
  models,
  settings,
  usage,
} from "../fixtures/workspace";
async function fixture(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    let body: unknown = {};
    if (p.endsWith("/auth/session")) body = { user, mode: "test" };
    else if (p.endsWith("/projects")) body = { projects };
    else if (p.endsWith("/work-orders")) body = { work_orders: orders };
    else if (p.endsWith("/overview")) body = overview;
    else if (p.endsWith("/usage/summary")) body = usage;
    else if (p.endsWith("/models")) body = { models };
    else if (p.endsWith("/settings")) body = settings;
    else if (p.endsWith("/connections"))
      body = { credentials_configured: true, connections: [] };
    else if (p.endsWith("/events")) {
      await route.fulfill({
        contentType: "text/event-stream",
        body: ": test-only fixture\n\n",
      });
      return;
    } else if (p.includes("/local/")) {
      if (p.endsWith("/gh"))
        body = { installed: true, authenticated: true, account: "panca" };
      else if (p.endsWith("/pick-folder")) body = { path: "/code/new-app" };
      else
        body = {
          path: "/code/new-app",
          name: "new-app",
          is_git: true,
          github_repo: "acme/new-app",
          default_branch: "main",
          language: "Go",
          repos: [],
        };
    }
    await route.fulfill({ json: body });
  });
}
test("Enter browses a local folder, shows publishing, Escape closes", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/factory");
  await page
    .getByRole("button", { name: "Add project", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Other ways to add")).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: /Browse folder/ }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(dialog.getByText("acme/new-app").first()).toBeVisible();
  await expect(dialog.getByText("via your gh login")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
});
