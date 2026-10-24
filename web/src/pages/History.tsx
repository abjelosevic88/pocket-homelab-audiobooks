import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApi } from '../lib/use'
import type { Listen } from '../lib/types'
import { clock, hours } from '../lib/books'
import { fmtDate, fmtISOTime } from '../lib/format'
import { Icon } from '../components/icons'
import { Img } from '../components/cards'
import { Empty, Loading, PageHeader, errMsg, useDebounce, useToast } from '../components/ui'

/** Every listening session, grouped by day: from this app and every Audiobookshelf app. */
export default function History() {
  const { toast } = useToast()
  const [sp, setSp] = useSearchParams()
  const book = sp.get('book') || ''
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 250)
  const [limit, setLimit] = useState(150)
  const { data, setData, error } = useApi<{ items: Listen[]; total: number }>(`${V1}/history?${new URLSearchParams({ q: dq, book, limit: String(limit) })}`)
  const days = useMemo(() => {
    const m = new Map<string, Listen[]>()
    for (const l of data?.items || []) m.set(l.day, [...(m.get(l.day) || []), l])
    return [...m.entries()]
  }, [data])
  const remove = async (l: Listen) => {
    try { await api.del(`${V1}/history/${l.id}`); if (data) setData({ ...data, items: data.items.filter(x => x.id !== l.id), total: data.total - 1 }) } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <div>
      <PageHeader title="History" sub={data ? `${data.total.toLocaleString()} listening sessions, here and in your connected players` : ' '}
        actions={<Link to="/stats" className="btn sm"><Icon name="bar" size={14} />Stats</Link>} />
      <div className="toolbar row wrap">
        <div className="search-box"><Icon name="search" size={15} /><input value={q} onChange={e => setQ(e.target.value)} placeholder="Find a book, author or narrator" aria-label="Filter history" /></div>
        {book && <button type="button" className="chip on" onClick={() => { const n = new URLSearchParams(sp); n.delete('book'); setSp(n, { replace: true }) }}>One book<Icon name="x" size={12} /></button>}
      </div>
      {error && <div className="callout danger"><Icon name="alert" size={15} />{error}</div>}
      {!data ? <Loading /> : data.items.length === 0 ? <Empty title="Nothing listened yet" icon="history">What you listen to here and in your connected players appears in your history.</Empty> : (
        <div className="history">
          {days.map(([day, ls]) => (
            <section key={day} className="h-day">
              <h3 className="h-date">{fmtDate(day)} <span className="muted small">{hours(ls.reduce((a, l) => a + l.seconds, 0))}</span></h3>
              {ls.map(l => (
                <div key={l.id} className="h-row">
                  <Link to={`/book/${l.item_id}`} className="h-art"><Img src={l.cover} w={90} alt={l.title} /></Link>
                  <div className="grow h-main">
                    <Link to={`/book/${l.item_id}`} className="h-title ellipsis">{l.title}</Link>
                    <span className="muted small ellipsis">{l.author}{l.duration ? ` · ${clock(l.start_pos)} → ${clock(l.end_pos)} of ${hours(l.duration)}` : ''}</span>
                  </div>
                  <span className="h-meta small muted">
                    <span className="tnum">{hours(l.seconds)}</span>
                    <span title={l.device || (l.source === 'pocket' ? 'Pocket Audiobooks' : 'Audiobookshelf')}>{fmtISOTime(l.started_at)}{l.device ? ` · ${l.device.split(' · ')[0]}` : ''}</span>
                  </span>
                  <button type="button" className="btn ghost icon-only xs h-del" aria-label="Remove from history" title="Remove from your listening history" onClick={() => remove(l)}><Icon name="x" size={14} /></button>
                </div>
              ))}
            </section>
          ))}
          {data.total > data.items.length && <div className="row mt"><button type="button" className="btn" onClick={() => setLimit(l => l + 300)}>Show more</button></div>}
        </div>
      )}
    </div>
  )
}
