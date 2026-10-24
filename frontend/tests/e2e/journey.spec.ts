import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
const repos: string[] = [];
function fixtureRepo() {
  const dir = mkdtempSync(path.join(tmpdir(), "lf-rebuild-fixture-"));
  repos.push(dir);
  writeFileSync(
    path.join(dir, "go.mod"),
    "module factory-fixture\n\ngo 1.25\n",
  );
  mkdirSync(path.join(dir, "greeter_test"));
  writeFileSync(
    path.join(dir, "greeter_test/greeter_test.go"),
    'package greeter_test\nimport("testing";"factory-fixture/greeter")\nfunc TestGreet(t *testing.T){if got:=greeter.Greet("Ada");got!="Hello, Ada!"{t.Fatal(got)}}\n',
  );
  for (const args of [
    ["init", "-q"],
    ["add", "-A"],
    [
      "-c",
      "user.email=saifullah.panca@amartha.com",
      "-c",
      "user.name=Saifullah Panca",
      "commit",
      "-qm",
      "test: fixture contract",
    ],
  ])
    execFileSync("git", args, { cwd: dir });
  return dir;
}
test.afterAll(() =>
  repos.forEach((dir) => rmSync(dir, { recursive: true, force: true })),
);
async function createProject(page: Page, name: string) {
  await page.getByRole("link", { name: "Factory", exact: true }).click();
  await page
    .getByRole("button", { name: "New project", exact: true })
    .first()
    .click();
  await page.fill("#prj-name", name);
  await page.fill("#prj-repo", fixtureRepo());
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeHidden();
}
async function createOrder(page: Page, title: string, objective: string) {
  await page
    .getByRole("button", { name: "New work order", exact: true })
    .click();
  await page.fill("#wo-title", title);
  await page.fill("#wo-objective", objective);
  await page.fill("#wo-acceptance", "TestGreet passes with go test ./...");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Choose the hands. Choose the eyes." }),
  ).toBeVisible();
  await page.selectOption("#wo-builder", "mock/mock-local");
  await page.selectOption("#wo-inspector", "mock/mock-local");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByRole("button", { name: "Create & draft blueprint" }).click();
}
test("full work order journey: project → plan → graph → build → quality → PR decision", async ({
  page,
}) => {
  test.setTimeout(180_000);
  await page.goto("/");
  await expect(
    page.getByRole("heading", {
      name: /Good (morning|afternoon|evening), Panca/,
    }),
  ).toBeVisible();
  await createProject(page, "E2E Greeter");
  await createOrder(
    page,
    `E2E journey ${Date.now()}`,
    "Implement the greeting function so the fixture contract test passes.",
  );
  await expect(
    page.getByText("Record failing baseline", { exact: true }),
  ).toBeVisible({ timeout: 60_000 });
  await expect(
    page.getByText("Architect review", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Approve plan", exact: true }).click();
  await page.getByRole("tab", { name: "Task graph", exact: true }).click();
  await expect(page.locator(".task-brick")).toHaveCount(4);
  await page.locator(".task-brick").first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Task contract" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Close dialog" }).click();
  await expect(
    page.locator(".detail-title").getByText("PR decision", { exact: true }),
  ).toBeVisible({
    timeout: 120_000,
  });
  await page.getByRole("tab", { name: "Quality", exact: true }).click();
  await expect(
    page.getByText("Current revision 1", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Historical evidence · revision 0"),
  ).toBeVisible();
  await page.locator("[data-check]").first().click();
  await expect(page.getByRole("dialog")).toContainText("Exit code:");
  await page.getByRole("button", { name: "Close dialog" }).click();
  await page.getByRole("tab", { name: "Activity", exact: true }).click();
  await expect(page.getByText("Execution runs", { exact: true })).toBeVisible();
  await expect(page.getByText("mock", { exact: true }).first()).toBeVisible();
  await page.getByRole("tab", { name: "Changes & PR", exact: true }).click();
  await expect(page.getByText("blocker", { exact: true })).toBeVisible();
  await expect(page.getByText("resolved", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Approve", exact: true }).click();
  await page.fill("#decision-notes", "Evidence reviewed at revision 1");
  await page
    .getByRole("button", { name: "Record approval", exact: true })
    .click();
  await expect(page.getByText("Assembled", { exact: true })).toBeVisible({
    timeout: 30_000,
  });
});
test("human clarification, plan revision and cancellation preserve the same order", async ({
  page,
}) => {
  await page.goto("/");
  await createProject(page, "E2E Clarification");
  await createOrder(
    page,
    "Clarification journey",
    "Implement the greeting. What should an empty name return?",
  );
  await expect(
    page.getByRole("button", { name: "Answer clarification" }),
  ).toBeVisible({ timeout: 60_000 });
  const url = page.url();
  await page.getByRole("button", { name: "Answer clarification" }).click();
  await page.fill(
    "#decision-notes",
    "Return Hello, stranger! for an empty name.",
  );
  await page.getByRole("button", { name: "Answer & re-review" }).click();
  await expect(
    page.getByRole("button", { name: "Approve plan", exact: true }),
  ).toBeVisible({ timeout: 60_000 });
  await page
    .getByRole("button", { name: "Request revision", exact: true })
    .click();
  await page.fill(
    "#decision-notes",
    "Keep the change scoped to the greeting function.",
  );
  await page.getByRole("button", { name: "Send revision to crew" }).click();
  await expect(
    page.getByRole("button", { name: "Approve plan", exact: true }),
  ).toBeVisible({ timeout: 60_000 });
  expect(page.url()).toBe(url);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page
    .getByRole("button", { name: "Cancel work order", exact: true })
    .click();
  await expect(page.getByText("Closed", { exact: true })).toBeVisible();
});
test("settings save, navigation and missing-order recovery against the real backend", async ({
  page,
}) => {
  await page.goto("/settings?tab=usage");
  await page.fill("#set-budget", "80.25");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByRole("status")).toHaveText("Settings saved");
  await page.reload();
  await expect(page.locator("#set-budget")).toHaveValue("80.25");
  await page.getByRole("link", { name: "Governance", exact: true }).click();
  await page.selectOption("#set-architect", "mock/mock-local");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByRole("status")).toHaveText("Settings saved");
  await page.getByRole("link", { name: "Knowledge", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Explore sample knowledge records" })
    .check();
  await expect(
    page.getByText(/UI-only preview. Nothing is ingested/),
  ).toBeVisible();
  await page.goto("/factory/missing");
  await expect(page.getByRole("alert")).toContainText("work order not found");
  await page
    .getByRole("link", { name: "Back to Factory", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "The factory floor" }),
  ).toBeVisible();
});
