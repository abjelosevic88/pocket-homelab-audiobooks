import { api, ApiError, V1 } from './api'

export interface PlaybackReport {
  report_id: string
  observed_at: number
  current_time: number
  listened: number
  duration: number
  playing: boolean
  close?: boolean
  ended?: boolean
  item_id: string
  client: string
  device?: string
}
interface Pending { session: string; report: PlaybackReport }
const prefix = (uid: number) => `pab:reports:${uid}:`
const memory = new Map<string, Pending>()
const flights = new Map<number, Promise<void>>()
// The server will never accept these; retrying them would block every later
// report behind them. Network failures and 5xx keep the entry for a retry.
const PERMANENT = new Set([400, 404, 409, 410, 422])
let endedHandler: ((session: string) => void) | null = null

/** Called when the server says a session had already ended (another device
 *  opened the book, or progress was edited): the player then reopens one. */
export function onSessionEnded(fn: ((session: string) => void) | null) { endedHandler = fn }

// One entry per receipt avoids tabs overwriting each other's queues. No tokens
// are stored. Re-sending a receipt is safe even if a beacon reached the server.
export function queueReport(uid: number, session: string, report: PlaybackReport) {
  const key = prefix(uid) + report.report_id
  const entry = { session, report }
  memory.set(key, entry)
  try { localStorage.setItem(key, JSON.stringify(entry)) } catch { /* memory fallback if storage is unavailable */ }
}
function pending(uid: number) {
  const entries = new Map<string, Pending>()
  for (const [key, value] of memory) if (key.startsWith(prefix(uid))) entries.set(key, value)
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (!key?.startsWith(prefix(uid))) continue
      const value = JSON.parse(localStorage.getItem(key) || 'null') as Pending | null
      if (value?.session && value.report?.report_id) entries.set(key, value)
    }
  } catch { /* memory entries remain available */ }
  return [...entries].sort((a, b) => a[1].report.observed_at - b[1].report.observed_at)
}
export function flushReports(uid: number, active: () => boolean): Promise<void> {
  const flight = flights.get(uid)
  if (flight) return flight
  const send = (async () => {
    while (true) {
      const entryPair = pending(uid)[0]
      if (!entryPair) return
      const [key, entry] = entryPair
      if (!active()) return
      // Retain the exact payload until acknowledged, including its timestamp.
      try {
        const r = await api.post<{ session_ended?: boolean }>(`${V1}/sessions/${encodeURIComponent(entry.session)}/sync`, entry.report)
        if (r?.session_ended && !entry.report.close && !entry.report.ended) endedHandler?.(entry.session)
      } catch (e) {
        if (!(e instanceof ApiError) || !PERMANENT.has(e.status)) throw e
        console.warn('playback report rejected, dropping it', e.status, e.message, entry.report)
      }
      memory.delete(key)
      try { localStorage.removeItem(key) } catch { /* future retries are idempotent */ }
    }
  })()
  flights.set(uid, send)
  void send.finally(() => flights.delete(uid)).catch(() => {})
  return send
}
