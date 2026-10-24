import { useEffect, useState } from 'react'
import { MetadataSettings } from './MetadataSettings'
import { NativeAccounts } from './NativeAccounts'
import { useApp } from '../lib/app-context'
import { api, V1 } from '../lib/api'
import { Loading, errMsg, useToast } from './ui'
import { fmtDateTime } from '../lib/format'

type Job = { id: string; status: string; started_at: number; report: { added: number; updated: number; missing: number; invalid: number; error?: string } }
type Library = { id: string; name: string; media_type: string; count: number; can_scan: boolean; watch: boolean; interval_seconds: number; jobs: Job[] }
export function NativeLibrarySettings() {
  const [libraries, setLibraries] = useState<Library[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const { user } = useApp()
  const [newLibrary, setNewLibrary] = useState({ name: '', path: '', media_type: 'book' })
  const { toast } = useToast()
  const load = async () => {
    try { const data = await api.get<{ libraries: Library[] }>(`${V1}/catalog/manage`); setLibraries(data.libraries); setError('') }
    catch (e) { setError(errMsg(e)) }
  }
  useEffect(() => { void load(); const timer = window.setInterval(load, 5000); return () => clearInterval(timer) }, [])
  const scan = async (id: string, force = false, import_metadata = false) => {
    setBusy(id)
    try { await api.post(`${V1}/catalog/libraries/${encodeURIComponent(id)}/scan`, { force, import_metadata }); toast('Scanning started'); await load() }
    catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy('') }
  }
  const cancel = async (id: string) => {
    try { await api.del(`${V1}/catalog/scans/${encodeURIComponent(id)}`); await load() }
    catch (e) { toast(errMsg(e), 'err') }
  }
  const watch = async (lib: Library, enabled: boolean, interval = lib.interval_seconds || 600) => {
    try { await api.put(`${V1}/catalog/libraries/${encodeURIComponent(lib.id)}/watch`, { enabled, interval_seconds: interval }); await load() }
    catch (e) { toast(errMsg(e), 'err') }
  }
  const create = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy('new')
    try { await api.post(`${V1}/catalog/libraries`, newLibrary); setNewLibrary({ name: '', path: '', media_type: 'book' }); await load(); toast('Library added') }
    catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy('') }
  }
  if (error) return <p className="callout danger">{error}</p>
  if (!libraries) return <Loading />
  return <div className="stack loose">
    <p>Your library, playback and progress are stored in Pocket Audiobooks.</p>
    {libraries.map(lib => {
      const latest = lib.jobs[0]
      const running = lib.jobs.find(job => job.status === 'running')
      return <section className="set-section" key={lib.id}>
        <div className="set-section-head"><div className="grow"><h3>{lib.name}</h3><p>{lib.count.toLocaleString()} {lib.media_type === 'podcast' ? 'podcasts' : 'books'}</p></div></div>
        {lib.can_scan && <div className="set-section-body stack">
          <label><input type="checkbox" checked={lib.watch} onChange={e => watch(lib, e.target.checked)} /> Automatically check for file changes</label>
          {lib.watch && <label>Check every <select value={lib.interval_seconds || 600} onChange={e => watch(lib, true, Number(e.target.value))}><option value={60}>minute</option><option value={600}>10 minutes</option><option value={3600}>hour</option><option value={86400}>day</option></select></label>}
          <p className="muted small">Scan for new and changed files. Missing files stay in the catalog. A full rescan reads audio metadata again. Import sidecars replaces matching catalog fields with metadata.json, desc.txt and reader.txt.</p>
          <div className="row wrap">
            <button type="button" className="btn primary" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id)}>Scan library</button>
            <button type="button" className="btn" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id, true)}>Full rescan</button>
            <button type="button" className="btn" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id, false, true)}>Import sidecars</button>
            {running && <button type="button" className="btn" onClick={() => cancel(running.id)}>Cancel scan</button>}
          </div>
          {latest && <div role="status" className="small"><p>{latest.status} · {fmtDateTime(new Date(latest.started_at).toISOString())}</p>
            {latest.status === 'complete' && <p>{latest.report.added} added · {latest.report.updated} updated · {latest.report.missing} missing · {latest.report.invalid || 0} invalid audio files</p>}
            {latest.report.error && <p className="danger-text">{latest.report.error}</p>}
          </div>}
        </div>}
      </section>
    })}
    {user?.role === 'admin' && <><NativeAccounts /><MetadataSettings /></>}
    {user?.role === 'admin' && <section className="set-section"><div className="set-section-head"><h3>Add a library</h3></div><form className="set-section-body stack" onSubmit={create}>
      <label>Name<input required value={newLibrary.name} onChange={e => setNewLibrary({ ...newLibrary, name: e.target.value })} /></label>
      <label>Folder on the server<input required placeholder="/media/audiobooks" value={newLibrary.path} onChange={e => setNewLibrary({ ...newLibrary, path: e.target.value })} /></label>
      <p className="muted small">Use an existing folder mounted into this app's container.</p>
      <button type="submit" className="btn primary" disabled={busy === 'new'}>Add library</button>
    </form></section>}
  </div>
}
