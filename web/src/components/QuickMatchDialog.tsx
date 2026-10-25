// Bulk quick match: a background job over a library or a selection, with
// progress, cancellation and a per-book report.
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { Modal, Segmented, errMsg, useToast } from './ui'
import '../styles/match.css'

export type MatchOptions = { override_details: boolean; override_cover: boolean; update_authors: boolean; update_series: boolean }
type Job = { id: string; library_id: string; status: string; total: number; done: number; error: string; created_at: number; options: MatchOptions; counts: Record<string, number> }
type Result = { item_id: string; title: string; status: string; message?: string; provider?: string; match_title?: string; match_author?: string; score?: number; exact?: boolean; fields?: string[]; cover?: boolean }

const STATUS: Record<string, [string, string]> = { matched: ['Matched', 'st-ok'], skipped: ['Skipped', ''], low_confidence: ['Low confidence', 'st-warn'], not_found: ['No result', ''], failed: ['Failed', 'st-danger'] }
const DEFAULTS: MatchOptions = { override_details: false, override_cover: false, update_authors: false, update_series: false }

export function MatchOptionsFields({ value, onChange }: { value: MatchOptions; onChange: (v: MatchOptions) => void }) {
  const opt = (k: keyof MatchOptions, label: string) => <label className="check-row"><input type="checkbox" checked={value[k]} onChange={e => onChange({ ...value, [k]: e.target.checked })} /> {label}</label>
  return <fieldset className="match-options"><legend className="small muted">By default only empty fields and missing covers are filled.</legend>
    {opt('override_details', 'Replace existing details (title, description, narrators, genres…)')}
    {opt('override_cover', 'Replace existing covers')}
    {opt('update_authors', 'Replace existing authors')}
    {opt('update_series', 'Replace existing series')}
  </fieldset>
}

export function QuickMatchDialog({ library, ids, onClose, onDone }: { library: string; ids: string[]; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [options, setOptions] = useState<MatchOptions>(DEFAULTS)
  const [job, setJob] = useState<Job>()
  const [results, setResults] = useState<Result[]>([])
  const [filter, setFilter] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const finished = useRef(false)

  const load = async (id: string, f = filter) => {
    const r = await api.get<{ job: Job; results: Result[] }>(`${V1}/catalog/match-jobs/${id}?${new URLSearchParams({ status: f, limit: '500' })}`)
    setJob(r.job); setResults(r.results)
    return r.job
  }
  // Resume this library's running (or latest) job when opened without a selection.
  useEffect(() => {
    if (ids.length) return
    api.get<Job[]>(`${V1}/catalog/match-jobs?library=${encodeURIComponent(library)}`).then(list => { if (list[0]) void load(list[0].id) }).catch(() => {})
  }, [library]) // eslint-disable-line react-hooks/exhaustive-deps
  const active = job && (job.status === 'running' || job.status === 'queued')
  useEffect(() => {
    if (!job || !active) return
    const t = window.setInterval(() => { void load(job.id).then(j => { if (j.status !== 'running' && j.status !== 'queued' && !finished.current) { finished.current = true; onDone(); toast(`Quick match ${j.status === 'complete' ? 'finished' : j.status}`) } }).catch(e => setError(errMsg(e))) }, 1500)
    return () => window.clearInterval(t)
  }, [job?.id, active]) // eslint-disable-line react-hooks/exhaustive-deps

  const start = async () => {
    setBusy(true); setError(''); finished.current = false
    try {
      const r = await api.post<{ id: string }>(`${V1}/catalog/match-jobs`, ids.length ? { ids, ...options } : { library_id: library, ...options })
      setFilter(''); await load(r.id, '')
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const cancel = async () => { if (!job) return; try { await api.del(`${V1}/catalog/match-jobs/${job.id}`); await load(job.id) } catch (e) { setError(errMsg(e)) } }
  const pct = job && job.total ? Math.round(job.done / job.total * 100) : 0
  const scope = ids.length ? `${ids.length} selected ${ids.length === 1 ? 'book' : 'books'}` : 'every book in this library'

  return <Modal title="Quick match" size="lg" onClose={onClose}>
    <div className="stack">
      {error && <p role="alert" className="callout danger">{error}</p>}
      {!active && <>
        <p>Search the metadata providers for {scope} and apply only confident matches (same title and author, or the same ASIN/ISBN). Requests to Audible are spaced about a second apart, so large libraries take a while; you can close this window.</p>
        <MatchOptionsFields value={options} onChange={setOptions} />
        <div className="row"><button type="button" className="btn primary" disabled={busy} onClick={start}>{busy ? 'Starting…' : job ? 'Run again' : 'Start quick match'}</button></div>
      </>}
      {job && <section className="stack" aria-live="polite">
        <div className="row"><b className="grow">{active ? `Matching… ${job.done} of ${job.total}` : job.status === 'complete' ? `Finished · ${job.total} books` : job.status === 'cancelled' ? `Cancelled after ${job.done} of ${job.total}` : `Stopped: ${job.error || job.status}`}</b>{active && <button type="button" className="btn danger" onClick={cancel}>Cancel</button>}</div>
        <span className="match-progress" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}><span style={{ width: `${pct}%` }} /></span>
        <Segmented size="sm" label="Show" value={filter} onChange={f => { setFilter(f); void load(job.id, f) }} options={[{ value: '', label: `All ${job.done}` }, ...Object.entries(STATUS).filter(([k]) => job.counts[k]).map(([k, [label]]) => ({ value: k, label: `${label} ${job.counts[k]}` }))]} />
        {results.length > 0 && <div className="match-results">{results.map(r => <div className="match-result" key={r.item_id}>
          <span className={`badge ${STATUS[r.status]?.[1] || ''}`}>{STATUS[r.status]?.[0] || r.status}</span>
          <div className="grow"><Link to={`/book/${encodeURIComponent(r.item_id)}`} onClick={onClose}>{r.title || r.item_id}</Link>
            <div className="small muted">{r.match_title && <>{r.status === 'matched' ? 'Matched' : 'Best'}: {r.match_title}{r.match_author ? ` — ${r.match_author}` : ''}{r.exact ? ' (exact ID)' : r.score ? ` (${Math.round(r.score * 100)}%)` : ''}. </>}{r.fields?.length ? `Updated ${r.fields.join(', ')}${r.cover ? ', cover' : ''}. ` : r.cover ? 'Added cover. ' : ''}{r.message}</div></div>
        </div>)}</div>}
      </section>}
    </div>
  </Modal>
}
