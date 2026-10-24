import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
const require = createRequire(new URL('../web/package.json', import.meta.url))
const { build } = require('esbuild')
const compiled = await build({ entryPoints: [new URL('../web/src/lib/playback-outbox.ts', import.meta.url).pathname], bundle: true, format: 'esm', write: false })
const code = compiled.outputFiles[0].text
class Storage {
  entries = new Map()
  get length() { return this.entries.size }
  key(i) { return [...this.entries.keys()][i] ?? null }
  getItem(key) { return this.entries.get(key) ?? null }
  setItem(key, value) { this.entries.set(key, value) }
  removeItem(key) { this.entries.delete(key) }
}
globalThis.localStorage = new Storage()
const load = suffix => import(`data:text/javascript;base64,${Buffer.from(code + '\n//' + suffix).toString('base64')}`)
const receipt = (id, seconds = 15) => ({ report_id: id, observed_at: 1000, current_time: 30, listened: seconds, duration: 100, playing: true, item_id: 'book', client: 'browser' })
test('lost acknowledgement survives reload, preserves receipt and isolates accounts', async () => {
  const one = await load('first')
  const applied = new Set(), received = []
  let loseResponse = true, counted = 0
  globalThis.fetch = async (url, options) => {
    const report = JSON.parse(options.body)
    received.push(report)
    if (!applied.has(report.report_id)) { applied.add(report.report_id); counted += report.listened }
    if (loseResponse) { loseResponse = false; throw Error('response lost after commit') }
    return { ok: true, status: 200, json: async () => ({ ok: true }) }
  }
  one.queueReport(1, 'session', receipt('one'))
  one.queueReport(2, 'other-session', receipt('other-account', 900))
  await assert.rejects(one.flushReports(1, () => true))
  assert.equal(counted, 15)
  const reloaded = await load('reload')
  await reloaded.flushReports(1, () => true)
  assert.equal(counted, 15)
  assert.deepEqual(received[0], received[1])
  assert.equal(localStorage.length, 1)
  await reloaded.flushReports(2, () => false)
  assert.equal(counted, 15)
})
test('a close queued during a pending request is drained before reopening playback', async () => {
  localStorage.entries.clear()
  const box = await load('concurrent')
  let unblock
  const gate = new Promise(resolve => { unblock = resolve })
  const seen = []
  globalThis.fetch = async (_, options) => {
    const body = JSON.parse(options.body); seen.push(body.report_id)
    if (seen.length === 1) await gate
    return { ok: true, status: 200, json: async () => ({ ok: true }) }
  }
  box.queueReport(1, 'session', receipt('periodic'))
  const first = box.flushReports(1, () => true)
  box.queueReport(1, 'session', { ...receipt('close'), close: true })
  const second = box.flushReports(1, () => true)
  unblock()
  await Promise.all([first, second])
  assert.deepEqual(seen, ['periodic', 'close'])
  assert.equal(localStorage.length, 0)
})
