import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { Field, Modal, errMsg } from './ui'

export type Share = { id:string; name:string; kind:string; target_id:string; episode_id:string; url:string; feed_url:string; has_password:boolean; downloadable:boolean; expires_at:number; revision:number }
export function ShareDialog({kind,target,name,episode='',existing,onClose,onSaved}:{kind:string;target:string;name:string;episode?:string;existing?:Share;onClose:()=>void;onSaved?:()=>void}) {
 const [title,setTitle]=useState(existing?.name || name)
 const [password,setPassword]=useState('')
 const [changePassword,setChangePassword]=useState(!existing)
 const [download,setDownload]=useState(existing?.downloadable || false)
 const [expiry,setExpiry]=useState(()=>{const t=existing?existing.expires_at:Date.now()+7*86400000;return t?new Date(t-new Date(t).getTimezoneOffset()*60000).toISOString().slice(0,16):''})
 const [saved,setSaved]=useState<Share>()
 const [busy,setBusy]=useState(false)
 const [error,setError]=useState('')
 const submit=async(e:React.FormEvent)=>{
  e.preventDefault();setBusy(true);setError('')
  try {
   const body={kind,target_id:target,episode_id:episode,name:title,downloadable:download,expires_at:expiry?new Date(expiry).getTime():0,...(changePassword?{password}:{}),revision:existing?.revision}
   const result=existing?await api.put<Share>(`${V1}/shares/${encodeURIComponent(existing.id)}`,body):await api.post<Share>(`${V1}/shares`,body)
   setSaved(result);onSaved?.()
  } catch(e){setError(errMsg(e))}finally{setBusy(false)}
 }
 return <Modal title={existing?'Edit share':'Share & RSS'} onClose={onClose}>{saved?<div className="stack">
  <p role="status">Share saved</p>
  <Field label="Share page"><input readOnly value={saved.url} onFocus={e=>e.target.select()} /></Field>
  <Field label="RSS feed"><input readOnly value={saved.feed_url} onFocus={e=>e.target.select()} /></Field>
  {saved.has_password && <p>Give the recipient the password separately. RSS clients can use any username and this password.</p>}
  <p><Link to="/shares" onClick={onClose}>Manage and revoke shares</Link></p>
 </div>:<form className="stack" onSubmit={submit}>
  <p>Anyone with this link can listen. Add a password to restrict access. Shared listening does not change your own progress.</p>
  <Field label="Share title"><input required maxLength={300} value={title} onChange={e=>setTitle(e.target.value)} /></Field>
  <Field label="Expires"><input type="datetime-local" value={expiry} onChange={e=>setExpiry(e.target.value)} /><span className="small muted">Leave empty for no expiry. You can revoke the link at any time.</span></Field>
  {existing && <label><input type="checkbox" checked={changePassword} onChange={e=>setChangePassword(e.target.checked)} /> Change password</label>}
  {changePassword && <Field label="Share password (optional)"><input type="password" autoComplete="new-password" minLength={8} maxLength={72} value={password} onChange={e=>setPassword(e.target.value)} /><span className="small muted">Empty means no password.</span></Field>}
  <label><input type="checkbox" checked={download} onChange={e=>setDownload(e.target.checked)} /> Offer original book downloads and companion files</label>
  <p className="small muted">Audio can be saved by RSS clients even when the book-download button is hidden. Collection and series links follow their current contents.</p>
  {error && <p role="alert" className="callout danger">{error}</p>}
  <button className="btn primary" disabled={busy} type="submit">{busy?'Saving…':existing?'Save share':'Create share'}</button>
 </form>}</Modal>
}
