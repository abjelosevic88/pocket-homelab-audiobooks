import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Field, Modal, errMsg, useToast } from './ui'
type Provider = { id: string; kind: string; name: string; url: string; enabled: boolean; has_secret: boolean; revision: number; region?: string; priority?: number; secret?: string; clear_secret?: boolean }
const REGIONS: Record<string, string> = { us: 'United States', ca: 'Canada', uk: 'United Kingdom', au: 'Australia', fr: 'France', de: 'Germany', jp: 'Japan', it: 'Italy', in: 'India', es: 'Spain' }
export function MetadataSettings() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [edit, setEdit] = useState<Provider | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const load = () => api.get<Provider[]>(`${V1}/catalog/metadata/providers?manage=1`).then(setProviders).catch(e => setError(errMsg(e)))
  useEffect(() => { void load() }, [])
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setError('')
    try { await api.put(`${V1}/catalog/metadata/providers`, edit); setEdit(null); await load(); toast('Provider saved') }
    catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <section className="set-section"><div className="set-section-head"><div className="grow"><h3>Metadata providers</h3><p>Match and quick match try enabled providers in match order, lowest first.</p></div><button type="button" className="btn" onClick={() => { setError(''); setEdit({ id: '', kind: 'custom', name: '', url: '', enabled: true, has_secret: false, revision: 0 }) }}>Add provider</button></div>
    <div className="set-section-body stack">{error && !edit && <p className="callout danger">{error}</p>}
      {providers.map(p => <div className="row" key={p.id}><div className="grow"><b>{p.name}</b><p className="small muted">{p.enabled ? 'Enabled' : 'Disabled'} · order {p.priority ?? 100}{p.kind === 'audible' ? ` · region ${p.region || 'default'}` : ''}{p.has_secret ? ' · key saved' : ''}</p></div><button type="button" className="btn" onClick={() => { setError(''); setEdit({ ...p, secret: '' }) }}>Configure provider</button></div>)}
    </div>
    {edit && <Modal title="Metadata provider" onClose={() => setEdit(null)}><form className="stack" onSubmit={save}>
      {error && <p className="callout danger" role="alert">{error}</p>}
      <Field label="Provider name"><input required value={edit.name} onChange={e => setEdit({ ...edit, name: e.target.value })} /></Field>
      {edit.kind === 'custom' && <Field label="Provider address" help="Base URL of an Audiobookshelf-compatible metadata provider."><input required type="url" value={edit.url} onChange={e => setEdit({ ...edit, url: e.target.value })} /></Field>}
      {edit.kind === 'audible' && <Field label="Audible region" help="Store to search. Default uses AUDIBLE_REGION, else United States."><select value={edit.region || ''} onChange={e => setEdit({ ...edit, region: e.target.value })}><option value="">Default</option>{Object.entries(REGIONS).map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select></Field>}
      <Field label="Match order" help="Lower numbers are tried first by Match and Quick match (1–1000)."><input type="number" min={1} max={1000} value={edit.priority ?? 100} onChange={e => setEdit({ ...edit, priority: Number(e.target.value) })} /></Field>
      {edit.kind !== 'openlibrary' && edit.kind !== 'audible' && <><Field label={edit.kind === 'google' ? 'Google Books API key' : 'Authorization header'} help={edit.has_secret ? 'Leave blank to keep the saved key.' : 'Stored encrypted and never returned to the browser.'}><input type="password" autoComplete="new-password" value={edit.secret || ''} onChange={e => setEdit({ ...edit, secret: e.target.value })} /></Field>{edit.has_secret && <label><input type="checkbox" checked={!!edit.clear_secret} onChange={e => setEdit({ ...edit, clear_secret: e.target.checked })} /> Remove saved key</label>}</>}
      <label><input type="checkbox" checked={edit.enabled} onChange={e => setEdit({ ...edit, enabled: e.target.checked })} /> Enabled</label>
      <button type="submit" className="btn primary" disabled={busy}>Save provider</button>
    </form></Modal>}
  </section>
}
