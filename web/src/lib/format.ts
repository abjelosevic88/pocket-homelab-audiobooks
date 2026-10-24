
export function todayISO(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
export function addDays(iso: string, n: number): string {
  const [y, m, d] = iso.split('-').map(Number)
  const t = new Date(y, m - 1, d + n)
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`
}
export function daysBetween(a: string, b: string): number {
  if (!a || !b) return 0
  const [y1, m1, d1] = a.split('-').map(Number), [y2, m2, d2] = b.split('-').map(Number)
  return Math.round((Date.UTC(y2, m2 - 1, d2) - Date.UTC(y1, m1 - 1, d1)) / 86400000)
}
/** Every date from a to b inclusive. */
export function dateRange(a: string, b: string): string[] {
  const out: string[] = []
  if (!a || !b || b < a) return out
  for (let d = a, i = 0; d <= b && i < 400; d = addDays(d, 1), i++) out.push(d)
  return out
}
export function weekday(iso: string, long = false): string {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(y, m - 1, d).toLocaleDateString(undefined, { weekday: long ? 'long' : 'short' })
}

// ---- dates: the format comes from Settings → General and is set once by the app context.
export type DateFormat = 'auto' | 'dmy-dot' | 'dmy-slash' | 'mdy-slash' | 'ymd' | 'dmmmy' | 'mmmdy'
export type TimeFormat = 'auto' | '24h' | '12h'
let DATE_FMT: DateFormat = 'auto'
let TIME_FMT: TimeFormat = 'auto'
export function setDateFormats(d: DateFormat, t: TimeFormat) { DATE_FMT = d; TIME_FMT = t }
export const DATE_FORMATS: { id: DateFormat; label: string }[] = [
  { id: 'auto', label: 'Browser locale' }, { id: 'dmy-dot', label: '31.12.2026' }, { id: 'dmy-slash', label: '31/12/2026' },
  { id: 'mdy-slash', label: '12/31/2026' }, { id: 'ymd', label: '2026-12-31' }, { id: 'dmmmy', label: '31 Dec 2026' }, { id: 'mmmdy', label: 'Dec 31, 2026' },
]
export const TIME_FORMATS: { id: TimeFormat; label: string }[] = [{ id: 'auto', label: 'Browser locale' }, { id: '24h', label: '17:05' }, { id: '12h', label: '5:05 PM' }]
const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
export function fmtYMD(y: number, m: number, d: number, withYear = true, fmt: DateFormat = DATE_FMT): string {
  const dd = String(d).padStart(2, '0'), mm = String(m).padStart(2, '0')
  switch (fmt) {
    case 'dmy-dot': return withYear ? `${dd}.${mm}.${y}` : `${dd}.${mm}.`
    case 'dmy-slash': return withYear ? `${dd}/${mm}/${y}` : `${dd}/${mm}`
    case 'mdy-slash': return withYear ? `${mm}/${dd}/${y}` : `${mm}/${dd}`
    case 'ymd': return withYear ? `${y}-${mm}-${dd}` : `${mm}-${dd}`
    case 'dmmmy': return withYear ? `${d} ${MON[m - 1]} ${y}` : `${d} ${MON[m - 1]}`
    case 'mmmdy': return withYear ? `${MON[m - 1]} ${d}, ${y}` : `${MON[m - 1]} ${d}`
    default: return new Date(y, m - 1, d).toLocaleDateString(undefined, withYear ? { day: 'numeric', month: 'short', year: 'numeric' } : { day: 'numeric', month: 'short' })
  }
}
export function fmtDate(d: string, withYear = true): string {
  if (!d) return ''
  const [y, m, day] = d.slice(0, 10).split('-').map(Number)
  return fmtYMD(y, m, day, withYear)
}
/** "14 Aug – 28 Aug 2026" */
export function fmtRange(a: string, b: string): string {
  if (!a && !b) return 'no dates yet'
  if (!b || a === b) return fmtDate(a || b)
  const sameYear = a.slice(0, 4) === b.slice(0, 4)
  return `${fmtDate(a, !sameYear)} – ${fmtDate(b)}`
}
export function fmtTime(t: Date, fmt: TimeFormat = TIME_FMT): string {
  const h = t.getHours(), mi = String(t.getMinutes()).padStart(2, '0')
  switch (fmt) {
    case '24h': return `${String(h).padStart(2, '0')}:${mi}`
    case '12h': return `${h % 12 || 12}:${mi} ${h < 12 ? 'AM' : 'PM'}`
    default: return t.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  }
}
/** "HH:MM" wall time → formatted. */
export function fmtClock(hhmm: string): string {
  if (!hhmm || hhmm.length < 5) return ''
  const t = new Date(2000, 0, 1, Number(hhmm.slice(0, 2)), Number(hhmm.slice(3, 5)))
  return fmtTime(t)
}
/** "2026-08-28T07:30" (wall) or ISO → "28 Aug 2026 07:30". */
export function fmtDateTime(s: string, withYear = true): string {
  if (!s) return ''
  if (s.length === 16 && s[10] === 'T') return `${fmtDate(s, withYear)} ${fmtClock(s.slice(11))}`
  const t = new Date(s)
  if (Number.isNaN(t.getTime())) return s
  return `${fmtYMD(t.getFullYear(), t.getMonth() + 1, t.getDate(), withYear)} ${fmtTime(t)}`
}
export function fmtISOTime(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : fmtTime(t)
}
export function ago(iso: string): string {
  if (!iso) return 'never'
  const t = new Date(iso).getTime()
  const s = Math.max(0, (Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  if (s < 86400 * 7) return `${Math.floor(s / 86400)} d ago`
  return fmtDateTime(iso)
}
export function fmtDuration(min: number): string {
  if (!min) return ''
  const h = Math.floor(min / 60), m = min % 60
  return h ? (m ? `${h} h ${m} min` : `${h} h`) : `${m} min`
}
export function fmtKm(km: number, unit: 'km' | 'mi' = 'km'): string {
  if (unit === 'mi') return `${(km * 0.621371).toFixed(km < 10 ? 1 : 0)} mi`
  return `${km.toFixed(km < 10 ? 1 : 0)} km`
}
export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
export function plural(n: number, one: string, many = one + 's'): string { return `${n.toLocaleString()} ${n === 1 ? one : many}` }
export function initials(name: string): string {
  const p = name.replace(/@.*/, '').split(/[\s._-]+/).filter(Boolean)
  return ((p[0]?.[0] || '?') + (p[1]?.[0] || '')).toUpperCase()
}
const palette = ['#0284c7', '#0d9488', '#d97706', '#db2777', '#2563eb', '#16a34a', '#0d9488', '#ea580c']
export function colorFor(id: number): string { return palette[Math.abs(id) % palette.length] }
export const TRIP_COLORS = ['#0284c7', '#0d9488', '#d97706', '#db2777', '#16a34a', '#0d9488', '#ea580c', '#dc2626', '#475569']
export function stars(r: number | null | undefined): string { return r ? '★'.repeat(Math.round(r)) + '☆'.repeat(5 - Math.round(r)) : '' }
export function cap(s: string): string { return s ? s[0].toUpperCase() + s.slice(1).replace(/[-_]/g, ' ') : '' }
export function gmaps(lat: number, lng: number): string { return `https://www.google.com/maps/dir/?api=1&destination=${lat},${lng}` }
export function appleMaps(lat: number, lng: number): string { return `https://maps.apple.com/?daddr=${lat},${lng}` }
export function osmLink(lat: number, lng: number): string { return `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lng}#map=16/${lat}/${lng}` }
