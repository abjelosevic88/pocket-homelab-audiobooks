import { useEffect, useState } from 'react'
import { V1 } from './api'

/** Live updates from the server (GET /api/v1/events, Server-Sent Events): the
 *  person's progress and sessions on other devices, and changed books. The
 *  stream reconnects with backoff; while it is down, callers fall back to
 *  polling (see `useRealtimeConnected`). */

export interface ProgressData { currentTime?: number; duration?: number; progress?: number; isFinished?: boolean; lastUpdate?: number }
export type RealtimeEvent =
  | { type: 'hello'; server_time: number }
  | { type: 'progress'; item_id: string; episode_id?: string; session_id: string; device?: string; progress: ProgressData }
  | { type: 'session'; session_id: string; closed: boolean }
  | { type: 'items'; updated: string[]; added: string[] }
  | { type: 'scan_complete'; library_id: string; added: number; updated: number; missing: number }

type Listener = (e: RealtimeEvent) => void
const listeners = new Set<Listener>()
const statusListeners = new Set<(up: boolean) => void>()
let source: EventSource | null = null
let retry: number | undefined
let backoff = 1000
let users = 0
let up = false

function setUp(v: boolean) {
  if (up === v) return
  up = v
  statusListeners.forEach(f => f(v))
}

function connect() {
  clearTimeout(retry)
  source?.close()
  const es = new EventSource(`${V1}/events`)
  source = es
  es.onopen = () => { backoff = 1000; setUp(true) }
  es.onmessage = m => {
    let ev: RealtimeEvent
    try { ev = JSON.parse(m.data) } catch { return }
    listeners.forEach(f => { try { f(ev) } catch { /* one listener must not stop the rest */ } })
  }
  es.addEventListener('unauthorized', () => {
    es.close(); setUp(false)
    window.dispatchEvent(new CustomEvent('pab:unauthorized'))
  })
  es.onerror = () => {
    setUp(false)
    // The browser retries a dropped stream by itself; a refused one (401, 5xx) is closed.
    if (es.readyState === EventSource.CLOSED && source === es) {
      retry = window.setTimeout(() => { if (users > 0) connect() }, backoff)
      backoff = Math.min(30_000, backoff * 2)
    }
  }
}

/** Keeps the stream open while at least one caller holds it (signed in). */
export function holdRealtime(): () => void {
  if (typeof EventSource === 'undefined') return () => {}
  users++
  if (users === 1) { backoff = 1000; connect() }
  let released = false
  return () => {
    if (released) return
    released = true
    users--
    if (users === 0) { clearTimeout(retry); source?.close(); source = null; setUp(false) }
  }
}

export function onRealtime(f: Listener): () => void {
  listeners.add(f)
  return () => { listeners.delete(f) }
}

export function realtimeConnected() { return up }

export function useRealtimeConnected() {
  const [v, setV] = useState(up)
  useEffect(() => {
    statusListeners.add(setV)
    setV(up)
    return () => { statusListeners.delete(setV) }
  }, [])
  return v
}
