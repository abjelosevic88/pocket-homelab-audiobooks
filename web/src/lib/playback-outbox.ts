import { api, V1 } from './api'

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
      await api.post(`${V1}/sessions/${encodeURIComponent(entry.session)}/sync`, entry.report)
      memory.delete(key)
      try { localStorage.removeItem(key) } catch { /* future retries are idempotent */ }
    }
  })()
  flights.set(uid, send)
  void send.finally(() => flights.delete(uid)).catch(() => {})
  return send
}
