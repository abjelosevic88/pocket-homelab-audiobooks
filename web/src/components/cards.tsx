// Covers, book tiles, shelves, series stacks, authors and the not-connected state.
import React, { useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import type { AuthorCard, Book, SeriesCard } from '../lib/types'
import { cover, hours, hue, left } from '../lib/books'
import { usePlayer } from '../lib/player'
import { useApp } from '../lib/app-context'
import { Icon } from './icons'

/** A cover that fades in, with a coloured placeholder (the title) until it loads or when there is none. */
export function Img({ src, w, alt, className = '', round, icon }: { src?: string; w: number; alt: string; className?: string; round?: boolean; icon?: string }) {
  const [state, setState] = useState<'loading' | 'ok' | 'err'>(src ? 'loading' : 'err')
  const url = cover(src, w)
  const initials = alt.split(/\s+/).filter(Boolean).slice(0, 2).map(x => x[0]).join('').toUpperCase()
  return (
    <span className={`art ${state === 'ok' ? 'loaded' : ''} ${round ? 'round' : ''} ${className}`} style={{ '--h': hue(alt) } as React.CSSProperties}>
      {state !== 'ok' && <span className="art-fallback">{icon ? <Icon name={icon} size={22} /> : <b>{initials}</b>}</span>}
      {url && state !== 'err' && <img src={url} alt="" loading="lazy" decoding="async" onLoad={() => setState('ok')} onError={() => setState('err')} />}
    </span>
  )
}

export function Progress({ value, className = '' }: { value: number; className?: string }) {
  if (!value || value <= 0) return null
  return <span className={`progress ${className}`}><span style={{ width: `${Math.min(100, Math.max(2, value * 100))}%` }} /></span>
}

/** A square cover linking to the book; play on hover, progress along the bottom. */
export function BookCard({ b, sub }: { b: Book; sub?: React.ReactNode }) {
  const p = usePlayer()
  const { settings } = useApp()
  const started = b.current_time > 0 && !b.finished
  const playingThis = p.isCurrent(b.id) && p.playing
  return (
    <Link to={`/book/${b.id}`} className="tile" title={b.title}>
      <span className="tile-art cover">
        <Img src={b.cover} w={240} alt={b.title} />
        {b.finished && <span className="mark done" title="Finished"><Icon name="check" size={12} strokeWidth={3} /></span>}
        {b.sequence && !b.finished && <span className="mark seq" title={`Book ${b.sequence}`}>#{b.sequence}</span>}
        <button type="button" className={`play-fab ${playingThis ? 'on' : ''}`} aria-label={playingThis ? `Pause ${b.title}` : `Play ${b.title}`}
          onClick={e => { e.preventDefault(); if (playingThis) p.pause(); else p.play(b) }}><Icon name={playingThis ? 'pause' : 'play'} size={18} /></button>
        <Progress value={started ? b.progress : 0} className="on-art" />
      </span>
      <span className="tile-title ellipsis">{b.title}</span>
      <span className="tile-sub ellipsis">{sub ?? (started ? left(b, settings?.speed) : b.author)}</span>
    </Link>
  )
}

/** A wide "continue" card: cover, title, progress and a big play. */
export function ContinueCard({ b }: { b: Book }) {
  const p = usePlayer()
  const { settings } = useApp()
  const playingThis = p.isCurrent(b.id) && p.playing
  const t = p.isCurrent(b.id) ? p.time : b.current_time
  const prog = b.duration ? t / b.duration : b.progress
  return (
    <div className="cont-card">
      <Link to={`/book/${b.id}`} className="cont-art"><Img src={b.cover} w={160} alt={b.title} /></Link>
      <div className="cont-body">
        <Link to={`/book/${b.id}`} className="cont-title ellipsis2">{b.title}</Link>
        <span className="muted small ellipsis">{b.author}</span>
        <span className="cont-foot">
          <Progress value={prog} />
          <span className="muted xsmall tnum">{Math.round(prog * 100)}% · {left({ duration: b.duration, current_time: t }, settings?.speed)}</span>
        </span>
      </div>
      <button type="button" className="btn primary round cont-play" aria-label={playingThis ? 'Pause' : `Continue ${b.title}`} onClick={() => playingThis ? p.pause() : p.play(b)}>
        <Icon name={playingThis ? 'pause' : 'play'} size={18} />
      </button>
    </div>
  )
}

export function Shelf({ title, sub, to, children, wide }: { title: React.ReactNode; sub?: React.ReactNode; to?: string; children: React.ReactNode; wide?: boolean }) {
  const row = useRef<HTMLDivElement>(null)
  const scroll = (dir: number) => row.current?.scrollBy({ left: dir * row.current.clientWidth * 0.85, behavior: 'smooth' })
  return (
    <section className="shelf">
      <div className="shelf-head">
        <div className="grow"><h2>{to ? <Link to={to}>{title}</Link> : title}</h2>{sub && <p className="muted small">{sub}</p>}</div>
        <div className="shelf-arrows">
          <button type="button" className="btn ghost icon-only sm" aria-label="Scroll left" onClick={() => scroll(-1)}><Icon name="chevron-left" size={16} /></button>
          <button type="button" className="btn ghost icon-only sm" aria-label="Scroll right" onClick={() => scroll(1)}><Icon name="chevron-right" size={16} /></button>
        </div>
      </div>
      <div className={`shelf-row ${wide ? 'wide' : ''}`} ref={row}>{children}</div>
    </section>
  )
}

/** A series as a little stack of its covers. */
export function SeriesTile({ s }: { s: SeriesCard }) {
  const covers = s.covers.slice(0, 3)
  return (
    <Link to={`/series/${s.id}?library=${encodeURIComponent(s.library_id)}`} className="tile" title={s.name}>
      <span className={`tile-art stack n${Math.max(1, covers.length)}`}>
        {covers.length === 0 && <Img src="" w={200} alt={s.name} icon="layers" />}
        {covers.map((c, i) => <Img key={c + i} src={c} w={200} alt={s.name} className={`st${i}`} />)}
        {s.finished > 0 && s.finished === s.books && <span className="mark done" title="All finished"><Icon name="check" size={12} strokeWidth={3} /></span>}
      </span>
      <span className="tile-title ellipsis">{s.name}</span>
      <span className="tile-sub ellipsis">{s.books} {s.books === 1 ? 'book' : 'books'}{s.finished > 0 ? ` · ${s.finished} finished` : ''}</span>
    </Link>
  )
}

export function AuthorTile({ a }: { a: AuthorCard }) {
  return (
    <Link to={`/author/${a.id}`} className="tile person" title={a.name}>
      <span className="tile-art round"><Img src={a.image} w={200} alt={a.name} round /></span>
      <span className="tile-title ellipsis center">{a.name}</span>
      <span className="tile-sub ellipsis center">{a.books ? `${a.books} ${a.books === 1 ? 'book' : 'books'}` : ''}</span>
    </Link>
  )
}

/** A row in a list of books (series page, up next, author page). */
export function BookRow({ b, n, actions }: { b: Book; n?: React.ReactNode; actions?: React.ReactNode }) {
  const p = usePlayer()
  const playingThis = p.isCurrent(b.id) && p.playing
  return (
    <li className={`book-row ${b.finished ? 'done' : ''}`}>
      {n !== undefined && <span className="br-n tnum">{n}</span>}
      <Link to={`/book/${b.id}`} className="br-art"><Img src={b.cover} w={96} alt={b.title} /></Link>
      <div className="br-body">
        <Link to={`/book/${b.id}`} className="br-title ellipsis">{b.title}</Link>
        <span className="muted small ellipsis">{[b.author, b.narrator ? `read by ${b.narrator}` : ''].filter(Boolean).join(' · ')}</span>
        <span className="br-meta muted xsmall">
          {b.finished ? <span className="badge st-ok">Finished</span> : b.current_time > 0 ? <><Progress value={b.progress} /><span className="tnum">{Math.round(b.progress * 100)}%</span></> : null}
          <span className="tnum">{hours(b.duration)}</span>
        </span>
      </div>
      <div className="br-actions">
        {actions}
        {!b.missing && <button type="button" className="btn ghost icon-only" aria-label={playingThis ? 'Pause' : `Play ${b.title}`} onClick={() => playingThis ? p.pause() : p.play(b)}><Icon name={playingThis ? 'pause' : 'play'} size={16} /></button>}
      </div>
    </li>
  )
}

/** The page's empty-state when Audiobookshelf isn't connected yet. */
export function NotConnected({ message }: { message: string }) {
  return (
    <div className="empty big">
      <span className="empty-ico"><Icon name="headphones" size={22} /></span>
      <h3>Connect your Audiobookshelf</h3>
      <p className="muted" style={{ maxWidth: 460 }}>{message}</p>
      <p className="muted small" style={{ maxWidth: 460 }}>Pocket Audiobooks plays your books with your own Audiobookshelf account, so your place, finished books and bookmarks stay yours — and the Audiobookshelf apps on your phone agree.</p>
      <Link to="/settings/audiobookshelf" className="btn primary"><Icon name="settings" size={15} />Settings → Audiobookshelf</Link>
    </div>
  )
}
