// The library: books (sorted, filtered, loading more as you scroll), series and authors, one library at a time.
import {BulkBookEditor} from '../components/BulkBookEditor'
import {QuickMatchDialog} from '../components/QuickMatchDialog'
import { AuthorMatchAllDialog } from '../components/AuthorDialogs'
import { FindCoversDialog, TidyTitlesDialog } from '../components/TidyDialogs'
import {useApp} from '../lib/app-context'
import { useEffect, useMemo, useRef, useState } from 'react'
import { NavLink, useSearchParams } from 'react-router-dom'
import { api, ApiError } from '../lib/api'
import { useApi } from '../lib/use'
import type { AuthorCard, Book, Library as LibraryT, SeriesCard } from '../lib/types'
import { V1 } from '../lib/books'
import { onBookFinished } from '../lib/live-progress'
import { Icon } from '../components/icons'
import { AuthorTile, BookCard, NotConnected, SeriesTile } from '../components/cards'
import { Select } from '../components/Select'
import { UploadBook } from '../components/UploadBook'
import { Empty, Loading, MenuButton, PageHeader, Segmented, errMsg, useDebounce } from '../components/ui'

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
  const [uploading, setUploading] = useState(false)
  const [revision, setRevision] = useState(0)
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!libs) return <Loading />
  if (!libs.length) return <Empty title="No book libraries" icon="book">No book library is available to your account. Add one in Settings, or check which libraries are hidden.</Empty>
  return (
    <div>
      <PageHeader title={view === 'books' ? 'Books' : view === 'series' ? 'Series' : 'Authors'} sub={lib ? lib.name : ''} actions={<div className="row wrap"><LibraryPicker libs={libs} lib={lib} pick={pick} /><button className="btn" onClick={() => setRevision(n => n + 1)}>Refresh</button><button className="btn primary" onClick={() => setUploading(true)}><Icon name="upload" size={16} />Upload</button></div>} />
      <Tabs />
      {uploading && <UploadBook library={lib?.id} onClose={() => { setUploading(false); setRevision(n => n + 1) }} onUploaded={id => { pick(id); setRevision(n => n + 1) }} />}
      {lib && view === 'books' && <Books lib={lib.id} key={`${lib.id}-${revision}`} />}
      {lib && view === 'series' && <Series lib={lib.id} key={`${lib.id}-${revision}`} />}
      {lib && view === 'authors' && <Authors lib={lib.id} key={`${lib.id}-${revision}`} />}
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
  /** Reloads the pages already shown, in place (no flash, scroll kept). */
  const refresh = async () => {
    const k = key
    try {
      const all: T[] = []
      let t = total
      for (let p = 0; p <= page.current; p++) {
        const r = await api.get<{ items: T[]; total: number }>(url(p))
        all.push(...r.items); t = r.total
      }
      if (live.current === k) { setItems(all); setTotal(t) }
    } catch { /* the next event or a manual refresh tries again */ }
  }
  return { items, total, err, busy, more, refresh }
}

function Books({ lib }: { lib: string }) {
  const {settings}=useApp()
  const [selecting,setSelecting]=useState(false),[selected,setSelected]=useState<string[]>([]),[editing,setEditing]=useState(false),[matching,setMatching]=useState<string[]|null>(null),[revision,setRevision]=useState(0)
  const [tidy, setTidy] = useState<'' | 'titles' | 'covers'>('')

  const [sp, setSp] = useSearchParams()
  const sort = sp.get('sort') || 'added'
  const show = sp.get('show') || 'all'
  const genre = sp.get('genre') || ''
  const narrator = sp.get('narrator') || ''
  const set = (k: string, v: string) => { const n = new URLSearchParams(sp); if (v && v !== 'all') n.set(k, v); else n.delete(k); setSp(n, { replace: true }) }
  const desc = sort === 'added' || sort === 'progress' || sort === 'year'
  const key = `${lib}|${sort}|${show}|${genre}|${narrator}|${revision}`
  useEffect(()=>{setSelected([])},[key])
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
        {settings?.backend==='native'&&<button className="btn" onClick={()=>{setSelecting(!selecting);setSelected([])}}>{selecting?'Cancel selection':'Select books'}</button>}
        {selecting&&<><button className="btn" onClick={()=>setSelected(items.slice(0,100).map(b=>b.id))}>Select visible books</button><button className="btn primary" disabled={!selected.length} onClick={()=>setEditing(true)}>Edit selected ({selected.length})</button></>}
        {settings?.backend==='native'&&<button className="btn" disabled={selecting&&!selected.length} onClick={()=>setMatching(selecting?selected:[])}>{selecting?`Quick match selected (${selected.length})`:'Quick match'}</button>}
        {settings?.backend==='native'&&!selecting&&<MenuButton className="btn" label="Tidy up the library" items={[{ label: 'Tidy titles & series…', icon: 'wand', onSelect: () => setTidy('titles') }, { label: 'Find missing covers…', icon: 'book-open', onSelect: () => setTidy('covers') }]}><Icon name="sparkles" size={15} />Tidy up</MenuButton>}
        <span className="spacer" />
        <span className="muted small">{total >= 0 ? `${total.toLocaleString()} ${total === 1 ? 'book' : 'books'}` : ''}</span>
        <Select ariaLabel="Sort" value={sort} onChange={v => set('sort', v)} options={SORTS} />
      </div>
      {err && <div className="callout danger"><Icon name="alert" size={15} />{err.msg}</div>}
      {total === 0 ? <Empty title="No books here" icon="book">{show !== 'all' ? 'Nothing matches this filter.' : 'This library is empty.'}</Empty> : (
        <div className="grid-covers">{items.map(b => selecting?<div key={b.id}><label><input type="checkbox" aria-label={`Select ${b.title}`} disabled={selected.length>=100&&!selected.includes(b.id)} checked={selected.includes(b.id)} onChange={e=>setSelected(v=>e.target.checked?[...v,b.id]:v.filter(id=>id!==b.id))}/> Select</label><BookCard b={b}/></div>:<BookCard key={b.id} b={b} />)}</div>
      )}
      {matching&&<QuickMatchDialog library={lib} ids={matching} onClose={()=>setMatching(null)} onDone={()=>setRevision(n=>n+1)}/>}
      {tidy==='titles'&&<TidyTitlesDialog library={lib} onClose={()=>setTidy('')} onDone={()=>setRevision(n=>n+1)}/>}
      {tidy==='covers'&&<FindCoversDialog library={lib} onClose={()=>setTidy('')} onDone={()=>setRevision(n=>n+1)}/>}
      {editing&&<BulkBookEditor ids={selected} onClose={()=>setEditing(false)} onSaved={()=>{setEditing(false);setSelecting(false);setRevision(n=>n+1)}}/>}
      <div ref={more} className="more-sentinel">{busy && <Loading label="" />}</div>
    </>
  )
}

function Series({ lib }: { lib: string }) {
  const [sort, setSort] = useState(() => localStorage.getItem('pab-series-sort') || 'name')
  const { items, total, err, busy, more, refresh } = usePaged<SeriesCard>(page => `${V1}/series?${new URLSearchParams({ library: lib, sort, desc: sort === 'name' ? '' : '1', page: String(page), limit: '48' })}`, `${lib}|${sort}`)
  // Finished counts and "all finished" badges follow listening on any device.
  const refreshLater = useRef(refresh)
  refreshLater.current = refresh
  useEffect(() => {
    let t: number | undefined
    const off = onBookFinished(() => { clearTimeout(t); t = window.setTimeout(() => void refreshLater.current(), 1500) })
    return () => { off(); clearTimeout(t) }
  }, [])
  if (err?.nc) return <NotConnected message={err.msg} />
  return (
    <>
      <div className="toolbar row wrap">
        <span className="muted small">{total >= 0 ? `${total.toLocaleString()} series` : ''}</span>
        <span className="spacer" />
        <Select ariaLabel="Sort" value={sort} onChange={v => { setSort(v); localStorage.setItem('pab-series-sort', v) }} options={[{ value: 'name', label: 'Name' }, { value: 'added', label: 'Recently added' }, { value: 'books', label: 'Most books' }, { value: 'length', label: 'Longest' }]} />
      </div>
      {err && <div className="callout danger"><Icon name="alert" size={15} />{err.msg}</div>}
      {total === 0 ? <Empty title="No series" icon="layers">Books with series metadata show up here.</Empty> : <div className="grid-covers">{items.map(s => <SeriesTile key={s.id} s={s} />)}</div>}
      <div ref={more} className="more-sentinel">{busy && <Loading label="" />}</div>
    </>
  )
}

function Authors({ lib }: { lib: string }) {
  const { settings } = useApp()
  const { data, notConnected, error, reload } = useApi<{ items: AuthorCard[] }>(`${V1}/authors?library=${encodeURIComponent(lib)}`)
  const [matching, setMatching] = useState(false)
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
        {settings?.backend === 'native' && <button className="btn" onClick={() => setMatching(true)}><Icon name="wand" size={15} />Match all authors</button>}
        <span className="spacer" />
        <span className="muted small">{data.items.length.toLocaleString()} authors</span>
      </div>
      {shown.length === 0 ? <Empty title="No authors" icon="feather" /> : <div className="grid-people">{shown.map(a => <AuthorTile key={a.id} a={a} />)}</div>}
      {matching && <AuthorMatchAllDialog library={lib} onClose={() => setMatching(false)} onDone={() => void reload()} />}
    </>
  )
}
