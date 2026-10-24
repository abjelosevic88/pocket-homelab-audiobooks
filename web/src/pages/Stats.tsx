import { useState } from 'react'
import { Link } from 'react-router-dom'
import { V1 } from '../lib/api'
import { useApi } from '../lib/use'
import type { Stats as StatsT } from '../lib/types'
import { hours } from '../lib/books'
import { fmtDate } from '../lib/format'
import { Icon } from '../components/icons'
import { BarList, Columns } from '../components/charts'
import { Img } from '../components/cards'
import { Card, Empty, Loading, PageHeader, Segmented } from '../components/ui'

const h1 = (s: number) => `${(s / 3600).toFixed(s >= 36000 ? 0 : 1)} h`
const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']

export default function Stats() {
  const year = new Date().getFullYear()
  const [range, setRange] = useState('30d')
  const { data: s, error } = useApi<StatsT>(`${V1}/stats?range=${range}`)
  return (
    <div>
      <PageHeader title="Stats" sub="What you listened to, here and in every Audiobookshelf app."
        actions={<Segmented size="sm" value={range} onChange={setRange} options={[{ value: '7d', label: 'Week' }, { value: '30d', label: 'Month' }, { value: '90d', label: '3 months' }, { value: String(year), label: String(year) }, { value: 'all', label: 'All time' }]} />} />
      {error && <div className="callout danger"><Icon name="alert" size={15} />{error}</div>}
      {!s ? <Loading /> : s.totals.sessions === 0 && s.finished === 0 ? <Empty title="Nothing listened in this period" icon="bar" /> : <>
        <div className="stat-tiles">
          <div className="stat-tile"><span className="stat-label">Time listened</span><span className="stat-value">{hours(s.totals.seconds)}</span><span className="stat-delta">{s.days_active} {s.days_active === 1 ? 'day' : 'days'} with a book on · {hours(s.average_seconds)} a day</span></div>
          <div className="stat-tile"><span className="stat-label">Books</span><span className="stat-value">{s.totals.books}</span><span className="stat-delta">{s.totals.sessions} sessions</span></div>
          <div className="stat-tile"><span className="stat-label">Finished</span><span className="stat-value">{s.finished}</span><span className="stat-delta">{s.finished === 1 ? 'book' : 'books'}</span></div>
          <div className="stat-tile"><span className="stat-label">Streak</span><span className="stat-value">{s.streak.current} {s.streak.current === 1 ? 'day' : 'days'}</span><span className="stat-delta">longest {s.streak.longest}{s.goal_minutes ? ` · goal ${s.goal_minutes} min` : ''}</span></div>
        </div>
        <Card title="Hours listened" className="mt">
          <Columns ariaLabel="Hours listened over time" data={s.series.map(p => ({ key: p.key, label: p.label, value: p.seconds / 3600, detail: hours(p.seconds) }))} format={n => `${+n.toFixed(1)} h`} labelEvery={s.series.length > 16 ? Math.ceil(s.series.length / 12) : 1} />
        </Card>
        {s.finished_books.length > 0 && (
          <Card title={`Finished ${s.range === 'all' ? '' : 'in this period'}`} className="mt">
            <div className="finished-row">
              {s.finished_books.map(f => (
                <Link key={f.id} to={`/book/${f.item_id}`} className="tile" title={f.title}>
                  <span className="tile-art cover"><Img src={f.cover} w={200} alt={f.title} /><span className="mark done"><Icon name="check" size={12} strokeWidth={3} /></span></span>
                  <span className="tile-title ellipsis">{f.title}</span>
                  <span className="tile-sub ellipsis">{fmtDate(f.day)}</span>
                </Link>
              ))}
            </div>
          </Card>
        )}
        <div className="two-col mt">
          <Card title="Most listened">
            <ol className="top-list">
              {s.top_books.map((r, i) => (
                <li key={r.key} className="top-row">
                  <span className="top-rank">{i + 1}</span>
                  <Link to={`/book/${r.key}`} className="top-art"><Img src={r.image} w={80} alt={r.name} /></Link>
                  <Link to={`/book/${r.key}`} className="grow ellipsis top-name">{r.name}</Link>
                  <span className="muted small nowrap">{h1(r.seconds)}</span>
                </li>
              ))}
            </ol>
          </Card>
          <Card title="Authors"><BarList data={s.top_authors.map(g => ({ key: g.key, label: g.name, value: g.seconds / 3600, detail: `${hours(g.seconds)} · ${g.count} sessions` }))} format={n => `${+n.toFixed(1)} h`} /></Card>
        </div>
        <div className="two-col mt">
          <Card title="Narrators">{s.top_narrators.length ? <BarList data={s.top_narrators.map(g => ({ key: g.key, label: g.name, value: g.seconds / 3600, detail: hours(g.seconds) }))} format={n => `${+n.toFixed(1)} h`} /> : <p className="muted small">No narrators in these books' details.</p>}</Card>
          <Card title="Genres">{s.top_genres.length ? <BarList data={s.top_genres.map(g => ({ key: g.key, label: g.name, value: g.seconds / 3600, detail: hours(g.seconds) }))} format={n => `${+n.toFixed(1)} h`} /> : <p className="muted small">No genres in these books' details.</p>}</Card>
        </div>
        <div className="two-col mt">
          <Card title="By weekday"><Columns ariaLabel="Hours listened by weekday" height={150} data={s.weekday.map((v, i) => ({ key: WEEKDAYS[i], label: WEEKDAYS[i], value: v / 3600, detail: hours(v) }))} format={n => `${+n.toFixed(1)} h`} /></Card>
          <Card title="By hour of the day"><Columns ariaLabel="Hours listened by hour of the day" height={150} labelEvery={3} data={s.hours.map((v, i) => ({ key: String(i), label: String(i), value: v / 3600, detail: `${i}:00 · ${hours(v)}` }))} format={n => `${+n.toFixed(1)} h`} /></Card>
        </div>
        {s.devices.length > 1 && <Card title="Where" className="mt"><BarList data={s.devices.map(g => ({ key: g.key, label: g.name, value: g.seconds / 3600, detail: hours(g.seconds) }))} format={n => `${+n.toFixed(1)} h`} /></Card>}
      </>}
    </div>
  )
}
