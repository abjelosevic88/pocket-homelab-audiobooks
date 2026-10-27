// Left column: continue button, library, yours — the Pocket Calendar sidebar.
import { type ReactNode, useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { usePlayer } from '../lib/player'
import { Icon } from './icons'
import { Img } from './cards'

type NavItem = { to: string; label: string; icon: string; end?: boolean }

const LIBRARY: NavItem[] = [
  { to: '/library', label: 'Books', icon: 'book', end: true },
  { to: '/library/series', label: 'Series', icon: 'layers' },
  { to: '/library/authors', label: 'Authors', icon: 'feather' },
]
const YOURS: NavItem[] = [
  { to: '/up-next', label: 'Up next', icon: 'list-ordered' },
  { to: '/history', label: 'History', icon: 'history' },
  { to: '/stats', label: 'Stats', icon: 'bar' },
]

export function Sidebar({ onNavigate }: { onNavigate: () => void }) {
  const { status, settings, user } = useApp()
  const p = usePlayer()
  const navigate = useNavigate()
  const title = settings?.title || status?.title || 'Pocket Audiobooks'
  return (
    <aside className="sidebar" aria-label="Audiobooks">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Audiobooks' ? title : <>Pocket <b>Audiobooks</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      {p.book ? (
        <button type="button" className="sb-now" onClick={() => { p.toggle() }} title={p.playing ? 'Pause' : 'Continue listening'}>
          <Img src={p.book.cover} w={80} alt={p.book.title} className="sb-now-art" />
          <span className="grow"><b className="ellipsis">{p.book.title}</b><span className="ellipsis">{p.playing ? (p.chapter?.title || 'Playing') : 'Continue listening'}</span></span>
          <span className="sb-now-btn"><Icon name={p.playing ? 'pause' : 'play'} size={14} /></span>
        </button>
      ) : (
        <button type="button" className="btn primary create-btn" onClick={() => { onNavigate(); navigate('/library?show=not-started&sort=random') }}>
          <Icon name="headphones" size={17} />Find something to hear
        </button>
      )}

      <nav className="sb-nav" aria-label="Sections">
        <NavLink to="/" end onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}><Icon name="home" size={16} />Home</NavLink>
      </nav>
      <Group title="Library"><Nav items={settings?.backend === 'native' ? [...LIBRARY, { to: '/lists', label: 'Collections & playlists', icon: 'layers' }, { to: '/trash', label: 'Trash', icon: 'trash' }] : LIBRARY} onNavigate={onNavigate} /></Group>
      <Group title="Yours"><Nav items={settings?.backend==='native' && user?.role==='admin' ? [...YOURS,{to:'/shares',label:'Shares & RSS',icon:'external'}] : YOURS} onNavigate={onNavigate} /></Group>

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('pab:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}

function Nav({ items, onNavigate }: { items: NavItem[]; onNavigate: () => void }) {
  return (
    <nav className="sb-nav tight">
      {items.map(n => (
        <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
          <Icon name={n.icon} size={16} />{n.label}
        </NavLink>
      ))}
    </nav>
  )
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  const key = `pab-sb-${title}`
  const [open, setOpen] = useState(() => localStorage.getItem(key) !== '0')
  const toggle = () => setOpen(o => { localStorage.setItem(key, o ? '0' : '1'); return !o })
  return (
    <section className="sb-sec">
      <h4 className="sb-h">
        <button type="button" className="sb-h-btn" aria-expanded={open} onClick={toggle}><Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} />{title}</button>
      </h4>
      {open && children}
    </section>
  )
}
