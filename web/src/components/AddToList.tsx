import { useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { Field, Loading, Modal, errMsg, useToast } from './ui'

type List = { id: string; name: string; revision: number; entries: { libraryItemId: string; episodeId?: string }[] }
export function AddToList({ book, library, collections, onClose }: { book: string; library: string; collections: boolean; onClose: () => void }) {
  const [kind, setKind] = useState(collections ? 'collections' : 'playlists')
  const [lists, setLists] = useState<List[] | null>(null)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const { toast } = useToast()
  const base = `${V1}/organize/${kind}`
  useEffect(() => { setLists(null); api.get<Record<string, List[]>>(`${base}?library=${encodeURIComponent(library)}`).then(data => setLists(data[kind])).catch(e => setError(errMsg(e))) }, [base, kind, library])
  const add = async (list?: List) => {
    setBusy(true); setError('')
    try {
      if (list) {
        const entries = list.entries.some(e => e.libraryItemId === book && !e.episodeId) ? list.entries : [...list.entries, { libraryItemId: book }]
        await api.put(`${base}/${encodeURIComponent(list.id)}`, { revision: list.revision, [kind === 'collections' ? 'books' : 'items']: kind === 'collections' ? entries.map(e => e.libraryItemId) : entries })
      } else await api.post(base, { libraryId: library, name, [kind === 'collections' ? 'books' : 'items']: kind === 'collections' ? [book] : [{ libraryItemId: book }] })
      toast('Book added'); onClose()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }
  return <Modal title="Add to a list" onClose={onClose}>
    <div className="stack">
      <Field label="List type"><select value={kind} onChange={e => setKind(e.target.value)}>{collections && <option value="collections">Shared collections</option>}<option value="playlists">My playlists</option></select></Field>
      <p className="muted small">{kind === 'collections' ? 'Collections are shared with people who can access this library.' : 'Playlists belong to your account.'}</p>
      {error && <p role="alert" className="callout danger">{error}</p>}
      {!lists ? <Loading /> : lists.map(list => <button type="button" className="btn" key={list.id} disabled={busy} onClick={() => add(list)}>{list.name}</button>)}
      <Field label="New list name"><input value={name} onChange={e => setName(e.target.value)} /></Field>
      <button type="button" className="btn primary" disabled={busy || !name.trim()} onClick={() => add()}>Create and add book</button>
    </div>
  </Modal>
}
