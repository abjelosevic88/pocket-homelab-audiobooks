// The library: books (sorted, filtered, loading more as you scroll), series and authors, one library at a time.
import { useEffect, useMemo, useRef, useState } from 'react'
import { NavLink, useSearchParams } from 'react-router-dom'
import { api, ApiError } from '../lib/api'
import { useApi } from '../lib/use'
import type { AuthorCard, Book, Library as LibraryT, SeriesCard } from '../lib/types'
import { V1 } from '../lib/books'
import { Icon } from '../components/icons'
import { AuthorTile, BookCard, NotConnected, SeriesTile } from '../components/cards'
import { Select } from '../components/Select'
import { Empty, Loading, PageHeader, Segmented, errMsg, useDebounce } from '../components/ui'

const SORTS = [
  { value: 'added', label: 'Recently added' }, { value: 'title', label: 'Title' }, { value: 'author', label: 'Author' }, { value: 'year', label: 'Published' },
  { value: 'length', label: 'Length' }, { value: 'progress', label: 'Last listened' }, { value: 'random', label: 'Shuffle' },
]
const LIB_KEY = 'pab-library'

/** The libraries and the one picked (URL first, then the last one used). */
function useLibrary() {
  const [sp, setSp] = useSearchParams()
  const { data: libs, notConnected, error } = useApi<LibraryT[]>(`${V1}/libraries`)
  const asked = sp.get('library') || localStorage.getItem(LIB_KEY) || ''
  const lib = libs ? (libs.find(l => l.id === asked) || libs[0]) : undefined
  const pick = (id: string) => { localStorage.setItem(LIB_KEY, id); const n = new URLSearchParams(sp); n.set('library', id); setSp(n, { replace: true }) }
  return { libs, lib, pick, notConnected, error }
}

function Tabs() {
  return (
    <nav className="lib-tabs" aria-label="Library">
      <NavLink to="/library" end className={({ isActive }) => isActive ? 'active' : ''}><Icon name="book" size={14} />Books</NavLink>
      <NavLink to="/library/series" className={({ isActive }) => isActive ? 'active' : ''}><Icon name="layers" size={14} />Series</NavLink>
      <NavLink to="/library/authors" className={({ isActive }) => isActive ? 'active' : ''}><Icon name="feather" size={14} />Authors</NavLink>
    </nav>
  )
}

function LibraryPicker({ libs, lib, pick }: { libs: LibraryT[]; lib?: LibraryT; pick: (id: string) => void }) {
  if (libs.length < 2) return null
  if (libs.length <= 4) return <Segmented size="sm" value={lib?.id || ''} onChange={pick} options={libs.map(l => ({ value: l.id, label: l.name }))} label="Library" />
  return <Select ariaLabel="Library" value={lib?.id || ''} onChange={pick} options={libs.map(l => ({ value: l.id, label: l.name }))} />
}

export default function Library({ view }: { view: 'books' | 'series' | 'authors' }) {
  const { libs, lib, pick, notConnected, error } = useLibrary()
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!libs) return <Loading />
  if (!libs.length) return <Empty title="No book libraries" icon="book">Audiobookshelf has no book library you can open (podcast libraries belong to Pocket Podcasts), or you hid them all in Settings.</Empty>
  return (
    <div>
      <PageHeader title={view === 'books' ? 'Books' : view === 'series' ? 'Series' : 'Authors'} sub={lib ? lib.name : ''} actions={<LibraryPicker libs={libs} lib={lib} pick={pick} />} />
      <Tabs />
      {lib && view === 'books' && <Books lib={lib.id} key={lib.id} />}
      {lib && view === 'series' && <Series lib={lib.id} key={lib.id} />}
      {lib && view === 'authors' && <Authors lib={lib.id} key={lib.id} />}
    </div>
  )
}

/** Pages of a list, loading the next one as the end comes into view. */
function usePaged<T>(url: (page: number) => string, key: string) {
  const [items, setItems] = useState<T[]>([])
  const [total, setTotal] = useState(-1)
  const [err, setErr] = useState<{ msg: string; nc: boolean } | null>(null)
  const [busy, setBusy] = useState(false)
  const page = useRef(0)
  const live = useRef(key)
  const load = async (p: number) => {
    setBusy(true)
    const k = key
    try {
      const r = await api.get<{ items: T[]; total: number }>(url(p))
      if (live.current !== k) return
      setItems(prev => p === 0 ? r.items : [...prev, ...r.items])
      setTotal(r.total)
      page.current = p
      setErr(null)
    } catch (e) {
      setErr({ msg: errMsg(e), nc: e instanceof ApiError && e.data.code === 'not_connected' })
    } finally { if (live.current === k) setBusy(false) }
  }
  useEffect(() => { live.current = key; setItems([]); setTotal(-1); load(0) }, [key]) // eslint-disable-line react-hooks/exhaustive-deps
  const more = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = more.current
    if (!el) return
    const io = new IntersectionObserver(es => { if (es[0].isIntersecting && !busy && items.length < total) load(page.current + 1) }, { rootMargin: '600px' })
    io.observe(el)
    return () => io.disconnect()
  }) // eslint-disable-line react-hooks/exhaustive-deps
  return { items, total, err, busy, more }
}

function Books({ lib }: { lib: string }) {
  const [sp, setSp] = useSearchParams()
  const sort = sp.get('sort') || 'added'
  const show = sp.get('show') || 'all'
  const genre = sp.get('genre') || ''
  const narrator = sp.get('narrator') || ''
  const set = (k: string, v: string) => { const n = new URLSearchParams(sp); if (v && v !== 'all') n.set(k, v); else n.delete(k); setSp(n, { replace: true }) }
  const desc = sort === 'added' || sort === 'progress' || sort === 'year'
  const key = `${lib}|${sort}|${show}|${genre}|${narrator}`
  const { items, total, err, busy, more } = usePaged<Book>(page => {
    const p = new URLSearchParams({ library: lib, sort, page: String(page), limit: '60' })
    if (desc) p.set('desc', '1')
    if (show !== 'all') p.set('show', show)
    if (genre) p.set('genre', genre)
    if (narrator) p.set('narrator', narrator)
    return `${V1}/books?${p}`
  }, key)
  if (err?.nc) return <NotConnected message={err.msg} />
  return (
    <>
      <div className="toolbar row wrap">
        <Segmented size="sm" value={show} onChange={v => set('show', v)} options={[{ value: 'all', label: 'All' }, { value: 'in-progress', label: 'In progress' }, { value: 'not-started', label: 'Not started' }, { value: 'finished', label: 'Finished' }]} label="Show" />
        {(genre || narrator) && <button type="button" className="chip on" onClick={() => { set('genre', ''); set('narrator', '') }}>{genre ? `Genre: ${genre}` : `Read by ${narrator}`}<Icon name="x" size={12} /></button>}
        <span className="spacer" />
        <span className="muted small">{total >= 0 ? `${total.toLocaleString()} ${total === 1 ? 'book' : 'books'}` : ''}</span>
        <Select ariaLabel="Sort" value={sort} onChange={v => set('sort', v)} options={SORTS} />
      </div>
      {err && <div className="callout danger"><Icon name="alert" size={15} />{err.msg}</div>}
      {total === 0 ? <Empty title="No books here" icon="book">{show !== 'all' ? 'Nothing matches this filter.' : 'This library is empty.'}</Empty> : (
        <div className="grid-covers">{items.map(b => <BookCard key={b.id} b={b} />)}</div>
      )}
      <div ref={more} className="more-sentinel">{busy && <Loading label="" />}</div>
    </>
  )
}

function Series({ lib }: { lib: string }) {
  const [sort, setSort] = useState(() => localStorage.getItem('pab-series-sort') || 'name')
  const { items, total, err, busy, more } = usePaged<SeriesCard>(page => `${V1}/series?${new URLSearchParams({ library: lib, sort, desc: sort === 'name' ? '' : '1', page: String(page), limit: '48' })}`, `${lib}|${sort}`)
  if (err?.nc) return <NotConnected message={err.msg} />
  return (
    <>
      <div className="toolbar row wrap">
        <span className="muted small">{total >= 0 ? `${total.toLocaleString()} series` : ''}</span>
        <span className="spacer" />
        <Select ariaLabel="Sort" value={sort} onChange={v => { setSort(v); localStorage.setItem('pab-series-sort', v) }} options={[{ value: 'name', label: 'Name' }, { value: 'added', label: 'Recently added' }, { value: 'books', label: 'Most books' }, { value: 'length', label: 'Longest' }]} />
      </div>
      {err && <div className="callout danger"><Icon name="alert" size={15} />{err.msg}</div>}
      {total === 0 ? <Empty title="No series" icon="layers">Books with a series in Audiobookshelf show up here.</Empty> : <div className="grid-covers">{items.map(s => <SeriesTile key={s.id} s={s} />)}</div>}
      <div ref={more} className="more-sentinel">{busy && <Loading label="" />}</div>
    </>
  )
}

function Authors({ lib }: { lib: string }) {
  const { data, notConnected, error } = useApi<{ items: AuthorCard[] }>(`${V1}/authors?library=${encodeURIComponent(lib)}`)
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 150)
  const shown = useMemo(() => (data?.items || []).filter(a => !dq.trim() || a.name.toLowerCase().includes(dq.trim().toLowerCase())), [data, dq])
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!data) return <Loading />
  return (
    <>
      <div className="toolbar row wrap">
        <div className="search-box"><Icon name="search" size={15} /><input value={q} onChange={e => setQ(e.target.value)} placeholder="Find an author" aria-label="Find an author" /></div>
        <span className="spacer" />
        <span className="muted small">{data.items.length.toLocaleString()} authors</span>
      </div>
      {shown.length === 0 ? <Empty title="No authors" icon="feather" /> : <div className="grid-people">{shown.map(a => <AuthorTile key={a.id} a={a} />)}</div>}
    </>
  )
}
