import {useEffect,useState} from 'react'
import {api,V1} from '../lib/api'
import {Modal,Loading,errMsg} from './ui'
import {clock} from '../lib/books'
type Track={id:string;name:string;duration:number;excluded:boolean}
export function TrackEditor({id,onClose,onSaved}:{id:string;onClose:()=>void;onSaved:()=>void}){
 const [data,setData]=useState<{revision:number;tracks:Track[]}>(),[reset,setReset]=useState(false),[error,setError]=useState(''),[busy,setBusy]=useState(false)
 const endpoint=`${V1}/catalog/items/${encodeURIComponent(id)}/tracks`
 useEffect(()=>{void api.get<NonNullable<typeof data>>(endpoint).then(setData).catch(e=>setError(errMsg(e)))},[endpoint])
 const move=(index:number,offset:number)=>setData(v=>{if(!v)return v;const tracks=[...v.tracks];[tracks[index],tracks[index+offset]]=[tracks[index+offset],tracks[index]];return {...v,tracks}})
 const save=async()=>{if(!data)return;setBusy(true);setError('');try{await api.put(endpoint,{revision:data.revision,tracks:data.tracks.map(t=>({id:t.id,excluded:t.excluded})),reset_chapters:reset});onSaved()}catch(e){setError(errMsg(e))}finally{setBusy(false)}}
 return <Modal title="Manage tracks" size="lg" onClose={onClose}><div className="stack">{error&&<p role="alert" className="callout danger">{error}</p>}{!data&&!error&&<Loading/>}{data&&<><p>Reorder files or exclude them from playback. Audio files stay unchanged. Pause playback on every device before saving.</p><p className="callout">Saved positions and bookmarks keep their elapsed times, so they may point to different content after a reorder. Listening history is preserved.</p>{data.tracks.map((t,i)=><div className="row wrap" key={t.id}><label className="grow"><input type="checkbox" checked={!t.excluded} onChange={e=>setData({...data,tracks:data.tracks.map(v=>v.id===t.id?{...v,excluded:!e.target.checked}:v)})}/> {t.name} · {clock(t.duration)}</label><button className="btn" aria-label={`Move ${t.name} up`} disabled={i===0||busy} onClick={()=>move(i,-1)}>Up</button><button className="btn" aria-label={`Move ${t.name} down`} disabled={i===data.tracks.length-1||busy} onClick={()=>move(i,1)}>Down</button></div>)}<label><input type="checkbox" checked={reset} onChange={e=>setReset(e.target.checked)}/> Replace current chapters with one chapter per included file</label><a className="link" href={`${V1}/catalog/items/${encodeURIComponent(id)}/metadata`}>Export existing chapter metadata</a><button className="btn primary" disabled={busy||!data.tracks.some(t=>!t.excluded&&t.duration>0)} onClick={save}>Save track order</button></>}</div></Modal>
}
