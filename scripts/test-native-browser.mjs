import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
const require = createRequire(new URL('../web/package.json', import.meta.url))
const { chromium } = require('playwright-core')
const browser = await chromium.connectOverCDP(process.env.CDP)
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
try {
  await context.addCookies([{ name: process.env.PAB_COOKIE_NAME, value: process.env.PAB_COOKIE, url: process.env.BASE, httpOnly: true }])
  await context.addInitScript(() => { const Audio = window.Audio; window.Audio = class extends Audio { constructor(...args) { super(...args); window.__nativeAudio = this } } })
  const page = await context.newPage()
  const errors = []
  page.on('pageerror', e => errors.push(String(e)))
  await page.goto(process.env.BASE + '/book/book')
  await page.locator('.book-title').filter({ hasText: 'Native book' }).waitFor()
  await page.getByRole('button', { name: 'Play', exact: true }).first().click()
  await page.waitForFunction(() => { const a = window.__nativeAudio; return a && !a.paused && a.currentTime > 1 })
  await page.getByRole('button', { name: 'Pause', exact: true }).first().click()
  await page.waitForFunction(async () => {
    const me = await fetch('/api/v1/books/book').then(r => r.json())
    return me.book.current_time > 1
  })
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByText('Edit book', { exact: true }).click()
  await page.getByLabel('Cover image', { exact: false }).setInputFiles({ name: 'cover.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2ZAAAAABJRU5ErkJggg==', 'base64') })
  await page.getByText('Cover saved', { exact: true }).waitFor()
  await page.getByLabel('Title', { exact: true }).fill('Edited in browser')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await page.locator('.book-title').filter({ hasText: 'Edited in browser' }).waitFor()
  await page.goto(process.env.BASE + '/settings/audiobookshelf')
  await page.getByRole('heading', { name: 'Libraries', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Scan library', exact: true }).click()
  await page.waitForFunction(() => [...document.querySelectorAll('[role="status"]')].some(e => e.textContent.includes('complete')))
  // Upload through the actual web API using a real file, then browse the result.
  const result = await context.request.post(process.env.BASE + '/api/v1/upload', { multipart: {
    library: 'lib', folder: 'folder', title: 'Browser upload', author: 'Browser Author',
    '0': { name: 'Uploaded.mp3', mimeType: 'audio/mpeg', buffer: await readFile(process.env.PAB_UPLOAD) },
  } })
  assert.equal(result.status(), 201, await result.text())
  const uploaded = await result.json()
  await page.goto(process.env.BASE + '/book/' + uploaded.id)
  await page.locator('.book-title').filter({ hasText: 'Browser upload' }).waitFor()
  assert.deepEqual(errors, [])
  console.log('Native browser acceptance passed: play, pause/save, metadata edit, scan, upload and browse.')
} finally { await context.close(); await browser.close() }
