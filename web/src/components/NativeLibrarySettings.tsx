import { useEffect, useState } from 'react'
import {LibrarySettingsEditor} from './LibrarySettingsEditor'
import { MetadataSettings } from './MetadataSettings'
import { NativeAccounts } from './NativeAccounts'
import { useApp } from '../lib/app-context'
import { api, V1 } from '../lib/api'
import { Loading, errMsg, useToast } from './ui'
import { ScanScheduleEditor, ScanScheduleStatus, describeSchedule, type ScanSchedule } from './ScanScheduleEditor'

type Job = { id: string; status: string; started_at: number; finished_at?: number | null; origin?: string; report: { added: number; updated: number; missing: number; quarantined?: number; invalid: number; error?: string } }
type Library = { id: string; name: string; media_type: string; count: number; can_scan: boolean; watch: boolean; interval_seconds: number; window_start: string; window_end: string; next_scan_at?: number | null; jobs: Job[] }
const DEFAULT_ALL = 3600
export function NativeLibrarySettings() {
  const [libraries, setLibraries] = useState<Library[] | null>(null)
  const [timezone, setTimezone] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const { user } = useApp()
  const [newLibrary, setNewLibrary] = useState({ name: '', path: '', media_type: 'book' })
  const { toast } = useToast()
  const load = async () => {
    try { const data = await api.get<{ libraries: Library[]; timezone?: { name: string } }>(`${V1}/catalog/manage`); setLibraries(data.libraries); setTimezone(data.timezone?.name || ''); setError('') }
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
  const schedule = (lib: Library): ScanSchedule => ({ enabled: lib.watch, interval_seconds: lib.interval_seconds || 3600, window_start: lib.window_start || '', window_end: lib.window_end || '' })
  const watch = async (lib: Library, s: ScanSchedule) => {
    try { await api.put(`${V1}/catalog/libraries/${encodeURIComponent(lib.id)}/watch`, s); toast('Schedule saved'); await load() }
    catch (e) { toast(errMsg(e), 'err') }
  }
  const watchAll = async (s: ScanSchedule) => {
    try { const r = await api.put<{ libraries: string[] }>(`${V1}/catalog/watch`, s); toast(`Schedule applied to ${r.libraries.length} ${r.libraries.length === 1 ? 'library' : 'libraries'}`); await load() }
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
    {libraries.filter(l => l.can_scan).length > 1 && (() => {
      const scannable = libraries.filter(l => l.can_scan)
      const same = scannable.every(l => describeSchedule(schedule(l)) === describeSchedule(schedule(scannable[0])))
      return <section className="set-section">
        <div className="set-section-head"><div className="grow"><h3>All libraries</h3><p>{same ? `Automatic scans: ${describeSchedule(schedule(scannable[0]))}` : 'Libraries use different automatic scan schedules'}</p></div></div>
        <div className="set-section-body"><ScanScheduleEditor value={same ? schedule(scannable[0]) : { enabled: true, interval_seconds: DEFAULT_ALL, window_start: '', window_end: '' }} timezone={timezone} saveLabel="Apply to all libraries" onSave={watchAll} /></div>
      </section>
    })()}
    {libraries.map(lib => {
      const latest = lib.jobs[0]
      const running = lib.jobs.find(job => job.status === 'running')
      return <section className="set-section" key={lib.id}>
        <div className="set-section-head"><div className="grow"><h3>{lib.name}</h3><p>{lib.count.toLocaleString()} books</p></div></div>
        {lib.can_scan && <div className="set-section-body stack">
          <ScanScheduleEditor value={schedule(lib)} timezone={timezone} onSave={s => watch(lib, s)} />
          <ScanScheduleStatus jobs={lib.jobs} nextAt={lib.watch ? lib.next_scan_at : null} />
          <p className="muted small">Scan for new and changed files. Missing files stay in the catalog. A full rescan reads audio metadata again. Import sidecars replaces matching catalog fields with metadata.json, legacy metadata.abs, desc.txt and reader.txt.</p>
          <div className="row wrap">
            <button type="button" className="btn primary" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id)}>Scan library</button>
            <button type="button" className="btn" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id, true)}>Full rescan</button>
            <button type="button" className="btn" disabled={!!running || busy === lib.id} onClick={() => scan(lib.id, false, true)}>Import sidecars</button>
            {running && <button type="button" className="btn" onClick={() => cancel(running.id)}>Cancel scan</button>}
          </div>
          {latest && latest.status === 'complete' && latest.report.invalid > 0 && <p className="small muted">{latest.report.invalid} invalid audio files in the latest scan</p>}
        </div>}
      </section>
    })}
    {user?.role === 'admin' && <><LibrarySettingsEditor refresh={libraries.map(l=>l.id).join(',')} onChanged={()=>void load()}/><NativeAccounts /><MetadataSettings /></>}
    {user?.role === 'admin' && <section className="set-section"><div className="set-section-head"><h3>Add a library</h3></div><form className="set-section-body stack" onSubmit={create}>
      <label>Name<input required value={newLibrary.name} onChange={e => setNewLibrary({ ...newLibrary, name: e.target.value })} /></label>
      <label>Folder on the server<input required placeholder="/media/audiobooks" value={newLibrary.path} onChange={e => setNewLibrary({ ...newLibrary, path: e.target.value })} /></label>
      <p className="muted small">Use an existing folder mounted into this app's container.</p>
      <button type="submit" className="btn primary" disabled={busy === 'new'}>Add library</button>
    </form></section>}
  </div>
}
