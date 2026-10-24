// One book: cover, what it is, your place in it, chapters, bookmarks, the rest of its series.
import { useEffect, useState } from 'react'
import {TrashDialog} from '../components/TrashDialog'
import {TrackEditor} from '../components/TrackEditor'
import { ShareDialog } from '../components/ShareDialog'
import { AddToList } from '../components/AddToList'
import { BookEditor } from '../components/BookEditor'
import { Link, useParams, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { usePlayer } from '../lib/player'
import { useApi } from '../lib/use'
import type { BookPage as BookPageT, Bookmark } from '../lib/types'
import { clock, cover, hours, left } from '../lib/books'
import { fmtBytes, fmtDate, fmtDateTime } from '../lib/format'
import { Icon } from '../components/icons'
import { BookRow, Img, NotConnected, Progress } from '../components/cards'
import { useCrumb } from '../components/Layout'
import { Confirm, Loading, MenuButton, type MenuItem, errMsg, useToast } from '../components/ui'

export default function BookPage() {
  const { id = '' } = useParams()
  const { toast } = useToast()
  const { settings } = useApp()
  const p = usePlayer()
  const { data, setData, error, notConnected, reload } = useApi<BookPageT>(`${V1}/books/${encodeURIComponent(id)}`)
  const [more, setMore] = useState(false)
  const [sharing,setSharing] = useState(false)
  const [trashing,setTrashing]=useState(false)
  const navigate=useNavigate()
  const [editing, setEditing] = useState(false)
  const [tracks,setTracks]=useState(false)
  const [organizing, setOrganizing] = useState(false)
  const [confirm, setConfirm] = useState<'' | 'reset' | 'finished'>('')
  useCrumb(data?.book.title)
  useEffect(() => {
    const h = (e: Event) => { const d = (e as CustomEvent<{ id: string }>).detail; if (d.id === id) reload() }
    window.addEventListener('pab:bookmarks', h)
    const pr = (e: Event) => { const d = (e as CustomEvent<{ id: string; ended: boolean }>).detail; if (d.id === id && d.ended) reload() }
    window.addEventListener('pab:progress', pr)
    return () => { window.removeEventListener('pab:bookmarks', h); window.removeEventListener('pab:progress', pr) }
  }, [id, reload])
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!data) return <Loading />
  const b = data.book
  const here = p.isCurrent(b.id)
  const t = here ? p.time : b.current_time
  const playingThis = here && p.playing
  const started = t > 0 && !b.finished
  const prog = b.duration ? t / b.duration : 0
  const chIdx = here ? p.chapterIndex : (b.chapter_list || []).findIndex((c, i, a) => c.start <= t && (i === a.length - 1 || a[i + 1].start > t))

  const progress = async (action: string, extra: Record<string, unknown> = {}) => {
    if (here && action === 'reset') p.seek(0)
    if (here && action === 'finished') p.stop()
    try { await api.post(`${V1}/books/${encodeURIComponent(b.id)}/progress`, { action, ...extra }); await reload(); toast(action === 'finished' ? 'Marked finished' : action === 'unfinished' ? 'Marked not finished' : action === 'reset' ? 'Back to the start' : action === 'hide' ? 'Taken off Continue listening' : 'Saved') } catch (e) { toast(errMsg(e), 'err') }
  }
  const upNext = async () => {
    try {
      if (data.up_next) await api.del(`${V1}/up-next/${encodeURIComponent(b.id)}`)
      else await api.post(`${V1}/up-next`, { item_id: b.id })
      setData({ ...data, up_next: !data.up_next })
      toast(data.up_next ? 'Taken off Up next' : 'Added to Up next')
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const delMark = async (m: Bookmark) => {
    try { await api.del(`${V1}/books/${encodeURIComponent(b.id)}/bookmarks?time=${m.time}`); setData({ ...data, bookmarks: data.bookmarks.filter(x => x.time !== m.time) }) } catch (e) { toast(errMsg(e), 'err') }
  }
  const playAt = (sec: number) => p.play(b, { at: sec })
  const menu = (): MenuItem[] => [
    { label: data.up_next ? 'Take off Up next' : 'Add to Up next', icon: 'list-ordered', onSelect: upNext },
    b.finished ? { label: 'Mark as not finished', icon: 'undo', onSelect: () => progress('unfinished') } : { label: 'Mark as finished', icon: 'check-circle', onSelect: () => setConfirm('finished') },
    ...(started ? [{ label: 'Take off Continue listening', icon: 'eye-off', onSelect: () => progress('hide') }] : []),
    ...(t > 0 || b.finished ? [{ label: 'Start over', icon: 'rotate-ccw', onSelect: () => setConfirm('reset') }] : []),
    ...(data.backend === 'native' ? [{ label: 'Add to a list', icon: 'layers', onSelect: () => setOrganizing(true) }] : []),
    ...(data.can_download ? [{ label: 'Download book', icon: 'download', onSelect: () => { window.location.href = `${V1}/catalog/items/${encodeURIComponent(b.id)}/download` } }] : []),
    ...(data.backend==='native' && data.can_download ? [{label:'Export metadata',icon:'download',onSelect:()=>{window.location.href=`${V1}/catalog/items/${encodeURIComponent(b.id)}/metadata`}}] : []),
    ...(data.can_share ? [{ label: 'Share & RSS', icon: 'external', onSelect: () => setSharing(true) }] : []),
    ...(data.can_delete ? [{ label: 'Move to trash', icon: 'trash', onSelect: () => setTrashing(true) }] : []),
    ...(data.can_edit && (b.files||0)>0 ? [{label:'Manage tracks',icon:'list-ordered',onSelect:()=>setTracks(true)}] : []),
    ...(data.can_edit ? [{ label: 'Edit book', icon: 'edit', onSelect: () => setEditing(true) }] : []),
    ...(data.open_url ? [{ label: 'Open in Audiobookshelf', icon: 'external', onSelect: () => window.open(data.open_url, '_blank', 'noreferrer') }] : []),
  ]
  return (
    <div className="book-page">
      {tracks&&<TrackEditor id={id} onClose={()=>setTracks(false)} onSaved={()=>{if(here)p.stop();setTracks(false);void reload()}}/>}
      {trashing&&<TrashDialog id={id} title={b.title} onClose={()=>setTrashing(false)} onRemoved={()=>{if(here)p.stop();navigate('/trash')}}/>}
      <div className="backdrop blur" style={b.cover ? { backgroundImage: `url(${cover(b.cover, 600)})` } : undefined}><div className="backdrop-shade" /></div>
      <section className="book-hero">
        <div className="book-cover"><Img src={b.cover} w={420} alt={b.title} /></div>
        <div className="book-text">
          {b.series_list && b.series_list.length > 0 && (
            <p className="eyebrow">{b.series_list.map((s, i) => <span key={s.id}>{i > 0 && ' · '}<Link className="eyebrow link" to={`/series/${s.id}?library=${encodeURIComponent(b.library_id)}`}>{s.name}{s.sequence ? ` · Book ${s.sequence}` : ''}</Link></span>)}</p>
          )}
          <h1 className="book-title">{b.title}</h1>
          {b.subtitle && <p className="book-subtitle">{b.subtitle}</p>}
          <p className="book-by">
            {b.authors && b.authors.length ? b.authors.map((a, i) => <span key={a.id}>{i > 0 && ', '}<Link to={`/author/${a.id}`}>{a.name}</Link></span>) : b.author}
            {(b.narrators?.length || b.narrator) && <span className="muted"> · read by {(b.narrators && b.narrators.length ? b.narrators : [b.narrator!]).map((n, i) => <span key={n}>{i > 0 && ', '}<Link to={`/library?library=${encodeURIComponent(b.library_id)}&narrator=${encodeURIComponent(n)}`}>{n}</Link></span>)}</span>}
          </p>
          <p className="book-meta muted">
            <span><Icon name="clock" size={13} />{hours(b.duration)}</span>
            {b.chapters > 0 && <span><Icon name="list-ordered" size={13} />{b.chapters} chapters</span>}
            {b.year && <span>{b.year}</span>}
            {b.abridged && <span className="badge">Abridged</span>}
            {b.explicit && <span className="badge">Explicit</span>}
          </p>
          <div className="book-actions">
            {data.can_read && <Link className="btn primary lg" to={`/read/${encodeURIComponent(b.id)}`}>Read ebook</Link>}
            <button type="button" className="btn primary lg" disabled={b.missing || !b.duration} onClick={() => playingThis ? p.pause() : p.play(b)}>
              <Icon name={playingThis ? 'pause' : 'play'} size={16} />{playingThis ? 'Pause' : started ? `Continue · ${left({ duration: b.duration, current_time: t }, settings?.speed)}` : b.finished ? 'Listen again' : 'Play'}
            </button>
            <button type="button" className={`btn lg ${data.up_next ? 'on' : ''}`} onClick={upNext} title={data.up_next ? 'On your Up next' : 'Add to Up next'}><Icon name={data.up_next ? 'check' : 'list-ordered'} size={16} />{data.up_next ? 'Up next' : 'Listen later'}</button>
            {b.finished ? <span className="badge st-ok lgbadge"><Icon name="check" size={12} strokeWidth={2.6} />Finished{b.finished_at ? ` ${fmtDate(new Date(b.finished_at).toISOString().slice(0, 10))}` : ''}</span> : null}
            <MenuButton label="More" className="btn ghost icon-only lg" items={menu}><Icon name="more" size={18} /></MenuButton>
          </div>
          {started && (
            <div className="book-progress">
              <Progress value={prog} />
              <span className="small muted tnum">{Math.round(prog * 100)}% · at {clock(t)}{chIdx >= 0 && b.chapter_list ? ` · ${b.chapter_list[chIdx].title}` : ''}</span>
            </div>
          )}
          {b.genres.length > 0 && <p className="book-genres">{b.genres.slice(0, 8).map(g => <Link key={g} className="chip" to={`/library?library=${encodeURIComponent(b.library_id)}&genre=${encodeURIComponent(g)}`}>{g}</Link>)}</p>}
        </div>
      </section>

      <div className="book-cols">
        <div className="book-main">
          {b.description && (
            <section className="section">
              <h2 className="section-h">About</h2>
              <div className={`book-desc ${more ? '' : 'clamp'}`}>{b.description.split(/\n{2,}/).map((para, i) => <p key={i}>{para}</p>)}</div>
              {b.description.length > 600 && <button type="button" className="link-btn small" onClick={() => setMore(m => !m)}>{more ? 'Show less' : 'Show more'}</button>}
            </section>
          )}
          {b.chapter_list && b.chapter_list.length > 0 && (
            <section className="section">
              <h2 className="section-h">Chapters <span className="muted small">{b.chapter_list.length}</span></h2>
              <ol className="chapters book">
                {b.chapter_list.map((c, i) => (
                  <li key={c.id} className={i === chIdx && (started || here) ? 'cur' : c.end <= t && !b.finished ? 'past' : ''}>
                    <button type="button" onClick={() => playAt(c.start)} title={`Play from ${clock(c.start)}`}>
                      <span className="ch-n tnum">{i + 1}</span><span className="grow ellipsis">{c.title}</span><span className="mono muted">{clock(c.end - c.start)}</span>
                      <span className="ch-play"><Icon name="play" size={12} /></span>
                    </button>
                  </li>
                ))}
              </ol>
            </section>
          )}
          {data.series.map(s => s.books.length > 1 && (
            <section className="section" key={s.id}>
              <h2 className="section-h"><Link to={`/series/${s.id}?library=${encodeURIComponent(b.library_id)}`}>{s.name}</Link> <span className="muted small">{s.books.length} books</span></h2>
              <ul className="book-list">{s.books.map(x => <BookRow key={x.id} b={x} n={x.sequence || '·'} />)}</ul>
            </section>
          ))}
        </div>
        <aside className="book-side">
          <section className="card side-card">
            <h3>Bookmarks</h3>
            {data.bookmarks.length === 0 ? <p className="muted small">None yet. While listening, press <kbd>b</kbd> to keep a moment.</p> : (
              <ul className="marks">
                {data.bookmarks.map(m => (
                  <li key={m.time}>
                    <button type="button" className="mark-go" onClick={() => playAt(m.time)}><span className="mono">{clock(m.time)}</span><span className="ellipsis">{m.title}</span></button>
                    <button type="button" className="btn ghost icon-only xs" aria-label="Delete bookmark" onClick={() => delMark(m)}><Icon name="x" size={13} /></button>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section className="card side-card">
            <h3>Your listening</h3>
            <dl className="facts">
              <dt>Listened</dt><dd>{data.listened.seconds ? hours(data.listened.seconds) : '—'}{data.listened.sessions ? ` in ${data.listened.sessions} ${data.listened.sessions === 1 ? 'session' : 'sessions'}` : ''}</dd>
              {data.listened.first && <><dt>Started</dt><dd>{fmtDateTime(data.listened.first)}</dd></>}
              {b.last_played ? <><dt>Last played</dt><dd>{fmtDateTime(new Date(b.last_played).toISOString())}</dd></> : null}
            </dl>
            {data.listened.sessions > 0 && <Link to={`/history?book=${encodeURIComponent(b.id)}`} className="small">See every session</Link>}
          </section>
          <section className="card side-card">
            <h3>Details</h3>
            <dl className="facts">
              {b.publisher && <><dt>Publisher</dt><dd>{b.publisher}</dd></>}
              {b.language && <><dt>Language</dt><dd>{b.language}</dd></>}
              {b.format && <><dt>Format</dt><dd>{b.format}{b.files && b.files > 1 ? ` · ${b.files} files` : ''}</dd></>}
              {b.size ? <><dt>Size</dt><dd>{fmtBytes(b.size)}</dd></> : null}
              <dt>Added</dt><dd>{fmtDate(new Date(b.added_at).toISOString().slice(0, 10))}</dd>
            </dl>
          </section>
        </aside>
      </div>
      {sharing && <ShareDialog kind="item" target={id} name={b.title} onClose={()=>setSharing(false)} />}
      {organizing && <AddToList book={b.id} library={b.library_id} collections={!!data.can_edit} onClose={() => setOrganizing(false)} />}
      {editing && <BookEditor id={id} onClose={() => setEditing(false)} onSaved={() => { void reload() }} />}
      {confirm === 'reset' && <Confirm title="Start over?" message="Your place goes back to the beginning, everywhere (the Audiobookshelf apps too). Your history stays." confirmLabel="Start over" onCancel={() => setConfirm('')} onConfirm={() => { setConfirm(''); progress('reset') }} />}
      {confirm === 'finished' && <Confirm title="Mark as finished?" message="It counts as finished in your stats and leaves Continue listening." confirmLabel="Mark finished" danger={false} onCancel={() => setConfirm('')} onConfirm={() => { setConfirm(''); progress('finished') }} />}
    </div>
  )
}
