import { useParams, useSearchParams } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { usePlayer } from '../lib/player'
import { useApi } from '../lib/use'
import type { Book } from '../lib/types'
import { V1, hours } from '../lib/books'
import { Icon } from '../components/icons'
import { BookRow, Img, NotConnected } from '../components/cards'
import { useCrumb } from '../components/Layout'
import { Loading } from '../components/ui'

interface SeriesT { id: string; library_id: string; name: string; books: Book[]; duration: number; left: number; finished: number }

export default function SeriesPage() {
  const { id = '' } = useParams()
  const [sp] = useSearchParams()
  const lib = sp.get('library') || localStorage.getItem('pab-library') || ''
  const { settings } = useApp()
  const p = usePlayer()
  const { data: s, error, notConnected } = useApi<SeriesT>(`${V1}/series/${encodeURIComponent(id)}?library=${encodeURIComponent(lib)}`)
  useCrumb(s?.name)
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!s) return <Loading />
  const next = s.books.find(b => !b.finished)
  const inProgress = s.books.find(b => !b.finished && b.current_time > 0)
  const go = inProgress || next
  return (
    <div>
      <section className="series-hero">
        <div className="series-covers">{s.books.slice(0, 4).map((b, i) => <Img key={b.id} src={b.cover} w={240} alt={b.title} className={`sc${i}`} />)}</div>
        <div className="grow">
          <p className="eyebrow">Series</p>
          <h1 className="book-title">{s.name}</h1>
          <p className="muted">{s.books.length} {s.books.length === 1 ? 'book' : 'books'} · {hours(s.duration)}{s.finished > 0 ? ` · ${s.finished} finished · ${hours(s.left / (settings?.speed || 1))} to go` : ''}</p>
          {s.books[0]?.author && <p className="muted small">{s.books[0].author}</p>}
          {go && <div className="row mt"><button type="button" className="btn primary lg" onClick={() => p.play(go)}><Icon name="play" size={16} />{inProgress ? `Continue book ${go.sequence || ''}` : s.finished ? `Start book ${go.sequence || 'next'}` : 'Start the series'}</button></div>}
          {!go && s.books.length > 0 && <p className="ok-text mt"><Icon name="check" size={14} /> You finished the whole series.</p>}
        </div>
      </section>
      <ul className="book-list">{s.books.map(b => <BookRow key={b.id} b={b} n={b.sequence || '·'} />)}</ul>
    </div>
  )
}
