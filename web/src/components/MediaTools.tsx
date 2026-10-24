import {useCallback,useEffect,useState} from 'react'
import {api,V1} from '../lib/api'
import {Loading,Modal,errMsg,useToast} from './ui'
import {fmtBytes,fmtDateTime} from '../lib/format'
type Job={id:string;kind:'m4b'|'embed';status:string;progress:number;error:string;size:number;created_at:number}
type Jobs={revision:number;jobs:Job[]}
export function MediaTools({id,onClose,onSaved}:{id:string;onClose:()=>void;onSaved:()=>void}){
 const [data,setData]=useState<Jobs>();const [error,setError]=useState('');const [busy,setBusy]=useState(false)
 const [kind,setKind]=useState<'m4b'|'embed'>('m4b');const [bitrate,setBitrate]=useState(128);const [cover,setCover]=useState(true)
 const {toast}=useToast();const endpoint=`${V1}/catalog/items/${encodeURIComponent(id)}/tools`
 const load=useCallback(async()=>{try{setData(await api.get<Jobs>(endpoint))}catch(e){setError(errMsg(e))}},[endpoint])
 useEffect(()=>{void load();const timer=setInterval(()=>void load(),2500);return()=>clearInterval(timer)},[load])
 const action=async(fn:()=>Promise<unknown>)=>{setBusy(true);setError('');try{await fn();await load()}catch(e){setError(errMsg(e))}finally{setBusy(false)}}
 const active=data?.jobs.some(j=>['queued','running','applying'].includes(j.status))
 return <Modal title="Audio tools" size="lg" onClose={onClose}><div className="stack">
 <p>Create a downloadable file with this book’s metadata, chapters and cover. You can keep listening while it is generated.</p>
 {error&&<p role="alert" className="callout danger">{error}</p>}{!data&&!error&&<Loading/>}
 <label>Operation<select className="input" value={kind} onChange={e=>setKind(e.target.value as 'm4b'|'embed')}><option value="m4b">Combine tracks into an M4B audiobook</option><option value="embed">Embed metadata in a single MP3, M4B or FLAC</option></select></label>
 {kind==='m4b'?<label>Audio quality<select className="input" value={bitrate} onChange={e=>setBitrate(Number(e.target.value))}>{[64,96,128,192,256,320].map(v=><option key={v} value={v}>{v} kbps</option>)}</select></label>:<p className="muted">Metadata embedding preserves the encoded audio. For a book split into several files, combine the tracks first.</p>}
 <label><input type="checkbox" checked={cover} onChange={e=>setCover(e.target.checked)}/> Include the book cover</label>
 <button className="btn primary" disabled={busy||active||!data} onClick={()=>void action(()=>api.post(endpoint,{kind,bitrate,cover,revision:data?.revision}))}>Generate file</button>
 <p className="muted">“Use for playback” keeps the originals in this book and excludes them from playback. Saved positions, bookmarks and history stay with the book. Pause playback on all devices before using a new file.</p>
 {data?.jobs.map(j=><article className="stack" key={j.id}><div className="row wrap"><strong>{j.kind==='m4b'?'M4B audiobook':'Metadata embedding'}</strong><span>{j.status} · {fmtDateTime(new Date(j.created_at).toISOString())}</span>{j.size>0&&<span>{fmtBytes(j.size)}</span>}</div>
 {j.status==='running'&&<progress max={1} value={j.progress} aria-label="Audio conversion progress"/>}
 {j.error&&<p role="alert" className="callout">{j.error}</p>}
 <div className="row wrap">
 {['queued','running','applying'].includes(j.status)&&<button className="btn" disabled={busy} onClick={()=>void action(()=>api.del(`${V1}/catalog/tools/${j.id}`))}>Cancel job</button>}
 {['failed','cancelled','complete'].includes(j.status)&&<button className="btn" disabled={busy||active} onClick={()=>void action(()=>api.post(`${V1}/catalog/tools/${j.id}/retry`,{}))}>Rebuild</button>}
 {['complete','applied'].includes(j.status)&&<a className="btn" href={`${V1}/catalog/tools/${j.id}/download`}>Download generated file</a>}
 {['complete','applying'].includes(j.status)&&<button className="btn primary" disabled={busy} onClick={()=>void action(async()=>{await api.post(`${V1}/catalog/tools/${j.id}/apply`,{});onSaved();toast('Generated audio selected; originals kept')})}>{j.status==='applying'?'Retry using file':'Use for playback'}</button>}
 {['failed','cancelled','complete','applied'].includes(j.status)&&<button className="btn" disabled={busy} onClick={()=>void action(()=>api.del(`${V1}/catalog/tools/${j.id}`))}>Remove generated download</button>}
 </div></article>)}
 </div></Modal>
}
