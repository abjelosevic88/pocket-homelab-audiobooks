import { useEffect, useRef, useState } from 'react'
import { api, ApiError, V1 } from '../lib/api'
import { Icon } from './icons'
import { MenuButton, type MenuItem, Spinner, useToast } from './ui'

// "Get it": ask Pocket Requests (through Pocket Core) for the ebook of an
// audiobook, or for more books by its author, and see what is already here
// or on its way. Without the link the buttons stay, greyed out, with the reason.

type Kind = 'ebook' | 'audiobook'
interface Have { id?: string; url: string }
interface Ask { id: number; kind: Kind; language?: string; status: string; state: string; progress: number; url?: string }
interface GBook { book_key: string; title: string; subtitle: string; year: number; cover: string; ebook: Have | null; audiobook: Have | null; asked: Ask[] }
interface GetIt { available: boolean; reason?: string; found?: boolean; book?: GBook; items?: GBook[]; languages: string[]; app_url?: string }

const LANG: Record<string, string> = { en: 'English', sr: 'Serbian', hr: 'Croatian', bs: 'Bosnian', de: 'German', fr: 'French', es: 'Spanish', it: 'Italian', ru: 'Russian', sl: 'Slovenian', mk: 'Macedonian', pl: 'Polish', nl: 'Dutch', pt: 'Portuguese' }
const langName = (c?: string) => (c ? LANG[c] || c.toUpperCase() : '')

const STATE: Record<string, string> = {
  waiting: 'Waiting for approval', sent: 'Asked for', searching: 'Looking for a copy', downloading: 'Downloading', importing: 'Arriving',
  attention: 'Needs a look', available: 'Here', failed: 'Couldn’t be asked for', unreleased: 'Not out yet',
}
function stateText(a: Ask): string {
  if (a.status === 'pending') return 'Waiting for approval'
  let s = STATE[a.state] || a.state
  if (a.state === 'downloading' && a.progress > 0 && a.progress < 1) s += ` · ${Math.round(a.progress * 100)}%`
  return s
}

async function ask(body: Record<string, unknown>): Promise<{ ok: boolean; msg: string; url?: string }> {
  const what = body.kind === 'ebook' ? 'Ebook' : 'Audiobook'
  try {
    const r = await api.post<Ask>(`${V1}/get-it`, body)
    return { ok: true, msg: `${what} asked for in Pocket Requests`, url: r.url }
  } catch (e) {
    const d = e instanceof ApiError ? e.data : {}
    return { ok: false, msg: (e as Error).message, url: (d.url as string) || undefined }
  }
}

/** Menu items: the ebook in any language the box reads, or in one of them. */
function ebookItems(langs: string[], go: (lang: string) => void): MenuItem[] {
  return [
    { label: 'Any language I read', icon: 'book', onSelect: () => go('') },
    ...langs.map(l => ({ label: `In ${langName(l)}`, onSelect: () => go(l) })),
  ]
}

function AskLine({ a }: { a: Ask }) {
  const text = <>{a.kind === 'ebook' ? 'Ebook' : 'Audiobook'}{a.language ? ` (${langName(a.language)})` : ''}: {stateText(a)}</>
  return a.url
    ? <a className="getit-ask small" href={a.url} target="_blank" rel="noreferrer">{text}<Icon name="external" size={11} /></a>
    : <span className="getit-ask small">{text}</span>
}

/** The "Get it" card on an audiobook's page: its ebook. */
export function GetItCard({ itemId }: { itemId: string }) {
  const { toast } = useToast()
  const [data, setData] = useState<GetIt | null>(null)
  const [busy, setBusy] = useState(false)
  const load = (fresh = false) => api.get<GetIt>(`${V1}/get-it/items/${encodeURIComponent(itemId)}${fresh ? '?fresh=1' : ''}`).then(setData).catch(e => setData({ available: false, reason: (e as Error).message, languages: [] }))
  useEffect(() => { setData(null); void load() }, [itemId]) // eslint-disable-line react-hooks/exhaustive-deps
  const go = async (lang: string) => {
    setBusy(true)
    const r = await ask({ kind: 'ebook', item_id: itemId, book_key: data?.book?.book_key, language: lang })
    setBusy(false)
    toast(r.msg, r.ok ? 'ok' : 'err', r.url ? { label: 'Open', run: () => { window.open(r.url, '_blank', 'noopener') } } : undefined)
    void load(true)
  }
  const off = !!data && !data.available
  const b = data?.book
  const ebooks = (b?.asked || []).filter(a => a.kind === 'ebook')
  return (
    <section className="card side-card getit">
      <h3 className="row-between">Get it{data?.app_url && <a className="muted small" href={data.app_url} target="_blank" rel="noreferrer">Pocket Requests <Icon name="external" size={11} /></a>}</h3>
      {!data ? <Spinner size={13} /> : (
        <div className="getit-row">
          <span className="getit-kind"><Icon name="book" size={14} />Ebook</span>
          {b?.ebook ? (
            <a className="btn sm" href={b.ebook.url} target="_blank" rel="noreferrer"><Icon name="check" size={13} />Read in Pocket Library</a>
          ) : (
            <MenuButton label="Get the ebook" className="btn sm" align="right" items={() => ebookItems(data.languages, go)}>
              {busy ? <Spinner size={12} /> : <Icon name="download" size={13} />}Get the ebook
            </MenuButton>
          )}
        </div>
      )}
      {ebooks.map(a => <AskLine key={a.id} a={a} />)}
      {off && <p className="core-hint muted small">{data?.reason}</p>}
      {data && !off && !data.found && <p className="muted small">Pocket Requests doesn’t know this book by its title; ask for it there by hand.</p>}
    </section>
  )
}

/** "More by <author>": their best-known books, with Get menus. Loads when scrolled into view. */
export function MoreByAuthor({ authorId, name }: { authorId: string; name: string }) {
  const { toast } = useToast()
  const ref = useRef<HTMLElement>(null)
  const [seen, setSeen] = useState(false)
  const [data, setData] = useState<GetIt | null>(null)
  const url = `${V1}/get-it/authors/${encodeURIComponent(authorId)}?name=${encodeURIComponent(name)}`
  useEffect(() => {
    const el = ref.current
    if (!el || seen) return
    const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) setSeen(true) }, { rootMargin: '200px' })
    io.observe(el)
    return () => io.disconnect()
  }, [seen])
  useEffect(() => {
    if (!seen) return
    api.get<GetIt>(url).then(setData).catch(e => setData({ available: false, reason: (e as Error).message, languages: [], items: [] }))
  }, [seen, url])
  const go = async (b: GBook, kind: Kind, lang = '') => {
    const r = await ask({ kind, book_key: b.book_key, title: b.title, author: name, language: lang })
    toast(r.msg, r.ok ? 'ok' : 'err', r.url ? { label: 'Open', run: () => { window.open(r.url, '_blank', 'noopener') } } : undefined)
    api.get<GetIt>(url + '&fresh=1').then(setData).catch(() => {})
  }
  const off = !!data && !data.available
  return (
    <section ref={ref} className="section">
      <h2 className="section-h">More by {name}</h2>
      {!data ? (seen ? <Spinner size={14} /> : <p className="muted small">…</p>) : (
        <>
          {off && <p className="core-hint muted small">{data.reason}</p>}
          {(data.items || []).length === 0 && !off && <p className="muted small">Pocket Requests found nothing more by {name}.</p>}
          <div className="grid-covers">
            {(data.items || []).map(b => {
              const asked = b.asked.length > 0
              const items: MenuItem[] = [
                { heading: 'Ask Pocket Requests for' },
                { label: b.audiobook ? 'The audiobook (here)' : 'The audiobook', icon: 'headphones', disabled: !!b.audiobook || off, onSelect: () => go(b, 'audiobook') },
                'sep',
                ...ebookItems(data.languages, l => go(b, 'ebook', l)).map(x => (typeof x === 'object' && 'label' in x ? { ...x, label: <>Ebook · {x.label}</>, disabled: off } : x)),
              ]
              return (
                <div key={b.book_key} className="getit-book">
                  <div className="getit-cover">{b.cover ? <img src={b.cover} alt={b.title} loading="lazy" /> : <span className="getit-nocover"><Icon name="book" size={22} /></span>}</div>
                  <div className="getit-meta">
                    <span className="getit-title ellipsis" title={b.title}>{b.title}</span>
                    <span className="muted small">{b.year || ''}</span>
                    <span className="getit-badges">
                      {b.audiobook && <a className="chip sm" href={b.audiobook.url}><Icon name="headphones" size={11} />Here</a>}
                      {b.ebook && <a className="chip sm" href={b.ebook.url} target="_blank" rel="noreferrer"><Icon name="book" size={11} />Ebook here</a>}
                      {asked && <a className="chip sm" href={b.asked[0].url} target="_blank" rel="noreferrer">{stateText(b.asked[0])}</a>}
                    </span>
                  </div>
                  <MenuButton label={`Get ${b.title}`} className="btn ghost sm icon-only getit-menu" align="right" items={items}><Icon name="download" size={14} /></MenuButton>
                </div>
              )
            })}
          </div>
        </>
      )}
    </section>
  )
}
