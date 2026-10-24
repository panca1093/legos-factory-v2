// Captures UI-validation screenshots of the running dev environment
// (backend :8080 + Vite :5173 must be up).
import { chromium } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const outDir = '../docs/screenshots'
mkdirSync(outDir, { recursive: true })

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })

async function settle(ms = 1200) {
  await page.waitForTimeout(ms)
}

await page.goto('http://localhost:5173/')
await settle(1800)
await page.screenshot({ path: `${outDir}/01-overview.png` })

await page.goto('http://localhost:5173/factory')
await settle(1200)
await page.screenshot({ path: `${outDir}/02-factory-board.png` })

// open the READY work order (Changes & PR tab)
await page.goto('http://localhost:5173/factory/wo_0c6ca2ba-d5c')
await settle(1500)
await page.getByRole('tab', { name: 'Task graph' }).click()
await settle(500)
await page.screenshot({ path: `${outDir}/03-task-graph.png` })

await page.getByRole('tab', { name: 'Quality' }).click()
await settle(500)
await page.screenshot({ path: `${outDir}/04-quality-evidence.png` })

await page.getByRole('tab', { name: 'Changes & PR' }).click()
await settle(500)
await page.screenshot({ path: `${outDir}/05-changes-pr.png` })

// new work order drawer
await page.goto('http://localhost:5173/factory')
await settle(1000)
await page.getByRole('button', { name: 'New work order' }).click()
await settle(500)
await page.screenshot({ path: `${outDir}/06-new-order-drawer.png` })

// settings
await page.goto('http://localhost:5173/settings')
await settle(1500)
await page.screenshot({ path: `${outDir}/07-settings.png` })

// mobile viewport
const mobile = await browser.newPage({ viewport: { width: 390, height: 844 } })
await mobile.goto('http://localhost:5173/')
await settle(1800)
const overflow = await mobile.evaluate(() => ({
  scrollW: document.documentElement.scrollWidth,
  innerW: window.innerWidth,
}))
console.log('mobile overflow check:', JSON.stringify(overflow))
await mobile.screenshot({ path: `${outDir}/08-mobile-overview.png` })

await mobile.goto('http://localhost:5173/factory')
await settle(1200)
const overflow2 = await mobile.evaluate(() => ({
  scrollW: document.documentElement.scrollWidth,
  innerW: window.innerWidth,
}))
console.log('mobile factory overflow check:', JSON.stringify(overflow2))
await mobile.screenshot({ path: `${outDir}/09-mobile-factory.png` })

await browser.close()
console.log('screenshots written to docs/screenshots/')
