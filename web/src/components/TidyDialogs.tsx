// Library tidy-up: repair broken titles/series/subtitles and find missing covers.
// Both preview first; nothing changes until the ticked rows are applied.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Icon } from './icons'
import { Empty, Loading, Modal, errMsg, useToast } from './ui'
import '../styles/match.css'

interface TidyField { field: 'title' | 'subtitle' | 'series'; from: string; to: string }
interface TidyChange { id: string; expected_updated_at: number; author: string; path: string; changes: TidyField[]; reasons: string[]; confidence: 'high' | 'medium' | 'low' }
interface TidyReport { books: number; changes: TidyChange[]; unresolved: { id: string; title: string; path: string; reason: string }[]; reasons: Record<string, number> }

const REASONS: Record<string, string> = {
  file_name: 'File name as title', edition_suffix: '“GA” / “Unabridged” suffix', year_suffix: 'Year in title', track_number: 'Track number in title',
  volume_prefix: '“Vol N -” prefix', series_in_title: 'Series in title', author_in_title: 'Author in title', series_prefix: 'Series name prefix',
  wrapped: 'Wrapped in brackets', placeholder: 'Placeholder title', fragment: 'Title fragment', capitalisation: 'Capitalisation',
  series: 'Missing series', sequence: 'Missing series number', subtitle: 'Subtitle',
}
const FIELD: Record<string, string> = { title: 'Title', subtitle: 'Subtitle', series: 'Series' }
const CONF: Record<string, string> = { high: 'st-ok', medium: 'st-info', low: 'st-warn' }

export function TidyTitlesDialog({ library, onClose, onDone }: { library: string; onClose: () => void; onDone: () => void }) {
  const [report, setReport] = useState<TidyReport>()
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const load = () => {
    setReport(undefined); setError('')
    api.get<TidyReport>(`${V1}/catalog/libraries/${encodeURIComponent(library)}/tidy`)
      .then(r => { setReport(r); setPicked(new Set(r.changes.filter(c => c.confidence === 'high').map(c => c.id))) })
      .catch(e => setError(errMsg(e)))
  }
  useEffect(load, [library]) // eslint-disable-line react-hooks/exhaustive-deps
  const changes = report?.changes || []
  const reasons = useMemo(() => Object.entries(report?.reasons || {}).sort((a, b) => b[1] - a[1]), [report])
  const toggle = (ids: string[], on: boolean) => setPicked(p => { const n = new Set(p); ids.forEach(id => on ? n.add(id) : n.delete(id)); return n })
  const byReason = (r: string) => {
    const ids = changes.filter(c => c.reasons.includes(r)).map(c => c.id)
    toggle(ids, !ids.every(id => picked.has(id)))
  }
  const apply = async () => {
    setBusy(true); setError('')
    try {
      const items = changes.filter(c => picked.has(c.id)).map(c => ({ id: c.id, expected_updated_at: c.expected_updated_at }))
      const r = await api.post<{ updated: number }>(`${V1}/catalog/libraries/${encodeURIComponent(library)}/tidy`, { items })
      toast(`${r.updated} ${r.updated === 1 ? 'book' : 'books'} tidied`)
      onDone(); onClose()
    } catch (e) {
      setError(`${errMsg(e)} Nothing was changed; refresh the preview and try again.`)
    } finally { setBusy(false) }
  }
  const footer = report && changes.length > 0 && <>
    <span className="muted small grow">{picked.size} of {changes.length} selected · saved together</span>
    <button className="btn" onClick={onClose}>Cancel</button>
    <button className="btn primary" disabled={busy || !picked.size} onClick={apply}>{busy ? 'Saving…' : `Apply ${picked.size}`}</button>
  </>
  return (
    <Modal title="Tidy titles & series" size="xl" onClose={onClose} footer={footer || undefined}>
      <div className="stack">
        {error && <p role="alert" className="callout danger"><Icon name="alert" size={15} />{error}</p>}
        {!report && !error && <Loading label="Checking every book…" />}
        {report && <>
          <p className="muted small">Proposals come from each book’s folder names, file names and tags. Fields you edited yourself are never touched. High-confidence changes are ticked; check the rest before applying.</p>
          {changes.length === 0 ? <Empty title="Nothing to tidy" icon="check">All {report.books.toLocaleString()} books look fine.</Empty> : <>
            <div className="row wrap tidy-picks">
              <button type="button" className="chip" onClick={() => toggle(changes.map(c => c.id), picked.size < changes.length)}>{picked.size < changes.length ? 'Select all' : 'Select none'}</button>
              {(['high', 'medium', 'low'] as const).map(c => { const ids = changes.filter(x => x.confidence === c).map(x => x.id); return ids.length ? <button key={c} type="button" className={`chip ${ids.every(id => picked.has(id)) ? 'active' : ''}`} onClick={() => toggle(ids, !ids.every(id => picked.has(id)))}>{c} confidence <span className="n">{ids.length}</span></button> : null })}
              <span className="muted small">By reason:</span>
              {reasons.map(([r, n]) => { const ids = changes.filter(c => c.reasons.includes(r)).map(c => c.id); return <button key={r} type="button" className={`chip sm ${ids.every(id => picked.has(id)) ? 'active' : ''}`} onClick={() => byReason(r)}>{REASONS[r] || r} <span className="n">{n}</span></button> })}
            </div>
            <div className="tidy-table-wrap">
              <table className="table compact tidy-table">
                <thead><tr><th style={{ width: 28 }}><span className="sr-only">Select</span></th><th>Book</th><th>Change</th><th>Why</th></tr></thead>
                <tbody>{changes.map(c => (
                  <tr key={c.id} className={picked.has(c.id) ? 'on' : ''}>
                    <td><input type="checkbox" aria-label={`Apply to ${c.path}`} checked={picked.has(c.id)} onChange={e => toggle([c.id], e.target.checked)} /></td>
                    <td><span className="tidy-path" title={c.path}>{c.path}</span><span className="muted xsmall">{c.author}</span></td>
                    <td>{c.changes.map(f => <div key={f.field} className="tidy-change"><span className="muted xsmall">{FIELD[f.field]}</span> <s>{f.from || '—'}</s> <Icon name="arrow-right" size={12} /> <b>{f.to || '(empty)'}</b></div>)}</td>
                    <td><span className={`badge ${CONF[c.confidence]}`}>{c.confidence}</span> <span className="muted xsmall">{c.reasons.map(r => REASONS[r] || r).join(', ')}</span></td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
          </>}
          {report.unresolved.length > 0 && <details>
            <summary className="small">{report.unresolved.length} {report.unresolved.length === 1 ? 'book still looks' : 'books still look'} wrong, but nothing says what {report.unresolved.length === 1 ? 'it' : 'they'} should be called</summary>
            <ul className="small">{report.unresolved.map(u => <li key={u.id}><b>{u.title}</b> <span className="muted">{u.path}</span></li>)}</ul>
          </details>}
        </>}
      </div>
    </Modal>
  )
}

interface CoverBook { id: string; title: string; author: string; path: string; expected_updated_at: number; source: '' | 'embedded' | 'folder' | 'audible'; detail: string; asin?: string; cover_url?: string; match?: string; reason?: string }
const SOURCE: Record<string, string> = { embedded: 'Embedded in the audio', folder: 'Image in the folder', audible: 'Audible' }

export function FindCoversDialog({ library, onClose, onDone }: { library: string; onClose: () => void; onDone: () => void }) {
  const [books, setBooks] = useState<CoverBook[]>()
  const [search, setSearch] = useState(false)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<{ id: string; error: string }[]>([])
  const { toast } = useToast()
  useEffect(() => {
    setBooks(undefined); setError('')
    api.get<{ books: CoverBook[] }>(`${V1}/catalog/libraries/${encodeURIComponent(library)}/covers${search ? '?search=1' : ''}`)
      .then(r => { setBooks(r.books); setPicked(new Set(r.books.filter(b => b.source).map(b => b.id))) })
      .catch(e => setError(errMsg(e)))
  }, [library, search])
  const fixable = (books || []).filter(b => b.source)
  const apply = async () => {
    setBusy(true); setError(''); setFailed([])
    try {
      const items = fixable.filter(b => picked.has(b.id)).map(b => ({ id: b.id, expected_updated_at: b.expected_updated_at, source: b.source, detail: b.detail }))
      const r = await api.post<{ saved: number; failed: { id: string; error: string }[] }>(`${V1}/catalog/libraries/${encodeURIComponent(library)}/covers`, { items })
      if (r.saved) { toast(`${r.saved} ${r.saved === 1 ? 'cover' : 'covers'} saved`); onDone() }
      if (r.failed.length) setFailed(r.failed); else onClose()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const footer = fixable.length > 0 && <>
    <span className="muted small grow">{picked.size} of {fixable.length} selected</span>
    <button className="btn" onClick={onClose}>Cancel</button>
    <button className="btn primary" disabled={busy || !picked.size} onClick={apply}>{busy ? 'Saving…' : `Save ${picked.size} ${picked.size === 1 ? 'cover' : 'covers'}`}</button>
  </>
  return (
    <Modal title="Find missing covers" size="lg" onClose={onClose} footer={footer || undefined}>
      <div className="stack">
        {error && <p role="alert" className="callout danger"><Icon name="alert" size={15} />{error}</p>}
        <label className="small"><input type="checkbox" checked={search} onChange={e => setSearch(e.target.checked)} /> Also search Audible by title and author for books without a local cover or ASIN (slower; only confident matches)</label>
        {!books && !error && <Loading label={search ? 'Looking on Audible…' : 'Looking for covers…'} />}
        {failed.length > 0 && <div className="callout danger small">{failed.map(f => <div key={f.id}>{books?.find(b => b.id === f.id)?.title || f.id}: {f.error}</div>)}</div>}
        {books && (books.length === 0 ? <Empty title="Every book has a cover" icon="check" /> : (
          <table className="table compact tidy-table">
            <thead><tr><th style={{ width: 28 }}><span className="sr-only">Select</span></th><th>Book</th><th>Cover from</th></tr></thead>
            <tbody>{books.map(b => (
              <tr key={b.id}>
                <td>{b.source && <input type="checkbox" aria-label={`Save a cover for ${b.title}`} checked={picked.has(b.id)} onChange={e => setPicked(p => { const n = new Set(p); if (e.target.checked) n.add(b.id); else n.delete(b.id); return n })} />}</td>
                <td><b>{b.title}</b><span className="tidy-path muted xsmall" title={b.path}>{b.path}</span></td>
                <td>{b.source ? <div className="row">
                  {b.cover_url && <img src={b.cover_url} alt="" width={44} height={44} className="tidy-thumb" loading="lazy" />}
                  <span>{SOURCE[b.source]}<span className="muted xsmall"> · {b.match ? `${b.match} (${b.detail})` : b.detail}</span></span>
                </div> : <span className="muted small">{b.reason}</span>}</td>
              </tr>
            ))}</tbody>
          </table>
        ))}
      </div>
    </Modal>
  )
}
