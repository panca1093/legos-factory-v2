import { expect, test } from '@playwright/test'
import { execSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// The backend must be running on 127.0.0.1:8080 in dev auth mode
// (see README quick start). This journey creates its own project and
// work order so it is repeatable.

function fixtureRepo(): string {
  const dir = mkdtempSync(path.join(tmpdir(), 'lf-e2e-repo-'))
  writeFileSync(path.join(dir, 'go.mod'), 'module factory-fixture\n\ngo 1.25\n')
  mkdirSync(path.join(dir, 'greeter_test'))
  writeFileSync(
    path.join(dir, 'greeter_test', 'greeter_test.go'),
    `package greeter_test\n\nimport (\n\t"testing"\n\n\t"factory-fixture/greeter"\n)\n\nfunc TestGreet(t *testing.T) {\n\tif got, want := greeter.Greet("Ada"), "Hello, Ada!"; got != want {\n\t\tt.Fatalf("got %q want %q", got, want)\n\t}\n}\n`,
  )
  execSync('git init -q && git add -A && git -c user.email=e2e@x -c user.name=e2e commit -qm fixture', { cwd: dir })
  return dir
}

test('full work order journey: project → order → plan → approve → build → verify → PR decision', async ({ page }) => {
  test.setTimeout(180_000)
  const repo = fixtureRepo()
  const title = `E2E journey ${Date.now()}`

  await page.goto('/')
  await expect(page.getByRole('heading', { name: /Hello/i })).toBeVisible()

  // Factory: create project
  await page.getByRole('link', { name: 'Factory' }).click()
  await page.getByRole('button', { name: 'New project' }).click()
  await page.fill('#prj-name', 'E2E Greeter')
  await page.fill('#prj-repo', repo)
  await page.getByRole('button', { name: 'Create project' }).click()
  await expect(page.getByRole('dialog')).toBeHidden()

  // create work order (auto-drafts blueprint)
  await page.getByRole('button', { name: 'New work order' }).click()
  await page.selectOption('#wo-project', { label: 'E2E Greeter' })
  await page.fill('#wo-title', title)
  await page.fill('#wo-objective', 'Implement the greeting function so the fixture contract test passes.')
  await page.fill('#wo-acceptance', 'TestGreet passes with go test ./...')
  await page.getByRole('button', { name: 'Create & draft blueprint' }).click()

  // wait for architect review with a drafted plan
  await expect(page.getByText('Record failing baseline')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('architect review')).toBeVisible()

  // approve → execution runs to human PR decision
  await page.getByRole('button', { name: 'Approve plan →' }).click()
  await expect(page.getByRole('button', { name: 'Approve PR' })).toBeVisible({ timeout: 120_000 })

  // quality evidence exists for two revisions (baseline FAIL + final PASS)
  await page.getByRole('tab', { name: 'Quality' }).click()
  await expect(page.getByText('REVISION 1')).toBeVisible()

  // findings include the scripted blocker, resolved
  await page.getByRole('tab', { name: 'Changes & PR' }).click()
  await expect(page.getByText('blocker')).toBeVisible()
  await expect(page.getByText('resolved')).toBeVisible()

  // human PR decision → READY (nothing published anywhere)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  await expect(page.getByText('ready', { exact: false })).toBeVisible({ timeout: 30_000 })
})
