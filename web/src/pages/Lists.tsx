import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApi } from '../lib/use'
import { useCrumb } from '../components/Layout'
import { Confirm, Field, Modal, Loading, errMsg, useToast } from '../components/ui'

type Entry = { libraryItemId: string; episodeId?: string }
type Item = { id: string; media: { metadata: { title: string; authorName: string } } }
type List = { can_edit: boolean; id: string; name: string; description: string; revision: number; entries: Entry[]; books?: Item[]; items?: { libraryItem: Item; episode?: { title: string } }[] }
export default function Lists() {
  const [params, setParams] = useSearchParams()
  const kind = params.get('kind') === 'playlists' ? 'playlists' : 'collections'
  const base = `${V1}/organize/${kind}`
  const { data, error, reload } = useApi<Record<string, List[]>>(base)
  const [deleting, setDeleting] = useState<List | null>(null)
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<List | null>(null)
  const { toast } = useToast()
  useCrumb(kind === 'collections' ? 'Collections' : 'My playlists')
  const update = async (list: List, entries: Entry[]) => {
    setBusy(true)
    try { await api.put(`${base}/${encodeURIComponent(list.id)}`, { revision: list.revision, [kind === 'collections' ? 'books' : 'items']: kind === 'collections' ? entries.map(e => e.libraryItemId) : entries }); await reload() }
    catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy(false) }
  }
  const move = (list: List, index: number, by: number) => { const entries = [...list.entries]; [entries[index], entries[index + by]] = [entries[index + by], entries[index]]; void update(list, entries) }
  const remove = async () => {
    if (!deleting) return
    try { await api.del(`${base}/${encodeURIComponent(deleting.id)}`); setDeleting(null); await reload() }
    catch (e) { toast(errMsg(e), 'err') }
  }
  const rename = async (e: React.FormEvent) => {
    e.preventDefault(); if (!editing) return
    setBusy(true)
    try { await api.put(`${base}/${encodeURIComponent(editing.id)}`, { name: editing.name, description: editing.description, revision: editing.revision }); setEditing(null); await reload() }
    catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy(false) }
  }
  return <div className="stack loose">
    <div className="row wrap"><h1>{kind === 'collections' ? 'Collections' : 'My playlists'}</h1><select aria-label="List type" value={kind} onChange={e => setParams({ kind: e.target.value })}><option value="collections">Shared collections</option><option value="playlists">My playlists</option></select></div>
    <p className="muted">Add books from their More → Add to a list menu.</p>
    {error && <p role="alert" className="callout danger">{error}</p>}
    {!data && !error && <Loading />}
    {data?.[kind]?.length === 0 && <p>No lists yet.</p>}
    {data?.[kind]?.map(list => <section className="set-section" key={list.id}>
      <div className="set-section-head"><div className="grow"><h2>{list.name}</h2>{list.description && <p>{list.description}</p>}</div>{list.can_edit && <><button type="button" className="btn" onClick={() => setEditing(list)}>Edit list</button><button type="button" className="btn" onClick={() => setDeleting(list)}>Delete list</button></>}</div>
      <ol className="set-section-body stack">{list.entries.map((entry, i) => {
        const item = kind === 'collections' ? list.books?.[i] : list.items?.[i]?.libraryItem
        return <li className="row wrap" key={`${entry.libraryItemId}:${entry.episodeId || ''}`}>
          <Link className="grow" to={`/book/${encodeURIComponent(entry.libraryItemId)}`}>{i + 1}. {list.items?.[i]?.episode?.title || item?.media.metadata.title || 'Book'}{item?.media.metadata.authorName ? ` — ${item.media.metadata.authorName}` : ''}</Link>
          <button type="button" className="btn" aria-label={`Move item ${i + 1} up`} disabled={!list.can_edit || busy || i === 0} onClick={() => move(list, i, -1)}>↑</button>
          <button type="button" className="btn" aria-label={`Move item ${i + 1} down`} disabled={!list.can_edit || busy || i === list.entries.length - 1} onClick={() => move(list, i, 1)}>↓</button>
          <button type="button" className="btn" disabled={!list.can_edit || busy} onClick={() => update(list, list.entries.filter((_, j) => j !== i))}>Remove</button>
        </li>
      })}</ol>
    </section>)}
    {editing && <Modal title="Edit list" onClose={() => setEditing(null)}><form className="stack" onSubmit={rename}><Field label="List name"><input required value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} /></Field><Field label="Description"><textarea value={editing.description} onChange={e => setEditing({ ...editing, description: e.target.value })} /></Field><button type="submit" className="btn primary" disabled={busy}>Save list</button></form></Modal>}
    {deleting && <Confirm title="Delete list?" message={`Delete “${deleting.name}”? Its books and listening history will remain.`} confirmLabel="Delete list" onCancel={() => setDeleting(null)} onConfirm={remove} />}
  </div>
}
