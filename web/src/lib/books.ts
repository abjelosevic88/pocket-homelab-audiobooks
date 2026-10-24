// Small helpers for books, times and covers.
import type { Book } from './types'
export { V1 } from './api'

/** A cover at a width (the server asks Audiobookshelf for that size). */
export function cover(url: string | undefined, w: number): string {
  if (!url) return ''
  return url + (url.includes('?') ? '&' : '?') + 'w=' + Math.round(w * Math.min(2, window.devicePixelRatio || 1))
}

/** 1:05:09 or 5:09. */
export function clock(sec: number): string {
  sec = Math.max(0, Math.floor(sec || 0))
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = sec % 60
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}` : `${m}:${String(s).padStart(2, '0')}`
}

/** "11h 4m", "45m". */
export function hours(sec: number): string {
  sec = Math.max(0, Math.round(sec || 0))
  const h = Math.floor(sec / 3600), m = Math.round((sec % 3600) / 60)
  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`
  if (m > 0) return `${m}m`
  return sec > 0 ? `${sec}s` : '0m'
}

/** What is left of a book at the person's speed: "3h 12m left". */
export function left(b: Pick<Book, 'duration' | 'current_time'>, speed = 1): string {
  const s = Math.max(0, (b.duration - b.current_time) / (speed || 1))
  return `${hours(s)} left`
}

export function bookSub(b: Book): string {
  return b.author || b.narrator || ''
}

/** "Book 2 of The Stormlight Archive" style line from a series string like "Name #2". */
export function seriesLine(b: Book): string {
  if (b.series_list && b.series_list.length) {
    const s = b.series_list[0]
    return s.sequence ? `${s.name} · Book ${s.sequence}` : s.name
  }
  return b.series || ''
}

export function hue(s: string): number { let h = 0; for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0; return Math.abs(h) % 360 }

/** "Chrome on macOS" — names this browser in Audiobookshelf's sessions. */
export function deviceName(): string {
  const ua = navigator.userAgent
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Browser'
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : ''
  return os ? `${b} on ${os}` : b
}

/** The audio types this browser plays, as Audiobookshelf names them. */
export function playableTypes(): string[] {
  const a = document.createElement('audio')
  const cands: [string, string][] = [
    ['audio/mpeg', 'audio/mpeg'], ['audio/mp4', 'audio/mp4; codecs="mp4a.40.2"'], ['audio/x-m4a', 'audio/mp4'], ['audio/x-m4b', 'audio/mp4'], ['audio/aac', 'audio/aac'],
    ['audio/flac', 'audio/flac'], ['audio/x-flac', 'audio/flac'], ['audio/ogg', 'audio/ogg; codecs="vorbis"'], ['audio/opus', 'audio/ogg; codecs="opus"'],
    ['audio/webm', 'audio/webm; codecs="opus"'], ['audio/wav', 'audio/wav'], ['audio/x-wav', 'audio/wav'],
  ]
  let failed: string[] = []
  try { failed = JSON.parse(localStorage.getItem(FAILED_KEY) || '[]') } catch { /* ignore */ }
  const out = cands.filter(([n, t]) => a.canPlayType(t) !== '' && !failed.includes(n)).map(([n]) => n)
  return out.length ? out : ['audio/mp4']
}

const FAILED_KEY = 'pab-undecodable'
/** Remember a type this browser said it plays but couldn't decode: Audiobookshelf converts it from then on. */
export function markUndecodable(mime: string) {
  if (!mime) return
  try {
    const l: string[] = JSON.parse(localStorage.getItem(FAILED_KEY) || '[]')
    if (!l.includes(mime)) localStorage.setItem(FAILED_KEY, JSON.stringify([...l, mime]))
  } catch { /* ignore */ }
}

/** This browser's stable id: Audiobookshelf keeps one session per person and device. */
export const CLIENT_ID = (() => {
  try { let id = localStorage.getItem('pab-client'); if (!id) { id = Math.random().toString(36).slice(2, 14); localStorage.setItem('pab-client', id) } return id }
  catch { return Math.random().toString(36).slice(2, 14) }
})()
