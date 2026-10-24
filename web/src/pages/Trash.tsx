import {useState} from 'react'
import {api,V1} from '../lib/api'
import {useApi} from '../lib/use'
import {Confirm,Loading,errMsg,useToast} from '../components/ui'
import {useCrumb} from '../components/Layout'
type Entry={id:string;title:string;episode_id:string;move_files:boolean;state:string;operation:string;created_at:number;error?:string}
export default function Trash(){
 const {data,error,reload}=useApi<Entry[]>(`${V1}/catalog/trash`)
 const [purging,setPurging]=useState<Entry>(),[busy,setBusy]=useState(false)
 const {toast}=useToast();useCrumb('Trash')
 const act=async(v:Entry,action:string)=>{setBusy(true);try{await api.post(`${V1}/catalog/trash/${encodeURIComponent(v.id)}/${action}`,{confirm:action==='purge'});setPurging(undefined);toast(action==='restore'?'Restored':action==='purge'?'Files permanently removed':'Operation completed')}catch(e){toast(errMsg(e),'err')}finally{await reload();setBusy(false)}}
 return <div className="stack loose"><h1>Trash</h1><p className="muted">Deleted books and episodes stay here until restored. Moving files to trash does not free disk space; permanent removal does. Listening history is preserved.</p>{error&&<p role="alert" className="callout danger">{error}</p>}{!data&&!error&&<Loading/>}{data?.length===0&&<p>Trash is empty.</p>}{data?.map(v=><section className="card side-card stack" key={v.id}><h2>{v.title}</h2><p>{v.episode_id?'Episode':'Library item'} · {new Date(v.created_at).toLocaleString()} · {v.state==='purged'?'Permanently removed':v.state} · {v.move_files?'Files included':'Catalog entry only'}</p>{v.error&&<p role="alert" className="callout danger">{v.error}</p>}{v.state!=='purged'&&<div className="row wrap"><button className="btn" disabled={busy||!['deleted','error'].includes(v.state)||v.operation==='purge'} onClick={()=>act(v,'restore')}>Restore</button>{v.state==='error'&&<button className="btn" disabled={busy} onClick={()=>act(v,'retry')}>Retry {v.operation}</button>}{v.state==='deleted'&&v.move_files&&<button className="btn danger" disabled={busy} onClick={()=>setPurging(v)}>Remove files permanently</button>}</div>}</section>)}{purging&&<Confirm title="Permanently remove files?" message={`Remove the files in trash for “${purging.title}”? This cannot be undone. Listening history remains.`} confirmLabel="Remove files permanently" onCancel={()=>setPurging(undefined)} onConfirm={()=>act(purging,'purge')}/>}</div>
}
