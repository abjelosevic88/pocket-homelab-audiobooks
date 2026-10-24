// README screenshots and a browser smoke test, against the demo library (scripts/demo-library/make.sh: a
// throwaway Audiobookshelf with 22 public-domain books and six weeks of a demo listener's history) and a
// Pocket Audiobooks started on it:
//
//   scripts/demo-library/make.sh
//   D=~/.cache/pocket-audiobooks-dev; DATA_DIR=$D/app ADMIN_EMAIL=ana@example.com ADMIN_PASSWORD=demo-password \
//     ADMIN_NAME="Ana Petrović" AUDIOBOOKSHELF_URL=http://localhost:13399 AUDIOBOOKSHELF_TOKEN=$(cat $D/ana.key) \
//     PORT=18302 ./pocket-audiobooks
//   BASE=http://127.0.0.1:18302 node scripts/screenshots.mjs                 # Playwright's own Chromium
//   CDP=http://127.0.0.1:19241 BASE=… node scripts/screenshots.mjs           # or any Chrome with remote debugging
//   SHOTS=0 … node scripts/screenshots.mjs                                   # checks only
import { createRequire } from 'node:module'
import assert from 'node:assert/strict'

const require = createRequire(new URL('../web/package.json', import.meta.url))
let pw
try { pw = require('playwright') } catch { pw = require('playwright-core') }
const { chromium } = pw
const base = process.env.BASE || 'http://127.0.0.1:18302'
const shots = process.env.SHOTS !== '0'
const dir = new URL('../docs/screenshots/', import.meta.url).pathname
const browser = process.env.CDP ? await chromium.connectOverCDP(process.env.CDP) : await chromium.launch()
const errors = []

async function session({ phone = false, dark = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: phone ? 3 : 2, isMobile: phone, hasTouch: phone, colorScheme: dark ? 'dark' : 'light',
    userAgent: phone ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' : undefined,
  })
  const page = await ctx.newPage()
  page.on('pageerror', e => errors.push(`pageerror: ${e}`))
  page.on('console', m => { if (m.type() === 'error' && !/409|Failed to load resource/.test(m.text())) errors.push(m.text()) })
  await page.goto(base + '/login')
  const res = await page.evaluate(() => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'ana@example.com', password: 'demo-password' }) }).then(r => r.status))
  assert.equal(res, 200, 'demo sign-in')
  await page.evaluate(() => { localStorage.clear(); localStorage.setItem('pab-client', 'screenshots') })
  return { ctx, page }
}
const settle = page => page.waitForTimeout(1500)
async function go(page, path) { await page.goto(base + path, { waitUntil: 'networkidle' }); await page.evaluate(() => document.fonts.ready); await settle(page) }
async function shot(page, name) { if (shots) await page.screenshot({ path: dir + name + '.png' }) }
const api = (page, path, init) => page.evaluate(([p, i]) => fetch('/api/v1' + p, i).then(r => r.json()), [path, init])
async function bookId(page, title) {
  const r = await api(page, `/search?q=${encodeURIComponent(title)}`)
  const b = r.books.find(x => x.title === title)
  assert.ok(b, `${title} is in the demo library`)
  return b.id
}
const covers = (page, n = 4) => page.waitForFunction(n => [...document.querySelectorAll('.art')].filter(a => a.classList.contains('loaded')).length >= n, n, { timeout: 45000 }).catch(() => console.log('covers slow on', page.url()))
const playLabel = page => page.evaluate(() => document.querySelector('.pb-play')?.getAttribute('aria-label'))

// ---- desktop ----
{
  const { ctx, page } = await session()
  await go(page, '/')
  assert.ok(await page.locator('.feature').isVisible(), 'the continue card on Home')
  assert.ok(await page.locator('.shelf').count() >= 3, 'shelves on Home')
  await covers(page)
  await shot(page, 'desktop-home')

  await go(page, '/library?sort=title')
  assert.ok(await page.locator('.grid-covers .tile').count() >= 20, 'the books grid')
  await covers(page)
  await shot(page, 'desktop-library')

  await go(page, '/library/series')
  assert.ok(await page.locator('.grid-covers .tile').count() >= 3, 'series')
  await covers(page)
  await shot(page, 'desktop-series')

  // the book page, then playing: an Audiobookshelf session, files through the app, progress back to Audiobookshelf
  const dracula = await bookId(page, 'Dracula')
  await go(page, `/book/${dracula}`)
  assert.ok(await page.locator('.chapters.book li').count() >= 20, 'chapters on the book page')
  assert.ok(await page.locator('.marks li').count() >= 2, 'bookmarks on the book page')
  await covers(page, 1)
  await shot(page, 'desktop-book')
  const before = (await api(page, `/books/${dracula}`)).book.current_time
  await page.click('.book-actions .btn.primary')
  await page.waitForFunction(() => document.querySelector('.pb-play')?.getAttribute('aria-label') === 'Pause', null, { timeout: 20000 })
  await page.waitForTimeout(3000)
  // skip forward, pause: the report moves the place in Audiobookshelf
  await page.keyboard.press('ArrowRight')
  await page.waitForTimeout(800)
  await page.keyboard.press('Space')
  await page.waitForTimeout(2500)
  assert.equal(await playLabel(page), 'Play', 'space pauses')
  const after = (await api(page, `/books/${dracula}`)).book.current_time
  console.log('place:', Math.round(before), '→', Math.round(after))
  assert.ok(after > before + 20, 'the place moved forward in Audiobookshelf')
  await page.keyboard.press('Space')
  await page.waitForTimeout(1500)
  await page.keyboard.press('p')
  await page.waitForSelector('.player-full')
  await page.waitForTimeout(1200)
  await shot(page, 'desktop-player')
  await page.click('.player-full .tabs button:has-text("Chapters")')
  assert.ok(await page.locator('.player-full .chapters li').count() >= 20, 'chapters in the player')
  await page.click('.player-full .chapters li:nth-child(4) button')
  await page.waitForTimeout(1500)
  const ch = await page.evaluate(() => document.querySelector('.player-full .chapters li.cur .grow')?.textContent)
  assert.equal(ch, 'Chapter 4', 'a chapter click jumps there')
  await page.keyboard.press('Escape')
  await page.keyboard.press('Space')
  await page.waitForTimeout(1500)

  // a book in twelve files: a jump past the first file loads the right one
  const sherlock = await bookId(page, 'The Adventures of Sherlock Holmes')
  await go(page, `/book/${sherlock}`)
  await page.click('.book-actions .btn.primary')
  await page.waitForFunction(() => document.querySelector('.pb-play')?.getAttribute('aria-label') === 'Pause', null, { timeout: 45000 })
  const t0 = await page.evaluate(() => document.querySelector('.pb-info .s')?.textContent)
  const bar = await page.locator('.pb-progress').boundingBox()
  await page.mouse.click(bar.x + bar.width * 0.9, bar.y + bar.height / 2)
  await page.waitForTimeout(3000)
  const t1 = await page.evaluate(() => document.querySelector('.pb-info .s')?.textContent)
  console.log('multi-file:', t0, '→', t1)
  assert.equal(await playLabel(page), 'Pause', 'still playing after the jump to another file')
  await page.keyboard.press('Space')
  await page.waitForTimeout(1200)

  await go(page, '/stats')
  await page.click('text=3 months')
  await settle(page)
  assert.ok(await page.locator('.stat-tile').count() >= 3, 'stat tiles')
  await covers(page)
  await shot(page, 'desktop-stats')

  await go(page, '/history')
  assert.ok(await page.locator('.h-row').count() >= 10, 'history rows')
  await covers(page)
  await shot(page, 'desktop-history')

  await go(page, '/settings/audiobookshelf')
  assert.ok(await page.locator('.conn-status.ok').isVisible(), 'Audiobookshelf connected in Settings')
  await shot(page, 'desktop-settings')
  await ctx.close()
}

// ---- dark ----
{
  const { ctx, page } = await session({ dark: true })
  const hound = await bookId(page, 'The Hound of the Baskervilles')
  await go(page, `/book/${hound}`)
  await covers(page, 3)
  await shot(page, 'desktop-book-dark')
  await ctx.close()
}

// ---- phone ----
{
  const { ctx, page } = await session({ phone: true })
  await go(page, '/')
  await covers(page)
  await shot(page, 'phone-home')
  const oz = await bookId(page, 'The Marvelous Land of Oz')
  await go(page, `/book/${oz}`)
  await covers(page, 3)
  await shot(page, 'phone-book')
  await ctx.close()
}

await browser.close().catch(() => {})
if (errors.length) { console.error(errors.join('\n')); process.exit(1) }
console.log('ok')
