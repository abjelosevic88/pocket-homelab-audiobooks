// Match one book against metadata providers, then choose the fields to apply.
import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { hours } from '../lib/books'
import { Loading, Modal, errMsg, useToast } from './ui'
import '../styles/match.css'

type Named = { name: string; sequence?: string }
type Meta = Record<string, unknown>
export type MatchScore = { total: number; title: number; author: number; duration: number; exact: boolean; confident: boolean }
export type MatchCandidate = { id: string; provider: string; title: string; authors: string[]; cover: string; duration?: number; region?: string; metadata: Meta; score?: MatchScore }
type Search = { item: { id: string; title: string; author: string; duration: number; expected_updated_at: number; has_cover: boolean; metadata: Meta }; query: { title: string; author: string }; provider?: string; candidates: MatchCandidate[]; errors: string[] }
type Provider = { id: string; name: string; kind: string }

const FIELDS: [string, string][] = [['title', 'Title'], ['subtitle', 'Subtitle'], ['authors', 'Authors'], ['narrators', 'Narrators'], ['series', 'Series'], ['description', 'Description'], ['publisher', 'Publisher'], ['publishedYear', 'Publication year'], ['language', 'Language'], ['genres', 'Genres'], ['tags', 'Tags'], ['isbn', 'ISBN'], ['asin', 'ASIN'], ['abridged', 'Abridged']]

export function show(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'boolean') return v ? 'Yes' : 'No'
  if (Array.isArray(v)) return v.map(x => typeof x === 'object' && x ? (x as Named).name + ((x as Named).sequence ? ` #${(x as Named).sequence}` : '') : String(x)).filter(Boolean).join(', ')
  return String(v)
}
const blank = (v: unknown) => v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
export function scoreBadge(s?: MatchScore) {
  if (!s) return null
  if (s.exact) return <span className="badge st-ok">Exact ID</span>
  return <span className={`badge ${s.confident ? 'st-ok' : s.total >= 0.7 ? 'st-warn' : 'st-danger'}`}>{s.confident ? 'Strong' : s.total >= 0.7 ? 'Possible' : 'Weak'} · {Math.round(s.total * 100)}%</span>
}

export function MatchDialog({ id, onClose, onSaved }: { id: string; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [providers, setProviders] = useState<Provider[]>([])
  const [provider, setProvider] = useState('')
  const [title, setTitle] = useState('')
  const [author, setAuthor] = useState('')
  const [data, setData] = useState<Search>()
  const [chosen, setChosen] = useState<MatchCandidate>()
  const [pick, setPick] = useState<Record<string, boolean>>({})
  const [useCover, setUseCover] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const base = `${V1}/catalog/items/${encodeURIComponent(id)}`

  const search = async (p = provider, t = title, a = author, first = false) => {
    setBusy(true); setError(''); setChosen(undefined)
    try {
      const q = new URLSearchParams()
      if (p) q.set('provider', p)
      if (!first) { q.set('title', t); q.set('author', a) }
      const res = await api.get<Search>(`${base}/match?${q}`)
      setData(res)
      if (first) { setTitle(res.query.title); setAuthor(res.query.author); setProvider(res.provider || '') }
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  useEffect(() => {
    api.get<Provider[]>(`${V1}/catalog/metadata/providers`).then(setProviders).catch(() => {})
    void search('', '', '', true)
  }, [id]) // eslint-disable-line react-hooks/exhaustive-deps

  const choose = async (c: MatchCandidate) => {
    if (!data) return
    setBusy(true); setError('')
    let full = c
    try {
      const kind = providers.find(p => p.id === c.provider)?.kind
      if (kind === 'audible') {
        const d = await api.get<MatchCandidate>(`${V1}/catalog/metadata/search?${new URLSearchParams({ provider: c.provider, asin: c.id })}`)
        full = { ...c, ...d, metadata: { ...c.metadata, ...Object.fromEntries(Object.entries(d.metadata).filter(([, v]) => !blank(v))) }, cover: d.cover || c.cover, score: c.score }
      } else if (kind === 'openlibrary') {
        const extra = await api.get<Meta>(`${V1}/catalog/metadata/search?${new URLSearchParams({ provider: c.provider, work: c.id })}`)
        full = { ...c, metadata: { ...c.metadata, ...extra } }
      }
    } catch { /* details are optional; the search result still applies */ }
    const cur = data.item.metadata
    const next: Record<string, boolean> = {}
    for (const [k] of FIELDS) { const v = full.metadata[k]; next[k] = !blank(v) && k !== 'tags' && show(v) !== show(cur[k]) && !(k === 'abridged' && v === false && !cur[k]) }
    setPick(next); setUseCover(!!full.cover && !data.item.has_cover); setChosen(full); setBusy(false)
  }

  const apply = async () => {
    if (!data || !chosen) return
    const metadata: Meta = {}
    for (const [k] of FIELDS) if (pick[k]) {
      let v = chosen.metadata[k]
      if (k === 'authors' || k === 'series') v = (v as Named[]).map(x => k === 'series' ? { name: x.name, sequence: x.sequence || '' } : { name: x.name })
      if (k === 'abridged') v = !!v
      metadata[k] = v
    }
    setBusy(true); setError('')
    try {
      await api.post(`${base}/match`, { expected_updated_at: data.item.expected_updated_at, metadata, cover: useCover ? chosen.cover : '' })
      toast('Match applied'); onSaved(); onClose()
    } catch (e) { setError(errMsg(e)); setBusy(false) }
  }

  const count = Object.values(pick).filter(Boolean).length + (useCover ? 1 : 0)
  return <Modal title="Match book" size="xl" onClose={onClose} footer={chosen ? <div className="row"><button type="button" className="btn" onClick={() => setChosen(undefined)} disabled={busy}>Back to results</button><span className="spacer" /><button type="button" className="btn primary" disabled={busy || !count} onClick={apply}>{busy ? 'Applying…' : `Apply ${count} ${count === 1 ? 'change' : 'changes'}`}</button></div> : undefined}>
    <div className="stack">
      {error && <p role="alert" className="callout danger">{error}</p>}
      {!chosen && <>
        <form className="match-search" onSubmit={e => { e.preventDefault(); void search() }}>
          <label>Provider<select value={provider} onChange={e => { setProvider(e.target.value); void search(e.target.value) }}><option value="">Automatic</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
          <label>Title or ASIN<input value={title} onChange={e => setTitle(e.target.value)} /></label>
          <label>Author<input value={author} onChange={e => setAuthor(e.target.value)} /></label>
          <button type="submit" className="btn" disabled={busy || !title.trim()}>Search</button>
        </form>
        {busy && <Loading label="Searching…" />}
        {!busy && data?.errors.map(e => <p key={e} className="callout warn small">{e}</p>)}
        {!busy && data && !data.candidates.length && <p className="muted">No results. Try a shorter title, another provider or the book's ASIN.</p>}
        {!busy && data && data.candidates.length > 0 && <div className="match-list" role="list">{data.candidates.map(c => <button type="button" role="listitem" key={c.provider + c.id} className="match-cand" onClick={() => void choose(c)}>
          {c.cover ? <img className="match-cover" src={c.cover} alt="" loading="lazy" referrerPolicy="no-referrer" /> : <span className="match-cover" />}
          <span className="match-body">
            <b>{c.title}{c.metadata.subtitle ? `: ${String(c.metadata.subtitle)}` : ''}</b>
            <span className="small">{c.authors.join(', ')}{c.metadata.narrators ? <span className="muted"> · read by {show(c.metadata.narrators)}</span> : null}</span>
            <span className="match-meta">{scoreBadge(c.score)}{!!c.metadata.series && <span>{show(c.metadata.series)}</span>}{!!c.duration && <span>{hours(c.duration)}{data.item.duration ? ` (book ${hours(data.item.duration)})` : ''}</span>}{!!c.metadata.publishedYear && <span>{String(c.metadata.publishedYear)}</span>}<span>{providers.find(p => p.id === c.provider)?.name || c.provider}</span></span>
          </span>
        </button>)}</div>}
      </>}
      {chosen && data && <>
        <p className="muted small">Choose what to copy from <b>{chosen.title}</b>. Unchecked fields keep their current value.</p>
        {chosen.cover && <label className="match-covers check-row"><input type="checkbox" checked={useCover} onChange={e => setUseCover(e.target.checked)} /> Use this cover
          <img className="match-cover lg" src={`${V1}/cover/${encodeURIComponent(id)}?w=200`} alt="Current cover" onError={e => { e.currentTarget.style.visibility = 'hidden' }} />→<img className="match-cover lg" src={chosen.cover} alt="New cover" referrerPolicy="no-referrer" /></label>}
        <table className="match-fields"><thead><tr><th><input type="checkbox" aria-label="Select all fields" checked={FIELDS.every(([k]) => blank(chosen.metadata[k]) || pick[k])} onChange={e => setPick(Object.fromEntries(FIELDS.map(([k]) => [k, e.target.checked && !blank(chosen.metadata[k])])))} /></th><th>Field</th><th>Current</th><th>From {providers.find(p => p.id === chosen.provider)?.name || chosen.provider}</th></tr></thead>
          <tbody>{FIELDS.filter(([k]) => !blank(chosen.metadata[k])).map(([k, label]) => { const same = show(chosen.metadata[k]) === show(data.item.metadata[k]); return <tr key={k} className={same ? 'same' : ''}>
            <td><input type="checkbox" aria-label={`Apply ${label.toLowerCase()}`} checked={!!pick[k]} onChange={e => setPick({ ...pick, [k]: e.target.checked })} /></td>
            <td>{label}{k === 'tags' && <div className="small muted">Tags can limit who sees a book</div>}</td>
            <td className="val"><span className="clamp">{show(data.item.metadata[k]) || '—'}</span></td>
            <td className="val new"><span className="clamp">{show(chosen.metadata[k])}</span></td>
          </tr> })}</tbody></table>
      </>}
    </div>
  </Modal>
}
