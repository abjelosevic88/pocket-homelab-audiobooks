// ⌘K / "/" — search every library (books, authors, series, narrators), or run a command.
import React, { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { usePlayer } from '../lib/player'
import { hours } from '../lib/books'
import type { AuthorCard, Book, SeriesCard } from '../lib/types'
import { Icon } from './icons'
import { Img } from './cards'
import { useDebounce } from './ui'

interface Results { books: Book[]; authors: AuthorCard[]; series: SeriesCard[]; narrators: { name: string; books: number; library_id: string }[] }

export function CommandPalette({ onClose, onShortcuts }: { onClose: () => void; onShortcuts: () => void }) {
  const navigate = useNavigate()
  const p = usePlayer()
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 160)
  const [hi, setHi] = useState(0)
  const [res, setRes] = useState<Results | null>(null)
  const list = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!dq.trim()) { setRes(null); return }
    let live = true
    api.get<Results>(`${V1}/search?q=${encodeURIComponent(dq)}`).then(r => { if (live) setRes(r) }).catch(() => {})
    return () => { live = false }
  }, [dq])

  type Row = { key: string; group: string; icon: React.ReactNode; label: React.ReactNode; sub?: React.ReactNode; hint?: string; run: () => void; alt?: () => void }
  const commands: Row[] = useMemo(() => [
    ...(p.book ? [{ key: 'toggle', group: 'Commands', icon: <Icon name={p.playing ? 'pause' : 'play'} size={16} />, label: p.playing ? `Pause “${p.book.title}”` : `Continue “${p.book.title}”`, hint: '␣', run: () => p.toggle() }] : []),
    ...(p.book ? [{ key: 'bookmark', group: 'Commands', icon: <Icon name="bookmark-plus" size={16} />, label: 'Bookmark this moment', hint: 'B', run: () => p.bookmark() }] : []),
    { key: 'random', group: 'Commands', icon: <Icon name="shuffle" size={16} />, label: 'Something I haven’t started', run: () => navigate('/library?show=not-started&sort=random') },
    { key: 'home', group: 'Go to', icon: <Icon name="home" size={16} />, label: 'Home', hint: 'H', run: () => navigate('/') },
    { key: 'books', group: 'Go to', icon: <Icon name="book" size={16} />, label: 'Books', hint: 'L', run: () => navigate('/library') },
    { key: 'series', group: 'Go to', icon: <Icon name="layers" size={16} />, label: 'Series', hint: 'S', run: () => navigate('/library/series') },
    { key: 'authors', group: 'Go to', icon: <Icon name="feather" size={16} />, label: 'Authors', hint: 'A', run: () => navigate('/library/authors') },
    { key: 'inprog', group: 'Go to', icon: <Icon name="headphones" size={16} />, label: 'In progress', run: () => navigate('/library?show=in-progress&sort=progress') },
    { key: 'finished', group: 'Go to', icon: <Icon name="check-circle" size={16} />, label: 'Finished', run: () => navigate('/library?show=finished') },
    { key: 'upnext', group: 'Go to', icon: <Icon name="list-ordered" size={16} />, label: 'Up next', hint: 'N', run: () => navigate('/up-next') },
    { key: 'history', group: 'Go to', icon: <Icon name="history" size={16} />, label: 'History', hint: 'Y', run: () => navigate('/history') },
    { key: 'stats', group: 'Go to', icon: <Icon name="bar" size={16} />, label: 'Stats', hint: 'T', run: () => navigate('/stats') },
    { key: 'settings', group: 'Go to', icon: <Icon name="settings" size={16} />, label: 'Settings', hint: ',', run: () => navigate('/settings') },
    { key: 'abs', group: 'Go to', icon: <Icon name="server" size={16} />, label: 'Settings: Audiobookshelf account', run: () => navigate('/settings/audiobookshelf') },
    { key: 'keys', group: 'Go to', icon: <Icon name="keyboard" size={16} />, label: 'Keyboard shortcuts', hint: '?', run: onShortcuts },
  ], [navigate, onShortcuts, p])

  const rows = useMemo<Row[]>(() => {
    const n = q.trim().toLowerCase()
    const words = n.split(/\s+/).filter(Boolean)
    const hit = (text: string) => words.every(w => text.toLowerCase().includes(w))
    const cmds = commands.filter(c => !n || hit(`${c.label} ${c.group}`))
    if (!n) return cmds
    const out: Row[] = []
    if (res) {
      out.push(...res.books.map<Row>(b => ({ key: 'b' + b.id, group: 'Books', icon: <Img src={b.cover} w={64} alt={b.title} className="pal-art" />, label: b.title,
        sub: [b.author, b.series, hours(b.duration)].filter(Boolean).join(' · '), hint: '↵', run: () => navigate(`/book/${b.id}`), alt: () => p.play(b) })))
      out.push(...res.series.map<Row>(s => ({ key: 's' + s.id, group: 'Series', icon: <Img src={s.covers[0]} w={64} alt={s.name} className="pal-art" icon="layers" />, label: s.name, sub: `${s.books} ${s.books === 1 ? 'book' : 'books'}`, run: () => navigate(`/series/${s.id}?library=${s.library_id}`) })))
      out.push(...res.authors.map<Row>(a => ({ key: 'a' + a.id, group: 'Authors', icon: <Img src={a.image} w={64} alt={a.name} className="pal-art" round />, label: a.name, sub: a.books ? `${a.books} books` : 'Author', run: () => navigate(`/author/${a.id}`) })))
      out.push(...res.narrators.map<Row>(x => ({ key: 'n' + x.name + x.library_id, group: 'Narrators', icon: <Icon name="mic" size={16} />, label: x.name, sub: `${x.books} ${x.books === 1 ? 'book' : 'books'}`, run: () => navigate(`/library?library=${x.library_id}&narrator=${encodeURIComponent(x.name)}`) })))
    }
    out.push(...cmds.slice(0, 5))
    return out
  }, [q, res, commands, navigate, p])
  useEffect(() => setHi(0), [q, res])
  useEffect(() => { list.current?.querySelector(`[data-i="${hi}"]`)?.scrollIntoView({ block: 'nearest' }) }, [hi])

  const run = (r: Row | undefined, alt = false) => { if (!r) return; onClose(); (alt && r.alt ? r.alt : r.run)() }
  let last = ''
  return createPortal(
    <div className="palette-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette-box" role="dialog" aria-label="Search and commands">
        <div className="pal-input">
          <Icon name="search" size={18} />
          <input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Books, authors, series, narrators, or a command…" aria-label="Search"
            onKeyDown={e => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => Math.min(rows.length - 1, h + 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => Math.max(0, h - 1)) }
              else if (e.key === 'Enter') { e.preventDefault(); run(rows[hi], e.shiftKey) }
              else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose() }
            }} />
          <kbd>esc</kbd>
        </div>
        <div className="pal-list" ref={list} role="listbox">
          {rows.length === 0 && <div className="pal-empty">{q.trim() && !res ? 'Searching…' : 'Nothing matches.'}</div>}
          {rows.map((r, i) => {
            const head = r.group !== last ? <div className="pal-group" key={'g' + r.group}>{r.group}</div> : null
            last = r.group
            return (
              <React.Fragment key={r.key}>
                {head}
                <div data-i={i} role="option" aria-selected={i === hi} className={`pal-row ${i === hi ? 'hi' : ''}`} onMouseEnter={() => setHi(i)} onClick={() => run(r)}>
                  <span className="pal-ico">{r.icon}</span>
                  <span className="grow"><span className="pal-label ellipsis">{r.label}</span>{r.sub && <span className="pal-sub ellipsis">{r.sub}</span>}</span>
                  {r.hint && <kbd>{r.hint}</kbd>}
                  {i === hi && <Icon name="chevron-right" size={14} className="muted" />}
                </div>
              </React.Fragment>
            )
          })}
        </div>
        {q.trim() && <div className="pal-foot muted xsmall"><kbd>↵</kbd> open · <kbd>⇧</kbd><kbd>↵</kbd> play a book now</div>}
      </div>
    </div>, document.body)
}
