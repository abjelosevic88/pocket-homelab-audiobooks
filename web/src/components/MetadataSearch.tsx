import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { errMsg } from './ui'
type Candidate = { id: string; provider: string; title: string; authors: string[]; cover: string; metadata: Record<string, unknown> }
export function MetadataSearch({ title, author, onSelect }: { title: string; author: string; onSelect: (metadata: Record<string, unknown>, cover: string) => void }) {
  const [providers, setProviders] = useState<{ id: string; name: string; kind: string }[]>([])
  const [provider, setProvider] = useState('openlibrary')
  const [query, setQuery] = useState(title)
  const [results, setResults] = useState<Candidate[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [searched, setSearched] = useState(false)
  useEffect(() => { api.get<typeof providers>(`${V1}/catalog/metadata/providers`).then(p => { setProviders(p); if (p.length) setProvider(p[0].id) }).catch(e => setError(errMsg(e))) }, [])
  const search = async () => {
    setBusy(true); setError(''); setResults([]); setSearched(true)
    try { setResults(await api.get<Candidate[]>(`${V1}/catalog/metadata/search?${new URLSearchParams({ provider, q: query, author })}`)) }
    catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  const choose = async (c: Candidate) => {
    setBusy(true); setError('')
    try {
      let extra: Record<string, unknown> = {}
      if (providers.find(p => p.id === c.provider)?.kind === 'openlibrary') extra = await api.get(`${V1}/catalog/metadata/search?${new URLSearchParams({ provider: c.provider, work: c.id })}`)
      onSelect({ ...c.metadata, ...extra }, c.cover); setResults([]); setSearched(false)
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <details><summary>Find metadata</summary><div className="stack mt">
    <p className="small muted">Choose a result, then review the fields before saving. Empty fields leave existing values intact.</p>
    <label>Metadata provider<select value={provider} onChange={e => setProvider(e.target.value)}>{providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
    <label>Title or ISBN<input value={query} onChange={e => setQuery(e.target.value)} /></label>
    <button type="button" className="btn" disabled={busy || !query.trim() || !providers.length} onClick={search}>{busy ? 'Searching…' : 'Search metadata'}</button>
    {error && <p className="callout danger" role="alert">{error}</p>}
    {!busy && searched && !results.length && !error && <p className="small muted">No results to display.</p>}
    {results.map((r, i) => <div className="row" key={r.id + i}><div className="grow"><b>{r.title}</b><p className="small muted">{r.authors.join(', ')} {String(r.metadata.publishedYear || '')}</p></div><button type="button" className="btn" disabled={busy} onClick={() => choose(r)}>Use this metadata</button></div>)}
  </div></details>
}
