import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApi } from '../lib/use'
import type { Book, UpNextEntry } from '../lib/types'
import { hours } from '../lib/books'
import { Icon } from '../components/icons'
import { BookRow, NotConnected } from '../components/cards'
import { Empty, Loading, PageHeader, errMsg, useToast } from '../components/ui'

/** Books you want to hear next, in your order; a finished book goes on with the first one. */
export default function UpNext() {
  const { toast } = useToast()
  const { data: list, setData, error, notConnected } = useApi<UpNextEntry[]>(`${V1}/up-next`)
  const [books, setBooks] = useState<Record<string, Book>>({})
  useEffect(() => {
    for (const x of list || []) {
      if (books[x.item_id]) continue
      api.get<{ book: Book }>(`${V1}/books/${encodeURIComponent(x.item_id)}`).then(r => setBooks(m => ({ ...m, [x.item_id]: r.book })))
        .catch(() => setBooks(m => ({ ...m, [x.item_id]: { id: x.item_id, library_id: '', title: x.title, author: x.author, genres: [], duration: 0, added_at: 0, chapters: 0, progress: 0, current_time: 0, finished: false, missing: true } })))
    }
  }, [list]) // eslint-disable-line react-hooks/exhaustive-deps
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!list) return <Loading />
  const move = async (i: number, d: number) => {
    const ids = list.map(x => x.item_id)
    const j = i + d
    if (j < 0 || j >= ids.length) return
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
    try { setData(await api.put<UpNextEntry[]>(`${V1}/up-next`, { item_ids: ids })) } catch (e) { toast(errMsg(e), 'err') }
  }
  const remove = async (id: string) => {
    try { await api.del(`${V1}/up-next/${encodeURIComponent(id)}`); setData(list.filter(x => x.item_id !== id)) } catch (e) { toast(errMsg(e), 'err') }
  }
  const total = list.reduce((s, x) => s + Math.max(0, (books[x.item_id]?.duration || 0) - (books[x.item_id]?.current_time || 0)), 0)
  return (
    <div>
      <PageHeader title="Up next" sub={list.length ? `${list.length} ${list.length === 1 ? 'book' : 'books'} · ${hours(total)} of listening` : 'Your own queue'} />
      {list.length === 0 ? (
        <Empty title="Nothing queued" icon="list-ordered">Press “Listen later” on a book. When a book ends, the first one here starts by itself (or the next book of its series).<div className="mt"><Link to="/library" className="btn">Browse the library</Link></div></Empty>
      ) : (
        <ul className="book-list">
          {list.map((x, i) => {
            const b = books[x.item_id]
            if (!b) return <li key={x.id} className="book-row"><span className="br-n tnum">{i + 1}</span><span className="muted">{x.title}</span></li>
            return <BookRow key={x.id} b={b} n={i + 1} actions={<>
              <button type="button" className="btn ghost icon-only" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}><Icon name="arrow-up" size={15} /></button>
              <button type="button" className="btn ghost icon-only" aria-label="Move down" disabled={i === list.length - 1} onClick={() => move(i, 1)}><Icon name="arrow-down" size={15} /></button>
              <button type="button" className="btn ghost icon-only" aria-label="Take off Up next" onClick={() => remove(x.item_id)}><Icon name="x" size={15} /></button>
            </>} />
          })}
        </ul>
      )}
    </div>
  )
}
