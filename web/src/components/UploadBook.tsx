import { useEffect, useRef, useState } from 'react'
import { V1 } from '../lib/api'
import { useApi } from '../lib/use'
import { Field, Loading, Modal, useToast } from './ui'
import { Icon } from './icons'

type Destination = { id: string; name: string; folders: { id: string; fullPath: string }[] }
type Options = { allowed: boolean; max_bytes: number; libraries: Destination[] }
const bytes = (n: number) => n >= 1024 ** 3 ? `${(n / 1024 ** 3).toFixed(1)} GiB` : n >= 1024 ** 2 ? `${(n / 1024 ** 2).toFixed(1)} MiB` : n >= 1024 ? `${(n / 1024).toFixed(1)} KiB` : `${n} B`

export function UploadBook({ library, onClose, onUploaded }: { library?: string; onClose: () => void; onUploaded: (library: string) => void }) {
  const { data, error, notConnected } = useApi<Options>(`${V1}/upload`)
  const { toast } = useToast()
  const [destination, setDestination] = useState(library || '')
  const [folder, setFolder] = useState('')
  const [title, setTitle] = useState('')
  const [author, setAuthor] = useState('')
  const [series, setSeries] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const [busy, setBusy] = useState(false)
  const [progress, setProgress] = useState(0)
  const [failure, setFailure] = useState('')
  const [done, setDone] = useState(false)
  const request = useRef<XMLHttpRequest | null>(null)
  const lib = data?.libraries.find(l => l.id === destination) || data?.libraries[0]
  const targetFolder = lib?.folders.find(f => f.id === folder) || lib?.folders[0]
  const total = files.reduce((n, f) => n + f.size, 0)
  const tooLarge = data ? total + files.length * 1024 + 65536 > data.max_bytes : false
  useEffect(() => () => { request.current?.abort() }, [])
  useEffect(() => {
    if (!busy) return
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [busy])
  const close = () => { request.current?.abort(); onClose() }
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!lib || !targetFolder || !files.length || !title.trim() || tooLarge || busy) return
    const body = new FormData()
    body.append('library', lib.id); body.append('folder', targetFolder.id)
    body.append('title', title.trim()); body.append('author', author.trim()); body.append('series', series.trim())
    files.forEach((file, i) => body.append(String(i), file, file.name))
    const xhr = new XMLHttpRequest()
    request.current = xhr
    xhr.open('POST', `${V1}/upload`)
    xhr.withCredentials = true
    xhr.upload.onprogress = event => { if (event.lengthComputable) setProgress(Math.min(100, Math.round(event.loaded / event.total * 100))) }
    xhr.onerror = () => { setBusy(false); setFailure('The connection was interrupted. Check the library before retrying.') }
    xhr.onabort = () => { request.current = null }
    xhr.onload = () => {
      request.current = null; setBusy(false)
      if (xhr.status >= 200 && xhr.status < 300) {
        setDone(true); onUploaded(lib.id)
        toast('Files uploaded. The book will appear after scanning finishes.')
        return
      }
      if (xhr.status === 401) window.dispatchEvent(new CustomEvent('pab:unauthorized'))
      let message = xhr.responseText.trim()
      try { message = JSON.parse(message).error || message } catch { /* ABS also returns plain text. */ }
      if (xhr.status === 413) message = 'These files exceed the upload size limit.'
      if (!message || message.startsWith('<')) message = `Upload failed (${xhr.status}). Check the library before retrying.`
      setFailure(message.slice(0, 400))
    }
    setFailure(''); setProgress(0); setBusy(true); xhr.send(body)
  }
  return <Modal title="Upload audiobook" onClose={close}>
    {error || notConnected ? <p className="callout danger" role="alert">{error || notConnected}</p> : !data ? <Loading /> : !data.allowed ?
      <p>Your Audiobookshelf account does not have upload permission. An Audiobookshelf administrator can enable it for you.</p> : done ?
      <div className="stack"><p>Upload complete. Your files are saved in <b>{lib?.name}</b>. The book will appear once Audiobookshelf finishes scanning.</p><button className="btn primary" onClick={close}>Done</button></div> : !data.libraries.length ?
      <p>No book libraries are available for upload. Add one in Audiobookshelf first.</p> :
      <form onSubmit={submit} className="stack">
        <p className="muted">Choose the audio files for one book. You can include a cover or ebook alongside them.</p>
        <fieldset disabled={busy} className="upload-fields stack">
          <Field label="Library"><select value={lib?.id || ''} onChange={e => { setDestination(e.target.value); setFolder('') }}>{data.libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</select></Field>
          <Field label="Folder"><select value={targetFolder?.id || ''} onChange={e => setFolder(e.target.value)}>{lib?.folders.map(f => <option key={f.id} value={f.id}>{f.fullPath}</option>)}</select></Field>
          {!targetFolder && <p className="callout danger">This library has no upload folder.</p>}
          <Field label="Files" help={`One book per upload · up to ${bytes(data.max_bytes)} total.`}>
            <input type="file" multiple accept="audio/*,.m4b,.m4a,.mp3,.flac,.ogg,.opus,.aac,.wav,.wma,.epub,.pdf,.jpg,.jpeg,.png,.webp" required onChange={e => {
              const chosen = Array.from(e.target.files || [])
              setFiles(chosen)
              if (!title.trim() && chosen.length) setTitle(chosen[0].name.replace(/\.[^.]+$/, ''))
            }} />
          </Field>
          {files.length > 0 && <div className="upload-file-list"><p>{files.length} {files.length === 1 ? 'file' : 'files'} · {bytes(total)}</p><ul>{files.map((f, i) => <li key={i}>{f.name} <span className="muted">({bytes(f.size)})</span></li>)}</ul></div>}
          <Field label="Book title" help="Files go into a folder named after the title."><input required value={title} onChange={e => setTitle(e.target.value)} maxLength={250} /></Field>
          <Field label="Author (optional)"><input value={author} onChange={e => setAuthor(e.target.value)} maxLength={250} /></Field>
          <Field label="Series (optional)"><input value={series} onChange={e => setSeries(e.target.value)} maxLength={250} /></Field>
        </fieldset>
        {tooLarge && <p className="callout danger" role="alert">The selected files exceed the {bytes(data.max_bytes)} upload limit.</p>}
        {failure && <p className="callout danger" role="alert">{failure}</p>}
        {busy && <div role="status" aria-live="polite"><progress value={progress} max={100} aria-label="Upload progress" /><p>{progress < 100 ? `Uploading… ${progress}%` : 'Files sent. Processing your book…'}</p></div>}
        <div className="row wrap"><button type="submit" className="btn primary" disabled={busy || !files.length || !title.trim() || !targetFolder || tooLarge}><Icon name="upload" size={16} />{busy ? 'Uploading…' : 'Upload book'}</button><button type="button" className="btn" onClick={close}>{busy ? 'Cancel upload' : 'Cancel'}</button></div>
      </form>}
  </Modal>
}
