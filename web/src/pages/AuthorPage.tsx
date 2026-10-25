import { useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useApi } from '../lib/use'
import type { AuthorCard, Book } from '../lib/types'
import { V1, hours } from '../lib/books'
import { Icon } from '../components/icons'
import { BookCard, Img, NotConnected } from '../components/cards'
import { useCrumb } from '../components/Layout'
import { Loading } from '../components/ui'
import { useApp } from '../lib/app-context'
import { AuthorEditDialog, AuthorMatchDialog, type NativeAuthor } from '../components/AuthorDialogs'

export default function AuthorPage() {
  const { id = '' } = useParams()
  const { data, error, notConnected, reload } = useApi<{ author: AuthorCard; description: string; books: Book[] }>(`${V1}/authors/${encodeURIComponent(id)}`)
  const [more, setMore] = useState(false)
  const { settings } = useApp()
  // Native library: the editable profile (ASIN, photo, bio) behind the author page.
  const native = useApi<NativeAuthor>(settings?.backend === 'native' ? `${V1}/catalog/authors/${encodeURIComponent(id)}` : null)
  const [dialog, setDialog] = useState<'' | 'match' | 'edit'>('')
  useCrumb(data?.author.name)
  const groups = useMemo(() => {
    const m = new Map<string, Book[]>()
    for (const b of data?.books || []) {
      const k = b.series_list?.[0]?.name || b.series?.replace(/ #[\d.]+$/, '') || ''
      m.set(k, [...(m.get(k) || []), b])
    }
    return [...m.entries()].sort((a, b) => (a[0] === '' ? 1 : b[0] === '' ? -1 : a[0].localeCompare(b[0])))
  }, [data])
  if (notConnected) return <NotConnected message={notConnected} />
  if (error) return <div className="callout danger"><Icon name="alert" size={15} />{error}</div>
  if (!data) return <Loading />
  const profile = native.data
  const a = profile ? { ...data.author, name: profile.name, image: profile.image || undefined } : data.author
  const description = profile ? profile.description : data.description
  const saved = (p: NativeAuthor) => { native.setData(p); void reload() }
  const total = data.books.reduce((s, b) => s + b.duration, 0)
  const done = data.books.filter(b => b.finished).length
  return (
    <div>
      <section className="person-hero">
        <div className="person-photo"><Img key={a.image || 'none'} src={a.image} w={300} alt={a.name} round /></div>
        <div className="grow">
          <p className="eyebrow">Author</p>
          <h1 className="book-title">{a.name}</h1>
          <p className="muted">{data.books.length} {data.books.length === 1 ? 'book' : 'books'} · {hours(total)}{done ? ` · ${done} finished` : ''}</p>
          {profile?.asin && <p className="author-meta muted small"><span>ASIN <span className="mono">{profile.asin}</span></span></p>}
          {description && <><p className={`book-desc ${more ? '' : 'clamp'}`}>{description}</p>{description.length > 400 && <button type="button" className="link-btn small" onClick={() => setMore(m => !m)}>{more ? 'Show less' : 'Show more'}</button>}</>}
          {profile?.can_edit && <div className="author-actions">
            <button type="button" className="btn" onClick={() => setDialog('match')}><Icon name="search" size={15} />Match author</button>
            <button type="button" className="btn" onClick={() => setDialog('edit')}><Icon name="edit" size={15} />Edit</button>
          </div>}
        </div>
      </section>
      {profile && dialog === 'match' && <AuthorMatchDialog author={profile} onClose={() => setDialog('')} onSaved={saved} />}
      {profile && dialog === 'edit' && <AuthorEditDialog author={profile} onClose={() => setDialog('')} onSaved={saved} />}
      {groups.map(([name, bs]) => (
        <section key={name || '-'} className="section">
          <h2 className="section-h">{name || (groups.length > 1 ? 'Other books' : 'Books')}</h2>
          <div className="grid-covers">{bs.map(b => <BookCard key={b.id} b={b} />)}</div>
        </section>
      ))}
    </div>
  )
}
