import { useEffect } from 'react'
import { Link } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { usePlayer } from '../lib/player'
import { useApi } from '../lib/use'
import type { Home as HomeT } from '../lib/types'
import { V1, cover, hours, left } from '../lib/books'
import { Icon } from '../components/icons'
import { AuthorTile, BookCard, ContinueCard, Img, NotConnected, Progress, Shelf } from '../components/cards'
import { Loading } from '../components/ui'

function greeting() {
  const h = new Date().getHours()
  return h < 5 ? 'Good night' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

export default function Home() {
  const { user } = useApp()
  const p = usePlayer()
  const { data: h, error, notConnected, reload } = useApi<HomeT>(`${V1}/home`)
  // progress moves while listening: refresh the shelves when a book is paused or finished
  useEffect(() => {
    const t = { id: 0 as number | undefined }
    const r = () => { clearTimeout(t.id); t.id = window.setTimeout(reload, 1500) }
    window.addEventListener('pab:progress', r)
    return () => { window.removeEventListener('pab:progress', r); clearTimeout(t.id) }
  }, [reload])
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!h) return <Loading />
  const first = (user?.name || '').split(' ')[0]
  const feature = h.continue[0]
  const goal = h.today.goal_minutes
  const todayMin = Math.round(h.today.seconds / 60)
  const empty = !h.continue.length && !h.recent.length && !h.discover.length
  return (
    <div className="home">
      <header className="home-hero">
        <div className="grow">
          <h1>{greeting()}{first ? `, ${first}` : ''}</h1>
          <p className="muted">
            {h.today.seconds > 60 ? <>{hours(h.today.seconds)} listened today{goal ? ` of your ${goal} min` : ''}</> : feature ? <>Pick up “{feature.title}” where you left off</> : <>Pick something from your shelves</>}
            {h.today.streak > 1 && <> · <span className="streak"><Icon name="zap" size={13} />{h.today.streak}-day streak</span></>}
          </p>
          {goal > 0 && <span className="goal-bar" title={`${todayMin} of ${goal} minutes today`}><span style={{ width: `${Math.min(100, (todayMin / goal) * 100)}%` }} /></span>}
        </div>
        <Link to="/library?show=not-started&sort=random" className="btn"><Icon name="shuffle" size={15} />Something new</Link>
      </header>

      {feature && <Feature b={feature} />}
      {h.continue.length > 1 && (
        <Shelf title="Continue listening" wide>{h.continue.slice(1).map(b => <ContinueCard key={b.id} b={b} />)}</Shelf>
      )}
      {h.up_next.length > 0 && <Shelf title="Up next" sub="In your order" to="/up-next">{h.up_next.map(b => <BookCard key={b.id} b={b} />)}</Shelf>}
      {h.series_next.length > 0 && <Shelf title="Next in your series" sub="The next book of series you started">{h.series_next.map(b => <BookCard key={b.id} b={b} sub={b.series || b.author} />)}</Shelf>}
      {h.recent.length > 0 && <Shelf title="Recently added" to="/library?sort=added">{h.recent.map(b => <BookCard key={b.id} b={b} />)}</Shelf>}
      {h.discover.length > 0 && <Shelf title="From your shelves" sub="Books you haven't started" to="/library?show=not-started&sort=random">{h.discover.map(b => <BookCard key={b.id} b={b} />)}</Shelf>}
      {h.listen_again.length > 0 && <Shelf title="Listen again" to="/library?show=finished">{h.listen_again.map(b => <BookCard key={b.id} b={b} sub={b.author} />)}</Shelf>}
      {h.authors.length > 0 && <Shelf title="Newest authors" to="/library/authors">{h.authors.map(a => <AuthorTile key={a.id} a={a} />)}</Shelf>}
      {empty && <div className="empty big"><span className="empty-ico"><Icon name="book" size={22} /></span><h3>No books yet</h3><p className="muted">Add a library in Settings, then scan your folders or upload a book.</p></div>}
      {p.remote && !p.remoteVisible && p.remote.playing && <p className="muted small">Also playing on {p.remote.device}: {p.remote.title}</p>}
    </div>
  )
}

/** The big card: the book you were in the middle of. */
function Feature({ b }: { b: HomeT['continue'][number] }) {
  const p = usePlayer()
  const { settings } = useApp()
  const here = p.isCurrent(b.id)
  const t = here ? p.time : b.current_time
  const playingThis = here && p.playing
  const ch = here ? p.chapter : null
  return (
    <section className="feature" style={b.cover ? { backgroundImage: `url(${cover(b.cover, 900)})` } : undefined}>
      <div className="feature-shade" />
      <div className="feature-body">
        <Link to={`/book/${b.id}`} className="feature-cover"><Img src={b.cover} w={320} alt={b.title} /></Link>
        <div className="feature-text">
          <p className="eyebrow">{playingThis ? 'Playing now' : 'Pick up where you left off'}</p>
          <h2 className="feature-title"><Link to={`/book/${b.id}`}>{b.title}</Link></h2>
          <p className="feature-meta">{b.author}{b.series ? ` · ${b.series}` : ''}{ch ? ` · ${ch.title}` : ''}</p>
          <div className="row wrap">
            <button type="button" className="btn primary lg" onClick={() => playingThis ? p.pause() : p.play(b)}><Icon name={playingThis ? 'pause' : 'play'} size={16} />{playingThis ? 'Pause' : `Continue · ${left({ duration: b.duration, current_time: t }, settings?.speed)}`}</button>
            <Link to={`/book/${b.id}`} className="btn glass lg"><Icon name="info" size={16} />Details</Link>
          </div>
          <span className="feature-progress"><Progress value={b.duration ? t / b.duration : b.progress} /><span className="tnum">{Math.round((b.duration ? t / b.duration : b.progress) * 100)}%</span></span>
        </div>
      </div>
    </section>
  )
}
