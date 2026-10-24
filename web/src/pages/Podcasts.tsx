import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import {EpisodeEditor} from '../components/EpisodeEditor'
import {TrashDialog} from '../components/TrashDialog'
import { ShareDialog } from '../components/ShareDialog'
import { usePlayer } from '../lib/player'
import type { Book } from '../lib/types'
import { Loading, errMsg, useToast } from '../components/ui'

type Show = { id: string; title: string; author: string; library_id: string }
type Library = { id: string; name: string; folders: { id: string; fullPath?: string; path?: string }[] }
type Episode = { id: string; title: string; publishedAt: number; downloaded: boolean; in_trash: boolean; job_id?: string; status?: string; error?: string }
type Settings = { auto_download: boolean; interval_seconds: number; max_new: number; keep_latest: number }
type Detail = { item: Show; episodes: Episode[]; settings: Settings; can_manage: boolean; can_delete: boolean; error: string; last_check: number }
export default function Podcasts() {
  const { id } = useParams()
  const {user}=useApp()
  const [sharing,setSharing]=useState<{id:string;title:string}>()
  const [editing,setEditing]=useState<string>()
  const [term,setTerm]=useState('')
  const [results,setResults]=useState<{id:number;title:string;artistName:string;feedUrl:string;itunesPageUrl:string}[]>([])
  const [trashing,setTrashing]=useState<{id:string;title:string}>()
  const [data, setData] = useState<{ items: Show[]; libraries: Library[]; can_subscribe: boolean } | null>(null)
  const [detail, setDetail] = useState<Detail | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [url, setURL] = useState('')
  const [library, setLibrary] = useState('')
  const [folder, setFolder] = useState('')
  const [imports, setImports] = useState<{ title: string; url: string; status?: string }[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const p = usePlayer(), navigate = useNavigate(), { toast } = useToast()
  const load = async () => {
    try {
      if (id) { const next = await api.get<Detail>(`${V1}/podcasts/${encodeURIComponent(id)}`); setDetail(next); setSettings(s => s || next.settings) }
      else { const next = await api.get<NonNullable<typeof data>>(`${V1}/podcasts`); setData(next); setLibrary(v => v || next.libraries[0]?.id || ''); setFolder(v => v || next.libraries[0]?.folders[0]?.id || '') }
      setError('')
    } catch (e) { setError(errMsg(e)) }
  }
  useEffect(() => { setDetail(null); setSettings(null); void load(); const timer = window.setInterval(load, 5000); return () => clearInterval(timer) }, [id]) // eslint-disable-line react-hooks/exhaustive-deps
  const act = async (fn: () => Promise<unknown>) => { setBusy(true); try { await fn(); await load() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
  const subscribe = (feed: string) => api.post<{ id: string }>(`${V1}/podcasts/subscribe`, { url: feed, library_id: library, folder_id: folder })
  if (error) return <p role="alert" className="callout danger">{error}</p>
  if (id) {
    if (!detail || !settings) return <Loading />
    return <div className="stack loose"><Link to="/podcasts">All podcasts</Link><h1>{detail.item.title}</h1><p className="muted">{detail.item.author}</p>
      {user?.role==='admin' && <button className="btn" onClick={()=>setSharing({id:'',title:detail.item.title})}>Share podcast & RSS</button>}
      {detail.can_delete&&<button className="btn" onClick={()=>setTrashing({id:'',title:detail.item.title})}>Move podcast to trash</button>}
      {trashing&&<TrashDialog id={id} episode={trashing.id} title={trashing.title} onClose={()=>setTrashing(undefined)} onRemoved={()=>{if(p.isCurrent(id))p.stop();setTrashing(undefined);if(!trashing.id)navigate('/trash');else void load()}}/>}
      {editing&&<EpisodeEditor item={id} id={editing} onClose={()=>setEditing(undefined)} onSaved={()=>{setEditing(undefined);void load()}}/>}
      {sharing && <ShareDialog kind="item" target={id} episode={sharing.id} name={sharing.title} onClose={()=>setSharing(undefined)} />}
      {detail.can_manage && <details><summary>Subscription settings</summary><form className="stack mt" onSubmit={e => { e.preventDefault(); void act(() => api.put(`${V1}/podcasts/${encodeURIComponent(id)}/settings`, settings)) }}>
        <label><input type="checkbox" checked={settings.auto_download} onChange={e => setSettings({ ...settings, auto_download: e.target.checked })} /> Download newly published episodes automatically</label>
        <label>Check every <select value={settings.interval_seconds} onChange={e => setSettings({ ...settings, interval_seconds: Number(e.target.value) })}><option value={300}>5 minutes</option><option value={3600}>hour</option><option value={21600}>6 hours</option><option value={86400}>day</option></select></label>
        <label>Maximum new episodes per check<input type="number" min={1} max={100} value={settings.max_new} onChange={e => setSettings({ ...settings, max_new: Number(e.target.value) })} /></label>
        <label>Keep latest downloaded episodes (0 keeps all)<input type="number" min={0} max={10000} disabled={!detail.can_delete} value={settings.keep_latest} onChange={e=>setSettings({...settings,keep_latest:Number(e.target.value)})}/></label><p className="small muted">Older downloads move to recoverable Trash. Permanently remove them there to free disk space. Recently active playback is protected.</p>
        <button className="btn primary" disabled={busy}>Save subscription settings</button>
      </form></details>}
      {detail.can_manage && <button type="button" className="btn" disabled={busy} onClick={() => act(() => api.post(`${V1}/podcasts/${encodeURIComponent(id)}/check`, {}))}>Check now</button>}
      {detail.error && <p className="callout danger">{detail.error}</p>}
      {!detail.episodes.length && <p className="muted">Check the feed to find episodes.</p>}
      {detail.episodes.map(ep => <section className="set-section" key={ep.id}><div className="set-section-body row wrap"><div className="grow"><b>{ep.title}</b><p className="small muted">{ep.publishedAt ? new Date(ep.publishedAt).toLocaleDateString() : ''} {ep.status && ep.status !== 'complete' ? ` · ${ep.status}` : ''}</p>{ep.error && <p className="small danger-text">{ep.error}</p>}</div>
        {detail.can_manage&&<button className="btn" onClick={()=>setEditing(ep.id)}>Edit episode</button>}{ep.in_trash&&<Link to="/trash">In trash</Link>}{ep.downloaded&&detail.can_delete&&<button className="btn" onClick={()=>setTrashing({id:ep.id,title:ep.title})}>Move episode to trash</button>}
        {ep.downloaded && user?.role==='admin' && <button className="btn" onClick={()=>setSharing({id:ep.id,title:ep.title})}>Share episode</button>}
        {ep.downloaded ? <button type="button" className="btn primary" disabled={busy} onClick={() => act(async () => { const { book } = await api.get<{ book: Book }>(`${V1}/podcasts/${encodeURIComponent(id)}/episodes/${encodeURIComponent(ep.id)}`); p.play(book) })}>Play episode</button> : detail.can_manage && (ep.status === 'queued' || ep.status === 'running' ? <button type="button" className="btn" disabled={busy} onClick={() => act(() => api.del(`${V1}/podcasts/jobs/${encodeURIComponent(ep.job_id!)}`))}>Cancel download</button> : <button type="button" className="btn" disabled={busy} onClick={() => act(() => api.post(`${V1}/podcasts/${encodeURIComponent(id)}/episodes/${encodeURIComponent(ep.id)}/download`, {}))}>Download episode</button>)}
      </div></section>)}
    </div>
  }
  if (!data) return <Loading />
  return <div className="stack loose"><div className="row wrap"><h1 className="grow">Podcasts</h1><a className="btn" href={`${V1}/podcasts/opml`}>Export OPML</a></div>
    {data.can_subscribe && <details><summary>Subscribe or import</summary><div className="stack mt">
      {!data.libraries.length ? <p>Add a podcast library in <Link to="/settings/audiobookshelf">Libraries settings</Link>.</p> : <>
        <label>Podcast library<select value={library} onChange={e => { setLibrary(e.target.value); setFolder(data.libraries.find(l => l.id === e.target.value)?.folders[0]?.id || '') }}>{data.libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</select></label>
        <label>Folder<select value={folder} onChange={e => setFolder(e.target.value)}>{data.libraries.find(l => l.id === library)?.folders.map(f => <option key={f.id} value={f.id}>{f.fullPath || f.path || f.id}</option>)}</select></label>
        <form className="row wrap" onSubmit={e=>{e.preventDefault();void act(async()=>setResults(await api.get(`${V1}/podcasts/search?term=${encodeURIComponent(term)}`)))}}><label className="grow">Search Apple Podcasts<input value={term} minLength={2} required onChange={e=>setTerm(e.target.value)}/></label><button className="btn" disabled={busy}>Search podcasts</button></form>
        {results.map(r=><div className="card side-card stack" key={r.id}><b>{r.title}</b><p>{r.artistName}</p><div className="row wrap">{r.itunesPageUrl&&<a href={r.itunesPageUrl} target="_blank" rel="noreferrer" className="btn">View on Apple Podcasts</a>}<button className="btn" disabled={busy||!folder} onClick={()=>act(async()=>{const v=await subscribe(r.feedUrl);navigate(`/podcasts/${v.id}`)})}>Subscribe to {r.title}</button></div></div>)}
        <form className="row wrap" onSubmit={e => { e.preventDefault(); void act(async () => { const result = await subscribe(url); navigate(`/podcasts/${result.id}`) }) }}><label className="grow">RSS or Atom feed URL<input type="url" required value={url} onChange={e => setURL(e.target.value)} /></label><button className="btn primary" disabled={busy || !folder}>Subscribe</button></form>
        <label>Import OPML<input type="file" accept=".opml,.xml,text/xml" disabled={busy} onChange={e => { const file = e.target.files?.[0]; if (!file) return; void act(async () => { if (file.size > 1024 * 1024) throw new Error('Use an OPML file under 1 MiB'); const result = await api.post<{ subscriptions: typeof imports }>(`${V1}/podcasts/opml`, { opml: await file.text() }); setImports(result.subscriptions) }) }} /></label>
        {imports.length > 0 && <><p>{imports.length} subscriptions in this file.</p><button type="button" className="btn" disabled={busy || !folder} onClick={() => act(async () => { for (let i = 0; i < imports.length; i++) { if (imports[i].status === 'Imported') continue; let status = 'Imported'; try { await subscribe(imports[i].url) } catch (e) { status = errMsg(e) }; setImports(list => list.map((s, j) => j === i ? { ...s, status } : s)) } })}>Import subscriptions</button>{imports.map((s, i) => <p className="small" key={i}>{s.title || s.url} · {s.status || 'Ready'}</p>)}</>}
      </>}
    </div></details>}
    {data.items.map(show => <section className="set-section" key={show.id}><div className="set-section-body"><h3><Link to={`/podcasts/${show.id}`}>{show.title}</Link></h3><p className="muted">{show.author}</p></div></section>)}
    {!data.items.length && <p className="muted">No podcast subscriptions yet.</p>}
  </div>
}
