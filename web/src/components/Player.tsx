import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { usePlayer } from '../lib/player'
import { useApp } from '../lib/app-context'
import { api, V1 } from '../lib/api'
import { clock, hours } from '../lib/books'
import type { Book, Bookmark } from '../lib/types'
import { Img } from './cards'
import { Icon } from './icons'
import { MenuButton, type MenuItem } from './ui'

const RATES = [0.8, 1, 1.1, 1.2, 1.25, 1.3, 1.4, 1.5, 1.75, 2, 2.5, 3]

/** The persistent bar at the bottom and its expanded view. */
export function Player() {
  const p = usePlayer()
  const { settings } = useApp()
  if (p.remoteVisible && p.remote) return <RemoteBar />
  if (!p.book) return null
  const b = p.book
  const dur = p.duration || b.duration || 0
  const pct = dur ? Math.min(100, (p.time / dur) * 100) : 0
  const back = settings?.skip_back || 10, fwd = settings?.skip_forward || 30
  return (
    <>
      <div className="player-bar" role="region" aria-label="Player">
        <div className="pb-progress" onClick={ev => { const r = (ev.currentTarget as HTMLElement).getBoundingClientRect(); p.seek(((ev.clientX - r.left) / r.width) * dur) }}>
          <span className="fill" style={{ width: `${pct}%` }} />
          {p.chapters.length > 1 && dur > 0 && p.chapters.slice(1).map(c => <span key={c.id} className="tick" style={{ left: `${(c.start / dur) * 100}%` }} />)}
        </div>
        <button type="button" className="pb-art" onClick={() => p.setExpanded(true)} aria-label="Open the player"><Img src={b.cover} w={96} alt={b.title} className="xs" /></button>
        <div className="pb-info" onClick={() => p.setExpanded(true)}>
          <div className="t">{b.title}</div>
          <div className="s muted">{p.chapter ? p.chapter.title : b.author} · {clock(p.time)} / {clock(dur)}{p.rate !== 1 ? ` · ${p.rate}×` : ''}{p.sleepAt ? ' · ☾' : ''}</div>
        </div>
        <div className="pb-controls">
          <button type="button" className="btn ghost icon-only hide-sm" onClick={() => p.chapterStep(-1)} title="Previous chapter (⇧←)" aria-label="Previous chapter"><Icon name="skip-back" size={14} /></button>
          <button type="button" className="btn ghost icon-only hide-sm pb-skip" onClick={() => p.skip(-back)} title={`Back ${back} s (←)`} aria-label={`Back ${back} seconds`}><Icon name="rotate-ccw" size={18} /></button>
          <button type="button" className="btn primary round pb-play" onClick={p.toggle} title={p.playing ? 'Pause (space)' : 'Play (space)'} aria-label={p.playing ? 'Pause' : 'Play'}>{p.loading && p.playing ? <span className="spinner sm on-accent" /> : <Icon name={p.playing ? 'pause' : 'play'} size={18} />}</button>
          <button type="button" className="btn ghost icon-only pb-skip" onClick={() => p.skip(fwd)} title={`Forward ${fwd} s (→)`} aria-label={`Forward ${fwd} seconds`}><Icon name="rotate-cw" size={18} /></button>
          <button type="button" className="btn ghost icon-only hide-sm" onClick={() => p.chapterStep(1)} title="Next chapter (⇧→)" aria-label="Next chapter"><Icon name="skip-forward" size={14} /></button>
          <button type="button" className="btn ghost icon-only hide-sm" onClick={() => p.setExpanded(true)} title="Open the player (p)" aria-label="Open the player"><Icon name="chevron-up" size={18} /></button>
        </div>
      </div>
      {p.expanded && <Expanded />}
    </>
  )
}

function Expanded() {
  const p = usePlayer()
  const { settings } = useApp()
  const navigate = useNavigate()
  const [tab, setTab] = useState<'now' | 'chapters' | 'bookmarks'>('now')
  const b = p.book!
  const dur = p.duration || b.duration || 0
  const back = settings?.skip_back || 10, fwd = settings?.skip_forward || 30
  useEffect(() => {
    const h = (ev: KeyboardEvent) => { if (ev.key === 'Escape') p.setExpanded(false) }
    window.addEventListener('keydown', h)
    document.body.style.overflow = 'hidden'
    return () => { window.removeEventListener('keydown', h); document.body.style.overflow = '' }
  }, [p])
  const [, tick] = useState(0)
  useEffect(() => { if (!p.sleepAt) return; const t = setInterval(() => tick(x => x + 1), 15000); return () => clearInterval(t) }, [p.sleepAt])
  const sleepLeft = p.sleepAt ? Math.max(0, Math.round((p.sleepAt - Date.now()) / 60000)) : 0
  const ch = p.chapter
  const chPct = ch && ch.end > ch.start ? Math.min(100, ((p.time - ch.start) / (ch.end - ch.start)) * 100) : 0
  const open = (to: string) => { p.setExpanded(false); navigate(to) }
  return (
    <div className="player-full" role="dialog" aria-label="Player">
      <div className="pf-head">
        <button type="button" className="btn ghost icon-only" onClick={() => p.setExpanded(false)} aria-label="Close the player" title="Close (esc)"><Icon name="chevron-down" size={20} /></button>
        <div className="tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === 'now'} className={tab === 'now' ? 'active' : ''} onClick={() => setTab('now')}>Now playing</button>
          {p.chapters.length > 0 && <button type="button" role="tab" aria-selected={tab === 'chapters'} className={tab === 'chapters' ? 'active' : ''} onClick={() => setTab('chapters')}>Chapters</button>}
          {<button type="button" role="tab" aria-selected={tab === 'bookmarks'} className={tab === 'bookmarks' ? 'active' : ''} onClick={() => setTab('bookmarks')}>Bookmarks</button>}
        </div>
        <MenuButton label="More" align="right" width={230} items={(): MenuItem[] => [
          { label: 'Open the book', icon: 'book-open', onSelect: () => open(`/book/${b.id}`) },
          { label: 'Add a bookmark here', icon: 'bookmark-plus', hint: 'B', onSelect: () => p.bookmark() },
          'sep',
          { label: 'Stop and close', icon: 'x', onSelect: () => { p.stop(); p.setExpanded(false) } },
        ]}><Icon name="more" size={18} /></MenuButton>
      </div>
      <div className="pf-body">
        {tab === 'now' && (
          <div className="pf-now">
            <div className="pf-cover"><Img src={b.cover} w={520} alt={b.title} /></div>
            <div className="pf-title"><Link to={`/book/${b.id}`} onClick={() => p.setExpanded(false)}>{b.title}</Link></div>
            <div className="muted">{b.authors && b.authors.length ? b.authors.map((a, i) => <span key={a.id}>{i > 0 && ', '}<Link to={`/author/${a.id}`} onClick={() => p.setExpanded(false)}>{a.name}</Link></span>) : b.author}{b.narrator ? <> · read by {b.narrator}</> : null}</div>
            {ch && <div className="pf-chapter">
              <button type="button" className="link-btn" onClick={() => setTab('chapters')}>{ch.title}</button>
              <span className="muted xsmall tnum">{clock(Math.max(0, ch.end - p.time) / (p.rate || 1))} left in the chapter</span>
              <span className="progress thin"><span style={{ width: `${chPct}%` }} /></span>
            </div>}
          </div>
        )}
        {tab === 'chapters' && (
          <ol className="chapters">
            {p.chapters.map((c, i) => (
              <li key={c.id} className={i === p.chapterIndex ? 'cur' : p.time >= c.end ? 'past' : ''}>
                <button type="button" onClick={() => p.seek(c.start)}>
                  <span className="ch-n tnum">{i + 1}</span><span className="grow ellipsis">{c.title}</span><span className="mono muted">{clock(c.end - c.start)}</span>
                </button>
              </li>
            ))}
          </ol>
        )}
        {tab === 'bookmarks' && <BookmarkList book={b} />}
      </div>
      <div className="pf-controls">
        <div className="pf-seek">
          <input type="range" min={0} max={dur || 1} step={1} value={Math.min(p.time, dur || 1)} onChange={ev => p.seek(Number(ev.target.value))} aria-label="Position" />
          <div className="row small muted"><span className="tnum">{clock(p.time)}</span><span className="spacer" /><span className="tnum">{Math.round(dur ? (p.time / dur) * 100 : 0)}% · {hours((dur - p.time) / (p.rate || 1))} left</span><span className="spacer" /><span className="tnum">-{clock(Math.max(0, dur - p.time))}</span></div>
        </div>
        <div className="pf-buttons">
          <button type="button" className="btn ghost icon-only lg" onClick={() => p.chapterStep(-1)} title="Previous chapter" aria-label="Previous chapter"><Icon name="skip-back" size={16} /></button>
          <button type="button" className="btn ghost lg pf-skip" onClick={() => p.skip(-back)} title={`Back ${back} s`} aria-label={`Back ${back} seconds`}><Icon name="rotate-ccw" size={22} /><small>{back}</small></button>
          <button type="button" className="btn primary round xl" onClick={p.toggle} aria-label={p.playing ? 'Pause' : 'Play'}>{p.loading && p.playing ? <span className="spinner on-accent" /> : <Icon name={p.playing ? 'pause' : 'play'} size={26} />}</button>
          <button type="button" className="btn ghost lg pf-skip" onClick={() => p.skip(fwd)} title={`Forward ${fwd} s`} aria-label={`Forward ${fwd} seconds`}><Icon name="rotate-cw" size={22} /><small>{fwd}</small></button>
          <button type="button" className="btn ghost icon-only lg" onClick={() => p.chapterStep(1)} title="Next chapter" aria-label="Next chapter"><Icon name="skip-forward" size={16} /></button>
        </div>
        <div className="pf-extra">
          <MenuButton label="Speed" className={`btn sm ${p.rate !== 1 ? 'on' : ''}`} width={160} items={() => [
            { heading: 'Speed' },
            ...RATES.map(r => ({ label: `${r}×`, checked: Math.abs(p.rate - r) < 0.01, onSelect: () => p.setRate(r) })),
          ] as MenuItem[]}><Icon name="gauge" size={14} />{p.rate}×</MenuButton>
          {<button type="button" className="btn sm" onClick={p.bookmark} title="Bookmark this moment (b)"><Icon name="bookmark-plus" size={14} />Bookmark</button>}
          <MenuButton label="Sleep timer" className={`btn sm ${p.sleepAt ? 'on' : ''}`} align="right" width={200} items={() => [
            { heading: 'Pause after' },
            ...[5, 10, 15, 30, 45, 60, 90].map(m => ({ label: `${m} minutes`, onSelect: () => p.setSleep(m) })),
            ...(p.chapters.length ? [{ label: 'End of the chapter', onSelect: () => p.setSleep('chapter') }] : []),
            ...(p.sleepAt ? ['sep' as const, { label: 'Off', icon: 'x', onSelect: () => p.setSleep(null) }] : []),
          ] as MenuItem[]}><Icon name="moon" size={14} />{p.sleepAt ? (p.sleepChapter ? 'End of chapter' : `${sleepLeft} min`) : 'Sleep'}</MenuButton>
        </div>
      </div>
    </div>
  )
}

/** The book's bookmarks (kept in Audiobookshelf, so the phone apps have them too). */
function BookmarkList({ book }: { book: Book }) {
  const p = usePlayer()
  const [marks, setMarks] = useState<Bookmark[] | null>(null)
  const load = () => api.get<{ bookmarks: Bookmark[] }>(`${V1}/books/${encodeURIComponent(book.id)}`).then(r => setMarks(r.bookmarks)).catch(() => setMarks([]))
  useEffect(() => {
    load()
    const h = () => load()
    window.addEventListener('pab:bookmarks', h)
    return () => window.removeEventListener('pab:bookmarks', h)
  }, [book.id]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!marks) return <div className="loading"><span className="spinner" /></div>
  if (!marks.length) return <div className="empty"><p className="muted">No bookmarks yet. Press <kbd>b</kbd> or “Bookmark” to keep a moment.</p></div>
  return (
    <ul className="chapters">
      {marks.map(m => (
        <li key={m.time}>
          <button type="button" onClick={() => p.seek(m.time)}><Icon name="bookmark" size={14} className="muted" /><span className="grow ellipsis">{m.title}</span><span className="mono muted">{clock(m.time)}</span></button>
        </li>
      ))}
    </ul>
  )
}

/** Another device is playing (the phone, another browser): its book with a
 *  clock that keeps ticking between reports, and "Continue here". */
function RemoteBar() {
  const p = usePlayer()
  const r = p.remote!
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!r.playing) { setNow(Date.now()); return }
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [r.playing, r.updated_at])
  const dur = r.duration || 0
  const elapsed = r.playing ? Math.max(0, (now + p.clockOffset - (r.updated_at || now)) / 1000) : 0
  const pos = Math.min(dur || Infinity, (r.position || 0) + elapsed)
  const pct = dur ? Math.min(100, (pos / dur) * 100) : 0
  const ago = Math.round((now + p.clockOffset - (r.updated_at || now)) / 60000)
  const here = async () => {
    try {
      const { book } = await api.get<{ book: Book }>(`${V1}/books/${encodeURIComponent(r.item_id!)}`)
      p.play(book, { at: Math.floor(pos) })
    } catch { /* the book page will say why */ }
  }
  return (
    <div className="player-bar remote" role="region" aria-label={`Playing on ${r.device}`}>
      <div className="pb-progress remote"><span className="fill" style={{ width: `${pct}%` }} /></div>
      <Link to={`/book/${r.item_id}`} className="pb-art"><Img src={r.cover} w={96} alt={r.title || ''} className="xs" /></Link>
      <Link to={`/book/${r.item_id}`} className="pb-info">
        <div className="t">{r.title}</div>
        <div className="s">
          <span className={`remote-dot ${r.playing ? 'live' : ''}`} aria-hidden />
          <span className="remote-where">{r.playing ? 'Playing' : 'Paused'} on {r.device}</span>
          <span className="muted"> · {clock(pos)} / {clock(dur)}{!r.playing && ago >= 1 ? ` · ${ago} min ago` : ''}</span>
        </div>
      </Link>
      <div className="pb-controls">
        <button type="button" className="btn sm primary remote-here" onClick={here} title={`Continue in this browser from ${clock(pos)}`}><Icon name="play" size={13} />Continue here</button>
      </div>
    </div>
  )
}
