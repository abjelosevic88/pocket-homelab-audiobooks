import { useEffect, useState } from 'react'
import { MetadataSearch } from './MetadataSearch'
import { api, V1, upload } from '../lib/api'
import type { Chapter } from '../lib/types'
import { Field, Loading, Modal, errMsg, useToast } from './ui'

type Named = { id?: string; name: string; sequence?: string }
type Form = { expected_updated_at: number; metadata: Record<string, unknown> & { title: string; authors: Named[]; series: Named[] }; chapters: Chapter[] }
export function BookEditor({ id, onClose, onSaved }: { id: string; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<Form | null>(null)
  const [coverURL, setCoverURL] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const endpoint = `${V1}/catalog/items/${encodeURIComponent(id)}/editor`
  useEffect(() => { api.get<Form>(endpoint).then(setForm).catch(e => setError(errMsg(e))) }, [endpoint])
  const set = (key: string, value: unknown) => setForm(f => f && ({ ...f, metadata: { ...f.metadata, [key]: value } }))
  const save = async (event: React.FormEvent) => {
    event.preventDefault(); if (!form) return
    setBusy(true); setError('')
    try { await api.put(endpoint, form); toast('Book saved'); onSaved(); onClose() }
    catch (e) { setError(errMsg(e)) }
    finally { setBusy(false) }
  }
  const cover = async (file?: File) => {
    if (!file) return
    setBusy(true); setError('')
    try { const result = await upload<{ updated_at: number }>(`${V1}/catalog/items/${encodeURIComponent(id)}/cover`, 'file', file); setForm(f => f && ({ ...f, expected_updated_at: result.updated_at })); onSaved(); toast('Cover saved') }
    catch (e) { setError(errMsg(e)) }
    finally { setBusy(false) }
  }
  return <Modal title="Edit book" size="lg" onClose={onClose}>
    {error && <p role="alert" className="callout danger">{error}</p>}
    {!form ? <Loading /> : <form className="stack" onSubmit={save}>
      <MetadataSearch title={form.metadata.title} author={form.metadata.authors.map(a => a.name).join(', ')} onSelect={(metadata, cover) => { setForm(f => f && ({ ...f, metadata: { ...f.metadata, ...metadata } })); setCoverURL(cover) }} />
      <Field label="Cover URL" help="A cover is saved when you press Use cover."><input type="url" value={coverURL} onChange={e => setCoverURL(e.target.value)} /></Field>
      {coverURL && <button type="button" className="btn" disabled={busy} onClick={async () => {setBusy(true);setError('');try {const result=await api.post<{updated_at:number}>(`${V1}/catalog/items/${encodeURIComponent(id)}/cover-url`,{url:coverURL});setForm(f=>f && ({...f,expected_updated_at:result.updated_at}));onSaved();toast('Cover saved')}catch(e){setError(errMsg(e))}finally{setBusy(false)}}}>Use cover</button>}
      <Field label="Cover image" help="JPEG or PNG. The image is saved when selected."><input type="file" accept="image/jpeg,image/png" disabled={busy} onChange={e => { void cover(e.target.files?.[0]) }} /></Field>
      <Field label="Title"><input required value={form.metadata.title} onChange={e => set('title', e.target.value)} /></Field>
      <Field label="Authors" help="Separate names with a semicolon."><input value={form.metadata.authors.map(a => a.name).join('; ')} onChange={e => set('authors', e.target.value.split(';').map(name => ({ name: name.trim() })).filter(a => a.name))} /></Field>
      <Field label="Description"><textarea rows={5} value={String(form.metadata.description || '')} onChange={e => set('description', e.target.value)} /></Field>
      {(['subtitle', 'publisher', 'publishedYear', 'language', 'isbn', 'asin'] as const).map(key => <Field key={key} label={{ subtitle: 'Subtitle', publisher: 'Publisher', publishedYear: 'Publication year', language: 'Language', isbn: 'ISBN', asin: 'ASIN' }[key]}><input value={String(form.metadata[key] || '')} onChange={e => set(key, e.target.value)} /></Field>)}
      {(['narrators', 'genres', 'tags'] as const).map(key => <Field key={key} label={{ narrators: 'Narrators', genres: 'Genres', tags: 'Tags' }[key]} help="Separate entries with a semicolon."><input value={(form.metadata[key] as string[] || []).join('; ')} onChange={e => set(key, e.target.value.split(';').map(x => x.trim()).filter(Boolean))} /></Field>)}
      {(['explicit', 'abridged'] as const).map(key => <label key={key}><input type="checkbox" checked={!!form.metadata[key]} onChange={e => set(key, e.target.checked)} /> {key === 'explicit' ? 'Explicit content' : 'Abridged edition'}</label>)}
      <fieldset className="stack"><legend>Series</legend>
        {form.metadata.series.map((series, i) => <div className="row" key={i}>
          <input aria-label={`Series ${i + 1} name`} placeholder="Series name" value={series.name} onChange={e => set('series', form.metadata.series.map((s, j) => j === i ? { ...s, name: e.target.value } : s))} />
          <input aria-label={`Series ${i + 1} book number`} placeholder="Book number" value={series.sequence || ''} onChange={e => set('series', form.metadata.series.map((s, j) => j === i ? { ...s, sequence: e.target.value } : s))} />
          <button type="button" className="btn" onClick={() => set('series', form.metadata.series.filter((_, j) => j !== i))}>Remove</button>
        </div>)}
        <button type="button" className="btn" onClick={() => set('series', [...form.metadata.series, { name: '', sequence: '' }])}>Add series</button>
      </fieldset>
      <details><summary>Chapters ({form.chapters.length})</summary><p className="muted small">Times are seconds from the beginning of the book.</p>
        <div className="stack">{form.chapters.map((chapter, i) => <div className="row wrap" key={i}>
          <input aria-label={`Chapter ${i + 1} title`} value={chapter.title} onChange={e => setForm({ ...form, chapters: form.chapters.map((c, j) => i === j ? { ...c, title: e.target.value } : c) })} />
          {(['start', 'end'] as const).map(key => <input key={key} aria-label={`Chapter ${i + 1} ${key} seconds`} type="number" min={0} step="0.001" value={chapter[key]} onChange={e => setForm({ ...form, chapters: form.chapters.map((c, j) => i === j ? { ...c, [key]: Number(e.target.value) } : c) })} />)}
          <button type="button" className="btn" onClick={() => setForm({ ...form, chapters: form.chapters.filter((_, j) => i !== j) })}>Remove</button>
        </div>)}<button type="button" className="btn" onClick={() => { const start = form.chapters.at(-1)?.end || 0; setForm({ ...form, chapters: [...form.chapters, { id: form.chapters.length, title: '', start, end: start }] }) }}>Add chapter</button></div>
      </details>
      <div className="row"><button type="submit" className="btn primary" disabled={busy}>{busy ? 'Saving…' : 'Save changes'}</button><button type="button" className="btn" onClick={onClose}>Cancel</button></div>
    </form>}
  </Modal>
}
