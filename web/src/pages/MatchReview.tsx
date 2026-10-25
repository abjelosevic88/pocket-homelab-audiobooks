// Review matches: walk the uncertain results of a bulk quick match one book at
// a time. The book is on the left, the provider's top candidates (searched
// again live, cached for this visit) on the right. Accept applies the chosen
// fields through the regular match endpoint; Skip just marks the book reviewed.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { hours } from '../lib/books'
import { show, type MatchCandidate, type MatchScore } from '../components/MatchDialog'
import { Empty, Kbd, Loading, PageHeader, Segmented, errMsg, modalOpen, useToast } from '../components/ui'
import '../styles/match.css'
import '../styles/review.css'

type Named = { name: string; sequence?: string }
type Meta = Record<string, unknown>
type Review = '' | 'accepted' | 'skipped'
type Entry = { position: number; item_id: string; title: string; status: string; message?: string; provider?: string; match_id?: string; match_title?: string; match_author?: string; score?: number; exact?: boolean; review: Review; reviewed_at: number }
type Counts = { total: number; reviewed: number; accepted: number; skipped: number; pending: number }
type Job = { id: string; library_id: string; status: string; total: number; created_at: number; counts: Record<string, number> }
type Queue = { job: Job; entries: Entry[]; counts: Counts; statuses: Record<string, number> }
type Score = MatchScore & { edition?: boolean }
type Cand = Omit<MatchCandidate, 'score'> & { score?: Score; dramatized?: boolean }
type Item = { id: string; title: string; author: string; duration: number; expected_updated_at: number; has_cover: boolean; metadata: Meta; path?: string; dramatized?: boolean }
type Search = { item: Item; query: { title: string; author: string }; provider?: string; candidates: Cand[]; errors: string[] }
type Provider = { id: string; name: string; kind: string }
type Query = { title: string; author: string; provider: string }

// The fields quick match may write (tags are left out: they control access).
const FIELDS: [string, string][] = [['title', 'Title'], ['subtitle', 'Subtitle'], ['authors', 'Authors'], ['narrators', 'Narrators'], ['series', 'Series'], ['description', 'Description'], ['publisher', 'Publisher'], ['publishedYear', 'Year'], ['language', 'Language'], ['genres', 'Genres'], ['isbn', 'ISBN'], ['asin', 'ASIN']]
const ASIN = /^(B0[0-9A-Z]{8}|\d{9}[\dX])$/
const REPLACE_KEY = 'pab-review-replace'
const blank = (v: unknown) => v === undefined || v === null || (typeof v === 'string' && !v.trim()) || (Array.isArray(v) && v.length === 0)
const pct = (v: number | undefined) => v === undefined || v < 0 ? '—' : `${Math.round(v * 100)}%`

function delta(cand?: number, book?: number) {
  if (!cand || !book) return ''
  if (Math.abs(cand - book) <= 120) return 'same length'
  return `${cand > book ? '+' : '−'}${hours(Math.abs(cand - book))}`
}

function edition(dramatized: boolean | undefined, narrators: unknown) {
  if (dramatized) return <span className="badge st-info" title="Full-cast dramatization (GraphicAudio and similar)">Dramatized</span>
  if (Array.isArray(narrators) && narrators.length) return <span className="badge" title="Single-narrator or ordinary reading">Reading</span>
  return <span className="badge" title="No narrator or publisher to tell the edition">Edition unknown</span>
}

/** Which fields Accept writes by default: empty ones, or every different one when replacing. */
function defaults(c: Cand, item: Item, replace: boolean, kind?: string) {
  const picks: Record<string, boolean> = {}
  for (const [k] of FIELDS) {
    const v = c.metadata[k]
    if (blank(v) || show(v) === show(item.metadata[k])) continue
    // Book catalogues describe the print edition; its publisher is not the audiobook's.
    if (k === 'publisher' && (kind === 'openlibrary' || kind === 'google')) continue
    picks[k] = blank(item.metadata[k]) || replace
  }
  return { picks, cover: !!c.cover && (!item.has_cover || replace) }
}

function payload(c: Cand, picks: Record<string, boolean>): Meta {
  const out: Meta = {}
  for (const [k] of FIELDS) {
    if (!picks[k]) continue
    let v = c.metadata[k]
    if (k === 'authors' || k === 'series') v = (v as Named[]).map(x => k === 'series' ? { name: x.name, sequence: x.sequence || '' } : { name: x.name })
    out[k] = v
  }
  return out
}

export default function MatchReview() {
  const { job: jobId = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const status = sp.get('status') || 'low_confidence'
  const [queue, setQueue] = useState<Queue>()
  const [qError, setQError] = useState('')
  const [providers, setProviders] = useState<Provider[]>([])
  const [idx, setIdx] = useState(-1)
  const [data, setData] = useState<Search>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [form, setForm] = useState<Query>({ title: '', author: '', provider: '' })
  const [chosen, setChosen] = useState(0)
  const [picks, setPicks] = useState<Record<string, boolean>>({})
  const [useCover, setUseCover] = useState(false)
  const [replace, setReplace] = useState(() => localStorage.getItem(REPLACE_KEY) === '1')
  const [busy, setBusy] = useState(false)
  const cache = useRef(new Map<string, Promise<Search>>())
  const titleInput = useRef<HTMLInputElement>(null)
  const current = useRef('')

  // Without a job in the URL, open the latest one that has uncertain results.
  useEffect(() => {
    if (jobId) return
    api.get<Job[]>(`${V1}/catalog/match-jobs`).then(list => {
      const j = list.find(j => j.status !== 'running' && j.status !== 'queued' && ((j.counts.low_confidence || 0) + (j.counts.not_found || 0)) > 0)
      if (j) navigate(`/review/${j.id}${j.counts.low_confidence ? '' : '?status=not_found'}`, { replace: true })
      else setQError('No finished quick match has uncertain results to review.')
    }).catch(e => setQError(errMsg(e)))
  }, [jobId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { api.get<Provider[]>(`${V1}/catalog/metadata/providers`).then(setProviders).catch(() => {}) }, [])

  // The queue for this job and filter; start at the first book still pending.
  useEffect(() => {
    if (!jobId) return
    setQueue(undefined); setQError('')
    api.get<Queue>(`${V1}/catalog/match-jobs/${encodeURIComponent(jobId)}/review?status=${encodeURIComponent(status)}`).then(q => {
      setQueue(q)
      const first = q.entries.findIndex(e => !e.review)
      setIdx(q.entries.length ? (first >= 0 ? first : 0) : -1)
    }).catch(e => setQError(errMsg(e)))
  }, [jobId, status])

  const entries = queue?.entries || []
  const entry = idx >= 0 ? entries[idx] : undefined
  const kindOf = useCallback((id?: string) => providers.find(p => p.id === id)?.kind, [providers])

  /** Candidates for a book, searched once per visit and query (the server spaces Audible requests). */
  const search = useCallback((e: Entry, q?: Query): Promise<Search> => {
    const provider = q ? q.provider : (e.provider || '')
    const key = [e.item_id, provider, q?.title ?? '', q?.author ?? ''].join('\u0000')
    let p = cache.current.get(key)
    if (!p) {
      const params = new URLSearchParams({ detail: '3' })
      if (provider) params.set('provider', provider)
      if (q) { params.set('title', q.title); params.set('author', q.author) }
      p = api.get<Search>(`${V1}/catalog/items/${encodeURIComponent(e.item_id)}/match?${params}`).catch(err => {
        cache.current.delete(key)
        // The provider that ran the job may have been disabled since: fall back to match order.
        if (!q && provider && err instanceof ApiError && err.status === 404) return search({ ...e, provider: '' })
        throw err
      })
      cache.current.set(key, p)
    }
    return p
  }, [])
  const forget = (itemId: string) => { for (const k of [...cache.current.keys()]) if (k.startsWith(itemId + '\u0000')) cache.current.delete(k) }

  const nextPending = useCallback((from: number, list = entries) => {
    for (let i = 1; i <= list.length; i++) { const j = (from + i) % list.length; if (!list[j].review) return j }
    return -1
  }, [entries])

  const show_ = useCallback((res: Search, keepForm = false) => {
    setData(res)
    if (!keepForm) setForm({ title: res.query.title, author: res.query.author, provider: res.provider || '' })
    setChosen(0)
  }, [])

  // Load the current book's candidates, then prefetch the next pending book's.
  useEffect(() => {
    if (!entry) { setData(undefined); return }
    const id = entry.item_id
    current.current = id
    setData(undefined); setError(''); setLoading(true)
    search(entry).then(res => { if (current.current === id) show_(res) })
      .catch(e => { if (current.current === id) { setError(errMsg(e)); setForm({ title: entry.title, author: '', provider: entry.provider || '' }) } })
      .finally(() => {
        if (current.current !== id) return
        setLoading(false)
        const n = nextPending(idx)
        if (n >= 0 && n !== idx) search(entries[n]).catch(() => {})
      })
  }, [entry?.item_id, entry?.position]) // eslint-disable-line react-hooks/exhaustive-deps

  const cands = (data?.candidates || []).slice(0, 3)
  const cand = cands[chosen]
  // Reset the field choice when the candidate or the replace toggle changes.
  useEffect(() => {
    if (!cand || !data) { setPicks({}); setUseCover(false); return }
    const d = defaults(cand, data.item, replace, kindOf(cand.provider))
    setPicks(d.picks); setUseCover(d.cover)
  }, [cand?.provider, cand?.id, data, replace, kindOf]) // eslint-disable-line react-hooks/exhaustive-deps

  const research = async (ev?: React.FormEvent) => {
    ev?.preventDefault()
    if (!entry || !form.title.trim()) return
    let q = { ...form, title: form.title.trim(), author: form.author.trim() }
    // A pasted ASIN is looked up on Audible directly.
    if (ASIN.test(q.title) && kindOf(q.provider) !== 'audible') {
      const audible = providers.find(p => p.kind === 'audible')
      if (audible) { q = { ...q, provider: audible.id }; setForm(q) }
    }
    const id = entry.item_id
    setLoading(true); setError('')
    try { const res = await search(entry, q); if (current.current === id) show_(res, true) } catch (e) { if (current.current === id) setError(errMsg(e)) } finally { if (current.current === id) setLoading(false) }
  }

  const mark = async (e: Entry, state: Review | 'pending') => {
    const r = await api.put<{ review: Review; reviewed_at: number }>(`${V1}/catalog/match-jobs/${encodeURIComponent(jobId)}/review/${e.position}`, { state })
    const list = entries.map(x => x.position === e.position ? { ...x, review: r.review, reviewed_at: r.reviewed_at } : x)
    const counts = { total: list.length, reviewed: 0, accepted: 0, skipped: 0, pending: 0 }
    for (const x of list) { if (x.review) { counts.reviewed++; counts[x.review]++ } else counts.pending++ }
    setQueue(q => q && { ...q, entries: list, counts })
    return list
  }
  const advance = (list: Entry[]) => {
    const n = nextPending(idx, list)
    if (n >= 0) setIdx(n)
  }

  const accept = async () => {
    if (!entry || !data || !cand || busy) return
    setBusy(true); setError('')
    try {
      const metadata = payload(cand, picks)
      if (Object.keys(metadata).length || useCover) {
        await api.post(`${V1}/catalog/items/${encodeURIComponent(entry.item_id)}/match`, { expected_updated_at: data.item.expected_updated_at, metadata, cover: useCover ? cand.cover : '' })
      }
      forget(entry.item_id)
      const list = await mark(entry, 'accepted')
      toast(`Accepted ${cand.title}`)
      advance(list)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // The book changed after its candidates were loaded: load it again before accepting.
        forget(entry.item_id)
        setError('This book changed since it was loaded. Its details were reloaded; check the fields and accept again.')
        try { const res = await search(entry); if (current.current === entry.item_id) show_(res) } catch { /* the error above stays */ }
      } else setError(errMsg(e))
    } finally { setBusy(false) }
  }
  const skip = async () => {
    if (!entry || busy) return
    setBusy(true); setError('')
    try { advance(await mark(entry, 'skipped')) } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const reopen = async () => {
    if (!entry || busy) return
    setBusy(true)
    try { await mark(entry, 'pending') } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const go = (d: number) => { if (entries.length) setIdx(i => Math.min(entries.length - 1, Math.max(0, i + d))) }

  // Keyboard: A accept, S skip, ←/→ previous/next, E edit search, 1–3 choose a candidate.
  // Captured before the app's own shortcuts (A and S open Authors and Series elsewhere).
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as HTMLElement
      if (t.closest('input, textarea, select, [contenteditable="true"]')) { if (e.key === 'Escape') (t as HTMLElement).blur(); return }
      if (modalOpen() || document.querySelector('.menu, .popover, .sel-pop, .palette-backdrop')) return
      const k = e.key.length === 1 ? e.key.toLowerCase() : e.key
      switch (k) {
        case 'a': void accept(); break
        case 's': void skip(); break
        case 'ArrowLeft': go(-1); break
        case 'ArrowRight': go(1); break
        case 'e': titleInput.current?.focus(); titleInput.current?.select(); break
        case '1': case '2': case '3': if (cands[Number(k) - 1]) setChosen(Number(k) - 1); break
        default: return
      }
      e.preventDefault(); e.stopPropagation()
    }
    window.addEventListener('keydown', h, true)
    return () => window.removeEventListener('keydown', h, true)
  })

  const counts = queue?.counts
  const progress = counts && counts.total ? Math.round(counts.reviewed / counts.total * 100) : 0
  const statusOptions = useMemo(() => [
    { value: 'low_confidence', label: `Low confidence ${queue?.statuses.low_confidence || 0}` },
    { value: 'not_found', label: `No result ${queue?.statuses.not_found || 0}` },
    { value: 'all', label: 'Both' },
  ], [queue])
  const setStatus = (v: string) => { const n = new URLSearchParams(sp); n.set('status', v); setSp(n, { replace: true }) }
  const editionClash = !!cand && !!data && cand.score?.edition === false
  const pickCount = Object.values(picks).filter(Boolean).length + (useCover ? 1 : 0)
  const providerName = (id?: string) => providers.find(p => p.id === id)?.name || id || 'Automatic'
  const item = data?.item
  const narrators = item?.metadata.narrators

  if (qError) return <><PageHeader title="Review matches" /><Empty title="Nothing to review" icon="review">{qError}</Empty></>
  if (!queue) return <><PageHeader title="Review matches" /><Loading /></>

  return <div className="review-page">
    <PageHeader title="Review matches" sub={<>Quick match from {new Date(queue.job.created_at).toLocaleString()} · {queue.job.total} books</>}
      actions={<Link className="btn" to={`/library${queue.job.library_id ? `?library=${encodeURIComponent(queue.job.library_id)}` : ''}`}>Back to library</Link>} />
    <div className="review-bar">
      <Segmented size="sm" label="Show" value={status} onChange={setStatus} options={statusOptions} />
      <span className="spacer" />
      <label className="check-row small"><input type="checkbox" checked={replace} onChange={e => { setReplace(e.target.checked); localStorage.setItem(REPLACE_KEY, e.target.checked ? '1' : '0') }} /> Replace existing details and cover</label>
    </div>
    <div className="review-progress">
      <div className="row small"><b className="grow">{counts!.reviewed} of {counts!.total} reviewed</b><span className="muted">{counts!.accepted} accepted · {counts!.skipped} skipped</span></div>
      <span className="match-progress" role="progressbar" aria-valuenow={progress} aria-valuemin={0} aria-valuemax={100} aria-label="Review progress"><span style={{ width: `${progress}%` }} /></span>
    </div>

    {!entries.length ? <Empty title="Nothing here" icon="review">This quick match has no books with this result.</Empty> : entry && <>
      <div className="review-nav row">
        <button type="button" className="btn" onClick={() => go(-1)} disabled={idx <= 0} aria-label="Previous book">← Previous</button>
        <span className="grow small muted review-pos">Book {idx + 1} of {entries.length}{counts!.pending === 0 && ' · all reviewed'}</span>
        <button type="button" className="btn" onClick={() => go(1)} disabled={idx >= entries.length - 1} aria-label="Next book">Next →</button>
      </div>
      {error && <p role="alert" className="callout danger">{error}</p>}
      <div className="review-grid">
        <section className="review-book" aria-label="Book">
          <img className="review-cover" src={`${V1}/cover/${encodeURIComponent(entry.item_id)}?w=300`} alt="" onError={e => { e.currentTarget.style.visibility = 'hidden' }} />
          <div className="review-book-body">
            <div className="row wrap">
              {entry.review ? <span className={`badge ${entry.review === 'accepted' ? 'st-ok' : ''}`}>{entry.review === 'accepted' ? 'Accepted' : 'Skipped'}</span> : <span className="badge st-warn">{entry.status === 'not_found' ? 'No result' : 'Low confidence'}</span>}
              {item && edition(item.dramatized, narrators)}
            </div>
            <h2><Link to={`/book/${encodeURIComponent(entry.item_id)}`}>{item?.title || entry.title}</Link></h2>
            <dl className="review-facts">
              <dt>Author</dt><dd>{item ? show(item.metadata.authors) || item.author || '—' : '…'}</dd>
              <dt>Narrator</dt><dd>{item ? show(narrators) || '—' : '…'}</dd>
              <dt>Series</dt><dd>{item ? show(item.metadata.series) || '—' : '…'}</dd>
              <dt>Length</dt><dd>{item ? item.duration ? hours(item.duration) : '—' : '…'}</dd>
              {!!item?.metadata.asin && <><dt>ASIN</dt><dd>{String(item.metadata.asin)}</dd></>}
              <dt>Folder</dt><dd className="review-path">{item?.path || '—'}</dd>
            </dl>
            <p className="small muted">Quick match: {entry.message || entry.status}{entry.match_title ? <> · best was <i>{entry.match_title}</i>{entry.match_author ? ` — ${entry.match_author}` : ''}{entry.score ? ` (${Math.round(entry.score * 100)}%)` : ''}</> : null}</p>
            {entry.review && <button type="button" className="btn sm" onClick={reopen} disabled={busy}>Mark as not reviewed</button>}
          </div>
        </section>

        <section className="review-cands" aria-label="Candidates">
          <form className="review-search" onSubmit={research}>
            <label>Title or ASIN<input ref={titleInput} value={form.title} onChange={e => setForm({ ...form, title: e.target.value })} /></label>
            <label>Author<input value={form.author} onChange={e => setForm({ ...form, author: e.target.value })} /></label>
            <label>Provider<select value={form.provider} onChange={e => setForm({ ...form, provider: e.target.value })}><option value="">Automatic</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
            <button type="submit" className="btn" disabled={loading || !form.title.trim()}>Search</button>
          </form>
          {loading && <Loading label="Searching… (Audible requests are spaced about a second apart)" />}
          {!loading && data?.errors.map(e => <p key={e} className="callout warn small">{e}</p>)}
          {!loading && data && !cands.length && <p className="muted">No results from {providerName(data.provider || form.provider)}. Edit the title (drop track numbers and part labels), try another provider or paste the ASIN.</p>}
          {!loading && cands.length > 0 && <div className="review-list" role="radiogroup" aria-label="Candidates">
            {cands.map((c, i) => <div key={c.provider + c.id} className={`review-cand ${i === chosen ? 'on' : ''}`}>
              <button type="button" role="radio" aria-checked={i === chosen} className="review-cand-head" onClick={() => setChosen(i)}>
                {c.cover ? <img className="match-cover" src={c.cover} alt="" loading="lazy" referrerPolicy="no-referrer" /> : <span className="match-cover" />}
                <span className="match-body">
                  <b>{c.title}{c.metadata.subtitle ? `: ${String(c.metadata.subtitle)}` : ''}</b>
                  <span className="small">{c.authors.join(', ')}{!blank(c.metadata.narrators) && <span className="muted"> · read by {show(c.metadata.narrators)}</span>}</span>
                  <span className="match-meta">
                    <Kbd>{i + 1}</Kbd>
                    {edition(c.dramatized, c.metadata.narrators)}
                    {!!c.duration && <span>{hours(c.duration)}{item?.duration ? ` (${delta(c.duration, item.duration)})` : ''}</span>}
                    {!blank(c.metadata.series) && <span>{show(c.metadata.series)}</span>}
                    {!!c.metadata.asin && <span>{String(c.metadata.asin)}</span>}
                    <span>{providerName(c.provider)}</span>
                  </span>
                  {c.score && <span className="review-score">
                    {c.score.exact ? <span className="badge st-ok">Exact ID</span> : <span className={`badge ${c.score.confident ? 'st-ok' : c.score.total >= 0.7 ? 'st-warn' : 'st-danger'}`}>{Math.round(c.score.total * 100)}%</span>}
                    <span>Title {pct(c.score.title)}</span><span>Author {pct(c.score.author)}</span><span>Length {pct(c.score.duration)}</span>
                    <span className={c.score.edition === false ? 'bad' : 'good'}>Edition {c.score.edition === false ? 'differs' : 'same'}</span>
                  </span>}
                </span>
              </button>
              {i === chosen && item && <div className="review-diff">
                {c.score?.edition === false && <p className="callout warn small" role="note">{c.dramatized ? 'This is a full-cast dramatization; the book looks like a reading.' : 'This is a single-narrator reading; the book looks like a full-cast dramatization.'} Quick match never pairs these. Accept only if you are sure it is the same production.</p>}
                <table className="match-fields"><thead><tr><th><span className="sr-only">Apply</span></th><th>Field</th><th>Current</th><th>Candidate</th></tr></thead>
                  <tbody>
                    {c.cover && <tr className={!useCover ? 'same' : ''}><td><input type="checkbox" aria-label="Apply cover" checked={useCover} onChange={e => setUseCover(e.target.checked)} /></td><td>Cover</td>
                      <td className="val">{item.has_cover ? <img className="match-cover" src={`${V1}/cover/${encodeURIComponent(item.id)}?w=120`} alt="Current cover" /> : '—'}</td>
                      <td className="val new"><img className="match-cover" src={c.cover} alt="Candidate cover" referrerPolicy="no-referrer" /></td></tr>}
                    {FIELDS.filter(([k]) => !blank(c.metadata[k])).map(([k, label]) => {
                      const same = show(c.metadata[k]) === show(item.metadata[k])
                      return <tr key={k} className={same ? 'same' : ''}>
                        <td><input type="checkbox" aria-label={`Apply ${label.toLowerCase()}`} disabled={same} checked={!!picks[k]} onChange={e => setPicks({ ...picks, [k]: e.target.checked })} /></td>
                        <td>{label}{!same && blank(item.metadata[k]) && <div className="small muted">empty now</div>}</td>
                        <td className="val"><span className="clamp">{show(item.metadata[k]) || '—'}</span></td>
                        <td className="val new"><span className="clamp">{show(c.metadata[k])}</span></td>
                      </tr>
                    })}
                  </tbody></table>
              </div>}
            </div>)}
          </div>}
        </section>
      </div>
      <div className="review-actions">
        <span className="small muted review-keys"><Kbd>A</Kbd> accept · <Kbd>S</Kbd> skip · <Kbd>←</Kbd><Kbd>→</Kbd> move · <Kbd>E</Kbd> edit search · <Kbd>1</Kbd>–<Kbd>3</Kbd> choose</span>
        <span className="spacer" />
        <button type="button" className="btn" onClick={skip} disabled={busy}>Skip</button>
        <button type="button" className={`btn ${editionClash ? 'danger' : 'primary'}`} onClick={accept} disabled={busy || loading || !cand}>
          {busy ? 'Saving…' : editionClash ? 'Accept different edition' : pickCount ? `Accept · ${pickCount} ${pickCount === 1 ? 'change' : 'changes'}` : 'Accept · nothing to change'}
        </button>
      </div>
    </>}
  </div>
}
