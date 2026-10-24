import { useState } from 'react'
import { api,V1 } from '../lib/api'
import { useApi } from '../lib/use'
import { ShareDialog,type Share } from '../components/ShareDialog'
import { Confirm,Loading,errMsg,useToast } from '../components/ui'
import { useCrumb } from '../components/Layout'
export default function Shares(){
 const {data,error,reload}=useApi<Share[]>(`${V1}/shares`)
 const [editing,setEditing]=useState<Share>()
 const [removing,setRemoving]=useState<Share>()
 const {toast}=useToast();useCrumb('Shares & RSS')
 const remove=async()=>{if(!removing)return;try{await api.del(`${V1}/shares/${encodeURIComponent(removing.id)}`);setRemoving(undefined);await reload();toast('Share revoked')}catch(e){toast(errMsg(e),'err')}}
 return <div className="stack loose"><h1>Shares & RSS</h1><p className="muted">Create a link from a book’s More → Share & RSS menu, or from a series or collection.</p>
 {error && <p role="alert" className="callout danger">{error}</p>}{!data&&!error&&<Loading />}
 {data?.length===0&&<p>No shares yet.</p>}
 {data?.map(s=><section className="card side-card stack" key={s.id}><div className="row wrap"><h2 className="grow">{s.name}</h2><button className="btn" onClick={()=>setEditing(s)}>Edit share</button><button className="btn" onClick={()=>setRemoving(s)}>Revoke share</button></div><p>{s.kind} · {s.has_password?'Password protected':'Anyone with the link'} · {s.expires_at?(s.expires_at<Date.now()?'Expired ':'Expires ')+new Date(s.expires_at).toLocaleString():'No expiry'}</p><label>Share page<input aria-label="Share page" readOnly value={s.url} onFocus={e=>e.target.select()} /></label><label>RSS feed<input aria-label="RSS feed" readOnly value={s.feed_url} onFocus={e=>e.target.select()} /></label></section>)}
 {editing&&<ShareDialog kind={editing.kind} target={editing.target_id} name={editing.name} existing={editing} onClose={()=>setEditing(undefined)} onSaved={()=>void reload()} />}
 {removing&&<Confirm title="Revoke share?" message={`The page, feed and media URLs for “${removing.name}” will stop working. Files already downloaded by recipients remain with them.`} confirmLabel="Revoke share" onCancel={()=>setRemoving(undefined)} onConfirm={remove} />}
 </div>
}
