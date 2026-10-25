// Author profiles (native library): match one author on Audible (Audnexus),
// edit name/bio/ASIN/photo, and match every author of a library in the background.
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, upload, V1 } from '../lib/api'
import { Field, Modal, Segmented, errMsg, useToast } from './ui'
import '../styles/match.css'

export type NativeAuthor = { id: string; name: string; asin: string; description: string; image: string; has_image: boolean; last_matched: number; updated_at: number; books: number; can_edit: boolean }
type MatchResult = { author_id: string; name: string; status: string; message?: string; asin?: string; match_name?: string; distance?: number; fields?: string[]; image?: boolean }
type Job = { id: string; library_id: string; status: string; total: number; done: number; error: string; created_at: number; options: { only_missing?: boolean }; counts: Record<string, number> }

const STATUS: Record<string, [string, string]> = { matched: ['Matched', 'st-ok'], skipped: ['Unchanged', ''], not_found: ['No result', ''], failed: ['Failed', 'st-danger'] }
const authorURL = (id: string) => `${V1}/catalog/authors/${encodeURIComponent(id)}`

function describe(r: MatchResult): string {
  const parts: string[] = []
  if (r.match_name) parts.push(`${r.status === 'matched' ? 'Matched' : 'Found'}: ${r.match_name}${r.asin ? ` (${r.asin})` : ''}.`)
  const changed = [...(r.fields || []).map(f => f === 'description' ? 'biography' : f === 'asin' ? 'ASIN' : f), ...(r.image ? ['photo'] : [])]
  if (changed.length) parts.push(`Updated ${changed.join(', ')}.`)
  if (r.message) parts.push(r.message.endsWith('.') ? r.message : `${r.message}.`)
  return parts.join(' ')
}

/** Match one author by name (closest Audible author) or by an exact ASIN. */
export function AuthorMatchDialog({ author, onClose, onSaved }: { author: NativeAuthor; onClose: () => void; onSaved: (a: NativeAuthor) => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(author.name)
  const [asin, setAsin] = useState(author.asin)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<MatchResult>()
  const run = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setError(''); setResult(undefined)
    try {
      const body = asin.trim() && asin.trim().toUpperCase() !== author.asin ? { asin: asin.trim() } : { name: name.trim() }
      const r = await api.post<{ result: MatchResult; author?: NativeAuthor }>(`${authorURL(author.id)}/match`, body)
      setResult(r.result)
      if (r.author) onSaved(r.author)
      if (r.result.status === 'matched') { toast('Author updated'); onClose() }
    } catch (err) { setError(errMsg(err)) } finally { setBusy(false) }
  }
  return <Modal title="Match author" onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Close</button><button type="submit" form="author-match" className="btn primary" disabled={busy}>{busy ? 'Searching…' : 'Match'}</button></>}>
    <form id="author-match" className="stack" onSubmit={run}>
      <p className="muted small">Looks the author up on Audible (via Audnexus) and saves their ASIN, biography and photo. An ASIN finds the exact author; otherwise the closest name is used.</p>
      {error && <p role="alert" className="callout danger">{error}</p>}
      <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} maxLength={500} /></Field>
      <Field label="ASIN" help="Optional, for example B001IGFHW6."><input value={asin} onChange={e => setAsin(e.target.value)} maxLength={10} spellCheck={false} /></Field>
      {result && result.status !== 'matched' && <p className="callout" role="status">{describe(result) || 'No change.'}</p>}
    </form>
  </Modal>
}

/** Edit an author's name, ASIN, biography and photo. */
export function AuthorEditDialog({ author, onClose, onSaved }: { author: NativeAuthor; onClose: () => void; onSaved: (a: NativeAuthor) => void }) {
  const { toast } = useToast()
  const [form, setForm] = useState({ name: author.name, asin: author.asin, description: author.description })
  const [photoURL, setPhotoURL] = useState('')
  const [current, setCurrent] = useState(author)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const saved = (a: NativeAuthor) => { setCurrent(a); onSaved(a) }
  const guard = async (fn: () => Promise<void>) => { setBusy(true); setError(''); try { await fn() } catch (e) { setError(errMsg(e)) } finally { setBusy(false) } }
  const save = (e: React.FormEvent) => {
    e.preventDefault()
    void guard(async () => {
      const body: Record<string, unknown> = {}
      if (form.name.trim() !== author.name) body.name = form.name
      if (form.asin.trim().toUpperCase() !== author.asin) body.asin = form.asin
      if (form.description !== author.description) body.description = form.description
      if (photoURL.trim()) body.image_url = photoURL.trim()
      saved(await api.put<NativeAuthor>(authorURL(author.id), body))
      toast('Author saved'); onClose()
    })
  }
  const pick = (f?: File) => { if (f) void guard(async () => { saved(await upload<NativeAuthor>(`${authorURL(author.id)}/image`, 'file', f)); toast('Photo saved') }) }
  const remove = () => void guard(async () => { saved(await api.del<NativeAuthor>(`${authorURL(author.id)}/image`)); toast('Photo removed') })
  return <Modal title="Edit author" onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="author-edit" className="btn primary" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button></>}>
    <form id="author-edit" className="stack" onSubmit={save}>
      {error && <p role="alert" className="callout danger">{error}</p>}
      <div className="row">
        <span className="author-edit-photo">{current.image ? <img src={current.image} alt="" /> : <span className="muted small">No photo</span>}</span>
        <div className="stack grow">
          <Field label="Photo" help="JPEG, PNG or WebP up to 10 MB. Saved when selected."><input type="file" accept="image/jpeg,image/png,image/webp" disabled={busy} onChange={e => pick(e.target.files?.[0])} /></Field>
          {current.has_image && <div><button type="button" className="btn sm danger" disabled={busy} onClick={remove}>Remove photo</button></div>}
        </div>
      </div>
      <Field label="Photo URL" help="Downloaded when you save."><input type="url" value={photoURL} onChange={e => setPhotoURL(e.target.value)} placeholder="https://" /></Field>
      <Field label="Name" help="Renaming updates every book by this author."><input required value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} maxLength={512} /></Field>
      <Field label="ASIN"><input value={form.asin} onChange={e => setForm({ ...form, asin: e.target.value })} maxLength={10} spellCheck={false} /></Field>
      <Field label="Biography"><textarea rows={8} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} /></Field>
    </form>
  </Modal>
}

/** Match every author of a library in the background, with progress and a report. */
export function AuthorMatchAllDialog({ library, onClose, onDone }: { library: string; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [onlyMissing, setOnlyMissing] = useState(true)
  const [job, setJob] = useState<Job>()
  const [results, setResults] = useState<MatchResult[]>([])
  const [filter, setFilter] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const finished = useRef(false)
  const load = async (id: string, f = filter) => {
    const r = await api.get<{ job: Job; results: MatchResult[] }>(`${V1}/catalog/author-match-jobs/${id}?${new URLSearchParams({ status: f, limit: '500' })}`)
    setJob(r.job); setResults(r.results)
    return r.job
  }
  useEffect(() => {
    api.get<Job[]>(`${V1}/catalog/author-match-jobs?library=${encodeURIComponent(library)}`).then(list => { if (list[0]) void load(list[0].id) }).catch(() => {})
  }, [library]) // eslint-disable-line react-hooks/exhaustive-deps
  const active = job && (job.status === 'running' || job.status === 'queued')
  useEffect(() => {
    if (!job || !active) return
    const t = window.setInterval(() => { void load(job.id).then(j => { if (j.status !== 'running' && j.status !== 'queued' && !finished.current) { finished.current = true; onDone(); toast(`Author match ${j.status === 'complete' ? 'finished' : j.status}`) } }).catch(e => setError(errMsg(e))) }, 1500)
    return () => window.clearInterval(t)
  }, [job?.id, active]) // eslint-disable-line react-hooks/exhaustive-deps
  const start = async () => {
    setBusy(true); setError(''); finished.current = false
    try {
      const r = await api.post<{ id: string }>(`${V1}/catalog/author-match-jobs`, { library_id: library, only_missing: onlyMissing })
      setFilter(''); await load(r.id, '')
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const cancel = async () => { if (!job) return; try { await api.del(`${V1}/catalog/author-match-jobs/${job.id}`); await load(job.id) } catch (e) { setError(errMsg(e)) } }
  const pct = job && job.total ? Math.round(job.done / job.total * 100) : 0
  return <Modal title="Match all authors" size="lg" onClose={onClose}>
    <div className="stack">
      {error && <p role="alert" className="callout danger">{error}</p>}
      {!active && <>
        <p>Look up the authors of this library on Audible and save their photos and biographies. Authors already matched by ASIN are looked up directly, others by the closest name. Requests are spaced about a second apart; you can close this window.</p>
        <label className="check-row"><input type="checkbox" checked={onlyMissing} onChange={e => setOnlyMissing(e.target.checked)} /> Only authors without a photo or biography (keeps existing ones)</label>
        <div className="row"><button type="button" className="btn primary" disabled={busy} onClick={start}>{busy ? 'Starting…' : job ? 'Run again' : 'Start'}</button></div>
      </>}
      {job && <section className="stack" aria-live="polite">
        <div className="row"><b className="grow">{active ? `Matching… ${job.done} of ${job.total}` : job.status === 'complete' ? `Finished · ${job.total} ${job.total === 1 ? 'author' : 'authors'}` : job.status === 'cancelled' ? `Cancelled after ${job.done} of ${job.total}` : `Stopped: ${job.error || job.status}`}</b>{active && <button type="button" className="btn danger" onClick={cancel}>Cancel</button>}</div>
        <span className="match-progress" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}><span style={{ width: `${pct}%` }} /></span>
        <Segmented size="sm" label="Show" value={filter} onChange={f => { setFilter(f); void load(job.id, f) }} options={[{ value: '', label: `All ${job.done}` }, ...Object.entries(STATUS).filter(([k]) => job.counts[k]).map(([k, [label]]) => ({ value: k, label: `${label} ${job.counts[k]}` }))]} />
        {results.length > 0 && <div className="match-results">{results.map(r => <div className="match-result" key={r.author_id}>
          <span className={`badge ${STATUS[r.status]?.[1] || ''}`}>{STATUS[r.status]?.[0] || r.status}</span>
          <div className="grow"><Link to={`/author/${encodeURIComponent(r.author_id)}`} onClick={onClose}>{r.name || r.author_id}</Link>
            <div className="small muted">{describe(r)}</div></div>
        </div>)}</div>}
      </section>}
    </div>
  </Modal>
}
