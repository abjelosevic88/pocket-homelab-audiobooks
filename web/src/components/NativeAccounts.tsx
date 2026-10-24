import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Field, Modal, errMsg, useToast } from './ui'

type Permissions = { download: boolean; upload: boolean; update: boolean; delete: boolean; accessAllLibraries: boolean; accessAllTags: boolean; accessExplicitContent: boolean; selectedTagsNotAccessible: boolean; librariesAccessible: string[] | null; itemTagsSelected: string[] | null }
type Account = { id: string; user_id: number | null; username: string; active: boolean; locked: boolean; permissions: Permissions; revision: string }
type Data = { accounts: Account[]; users: { id: number; name: string; email: string }[]; libraries: { id: string; name: string }[] }
const empty = (): Account => ({ id: '', user_id: null, username: '', active: true, locked: false, revision: '', permissions: { download: false, upload: false, update: false, delete: false, accessAllLibraries: false, accessAllTags: true, accessExplicitContent: false, selectedTagsNotAccessible: false, librariesAccessible: [], itemTagsSelected: [] } })
export function NativeAccounts() {
  const [data, setData] = useState<Data | null>(null)
  const [edit, setEdit] = useState<Account | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const load = () => api.get<Data>(`${V1}/catalog/accounts`).then(setData).catch(e => setError(errMsg(e)))
  useEffect(() => { void load() }, [])
  const permission = (key: keyof Permissions, value: boolean | string[]) => setEdit(e => e && ({ ...e, permissions: { ...e.permissions, [key]: value } }))
  const save = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try { await api.put(`${V1}/catalog/accounts`, edit); setEdit(null); await load(); toast('Library access saved') }
    catch (e) { setError(errMsg(e)) }
    finally { setBusy(false) }
  }
  const mapped = edit?.id ? data?.accounts.find(a => a.id === edit.id)?.user_id : null
  return <section className="set-section">
    <div className="set-section-head"><div className="grow"><h3>Library access</h3><p>Connect imported listening accounts to people on this box, or grant library access to a new person.</p></div><button className="btn" type="button" onClick={() => { setError(''); setEdit(empty()) }}>Grant access</button></div>
    <div className="set-section-body stack">
      {error && !edit && <p className="callout danger" role="alert">{error}</p>}
      {data?.accounts.map(a => <div className="row" key={a.id}><div className="grow"><b>{a.username}</b><div className="small muted">{data.users.find(u => u.id === a.user_id)?.email || 'Not connected to a person'} · {!a.active ? 'Inactive' : a.locked ? 'Locked' : 'Active'}</div></div><button className="btn" type="button" onClick={() => { setError(''); setEdit(structuredClone(a)) }}>Edit access</button></div>)}
      <p className="small muted">A connected account keeps its owner. To prevent transferring private listening history, existing connections cannot be reassigned.</p>
    </div>
    {edit && data && <Modal title={edit.id ? 'Edit library access' : 'Grant library access'} onClose={() => setEdit(null)}>
      <form className="stack" onSubmit={save}>
        {error && <p role="alert" className="callout danger">{error}</p>}
        <Field label="Account name"><input required value={edit.username} onChange={e => setEdit({ ...edit, username: e.target.value })} /></Field>
        <Field label="Person"><select required={!edit.id} disabled={!!mapped} value={edit.user_id || ''} onChange={e => { const id = Number(e.target.value) || null; const u = data.users.find(u => u.id === id); setEdit({ ...edit, user_id: id, username: edit.username || u?.name || u?.email || '' }) }}><option value="">Not connected</option>{data.users.filter(u => u.id === edit.user_id || !data.accounts.some(a => a.user_id === u.id)).map(u => <option key={u.id} value={u.id}>{u.name || u.email} ({u.email})</option>)}</select></Field>
        <label><input type="checkbox" checked={edit.active} onChange={e => setEdit({ ...edit, active: e.target.checked })} /> Active</label>
        <label><input type="checkbox" checked={edit.locked} onChange={e => setEdit({ ...edit, locked: e.target.checked })} /> Locked</label>
        <label><input type="checkbox" checked={edit.permissions.accessAllLibraries} onChange={e => permission('accessAllLibraries', e.target.checked)} /> All libraries</label>
        {!edit.permissions.accessAllLibraries && <fieldset className="stack"><legend>Allowed libraries</legend>{data.libraries.map(lib => <label key={lib.id}><input type="checkbox" checked={(edit.permissions.librariesAccessible || []).includes(lib.id)} onChange={e => permission('librariesAccessible', e.target.checked ? [...edit.permissions.librariesAccessible || [], lib.id] : (edit.permissions.librariesAccessible || []).filter(id => id !== lib.id))} /> {lib.name}</label>)}</fieldset>}
        <label><input type="checkbox" checked={edit.permissions.accessAllTags} onChange={e => permission('accessAllTags', e.target.checked)} /> All tags</label>
        {!edit.permissions.accessAllTags && <><Field label="Tags" help="Separate tags with a semicolon."><input value={(edit.permissions.itemTagsSelected || []).join('; ')} onChange={e => permission('itemTagsSelected', e.target.value.split(';').map(t => t.trim()).filter(Boolean))} /></Field><label><input type="checkbox" checked={edit.permissions.selectedTagsNotAccessible} onChange={e => permission('selectedTagsNotAccessible', e.target.checked)} /> Hide these tags (otherwise only these tags are allowed)</label></>}
        {([['accessExplicitContent', 'Explicit content'], ['download', 'Download original files'], ['upload', 'Upload books'], ['update', 'Edit books and collections'], ['delete', 'Delete library content']] as const).map(([key, label]) => <label key={key}><input type="checkbox" checked={edit.permissions[key]} onChange={e => permission(key, e.target.checked)} /> {label}</label>)}
        <button type="submit" className="btn primary" disabled={busy}>Save access</button>
      </form>
    </Modal>}
  </section>
}
