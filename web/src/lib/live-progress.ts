import { useCallback, useSyncExternalStore } from 'react'
import type { Book } from './types'

/** Listening progress that moved after a list was loaded: this tab's player
 *  and other devices (the realtime stream) both announce it as `pab:progress`.
 *  Grids overlay it on the books they show, so progress bars and finished
 *  badges follow along without reloading the page. */

interface Live { time: number; progress?: number; finished: boolean; at: number }
type Detail = { id?: string; time?: number; ended?: boolean; progress?: number }

const live = new Map<string, Live>()
const subs = new Map<string, Set<() => void>>()
const finishedSubs = new Set<(id: string) => void>()
let listening = false

function listen() {
  if (listening || typeof window === 'undefined') return
  listening = true
  window.addEventListener('pab:progress', e => {
    const d = (e as CustomEvent<Detail>).detail
    if (!d?.id) return
    const prev = live.get(d.id)
    const finished = !!d.ended
    live.set(d.id, { time: d.time ?? 0, progress: d.progress, finished, at: Date.now() })
    subs.get(d.id)?.forEach(f => f())
    if (finished && !prev?.finished) finishedSubs.forEach(f => f(d.id!))
  })
}

/** The book with any newer progress applied. Data the server sent after the
 *  event (a reload) wins: `last_played` is newer than the event then. */
export function useLiveBook(b: Book): Book {
  listen()
  const id = b.id
  const subscribe = useCallback((cb: () => void) => {
    let s = subs.get(id)
    if (!s) subs.set(id, s = new Set())
    s.add(cb)
    return () => { s!.delete(cb); if (!s!.size) subs.delete(id) }
  }, [id])
  const v = useSyncExternalStore(subscribe, () => live.get(id))
  if (!v || (b.last_played && b.last_played >= v.at)) return b
  const progress = v.finished ? 1 : v.progress ?? (b.duration ? Math.min(1, v.time / b.duration) : b.progress)
  return { ...b, current_time: v.time, progress, finished: v.finished || (b.finished && !v.time), last_played: v.at }
}

/** Calls `f` when a book is newly finished anywhere (series counts change). */
export function onBookFinished(f: (id: string) => void): () => void {
  listen()
  finishedSubs.add(f)
  return () => { finishedSubs.delete(f) }
}
