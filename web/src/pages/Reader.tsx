import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import type Book from 'epubjs/types/book'
import type Rendition from 'epubjs/types/rendition'
import type { Location } from 'epubjs/types/rendition'
import type { NavItem } from 'epubjs/types/navigation'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { errMsg } from '../components/ui'
import '../styles/reader.css'

type Position = { location: string; progress: number; revision: string }
type ReaderInfo = { title: string; files: { id: string; name: string }[]; primary: string; file_id: string; position: Position }

export default function Reader() {
 const { id = '' } = useParams()
 const { user } = useApp()
 const [query] = useSearchParams()
 // A new file/account gets a fresh rendering and save queue.
 return <ReaderFile key={`${user?.id}:${id}:${query.get('file') || ''}`} id={id} file={query.get('file') || ''} userID={user!.id} />
}
function ReaderFile({id,file,userID}:{id:string;file:string;userID:number}) {
 const [info,setInfo]=useState<ReaderInfo>()
 const [error,setError]=useState('')
 const [status,setStatus]=useState('Opening ebook…')
 const [conflict,setConflict]=useState(false)
 const [ready,setReady]=useState(false)
 const [toc,setTOC]=useState<NavItem[]>([])
 const [percent,setPercent]=useState(0)
 const [font,setFont]=useState(110)
 const [page,setPage]=useState(1)
 const [pages,setPages]=useState(0)
 const [pdf,setPDF]=useState<PDFDocumentProxy>()
 const host=useRef<HTMLDivElement>(null)
 const canvas=useRef<HTMLCanvasElement>(null)
 const textLayer=useRef<HTMLDivElement>(null)
 const rendition=useRef<Rendition | undefined>(undefined)
 const save=useRef<()=>Promise<void>>(async()=>{})
 const changed=useRef<(location:string,progress:number)=>void>(()=>{})
 const position=useRef<Position | undefined>(undefined)
 const key=`pab-reading:${userID}:${id}:${file}`
 const isPDF=info?.files.find(f=>f.id===info.file_id)?.name.toLowerCase().endsWith('.pdf')

 useEffect(()=>{
  let dead=false, busy=false, halted=false, pending:Position|undefined, timer:ReturnType<typeof setTimeout>|undefined
  let ebook:Book|undefined, loadingPDF:{destroy:()=>Promise<void>}|undefined
  const abort=new AbortController()
  let endpoint=''
  const flush=async()=>{
   if (busy || halted || !pending || !endpoint) return
   busy=true
   const sent={...pending,revision:position.current!.revision}
   try {
    setStatus('Saving…')
    const next=await api.put<Position>(endpoint,{...sent,user_id:userID})
    position.current=next
    if (pending?.location===sent.location && pending?.progress===sent.progress) {pending=undefined;localStorage.removeItem(key)}
    else if (pending) {pending.revision=next.revision;localStorage.setItem(key,JSON.stringify(pending))}
    if (!dead) setStatus('Place saved')
   } catch(e) {
    if(e instanceof ApiError && e.status===409) {halted=true;if(!dead){setConflict(true);setError('Your reading position changed on another device. Reload to use that position.')}}
    else if(!dead) setError('Could not save your place. Your position is kept on this device; retry when connected.')
   } finally {busy=false}
   if (!halted && pending && (pending.location!==sent.location || pending.progress!==sent.progress)) await flush()
  }
  save.current=flush
  changed.current=(location,progress)=>{
   if(dead || halted || !position.current || !location)return
   const p=Math.min(1,Math.max(0,progress));setPercent(p)
   if (position.current.location===location && position.current.progress===p && !pending)return
   pending={location,progress:p,revision:position.current.revision}
   localStorage.setItem(key,JSON.stringify(pending));setStatus('Saving…')
   clearTimeout(timer);timer=setTimeout(()=>void flush(),500)
  }
  const leave=()=>{
   if(document.visibilityState==='visible')return
   if(pending && !busy && endpoint && !halted) {
    const sent={...pending,revision:position.current!.revision}
    // Keep the durable receipt until a later acknowledged retry; the server
    // accepts an identical position even if this response is lost on navigation.
    void fetch(endpoint,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({...sent,user_id:userID}),keepalive:true}).catch(()=>{})
   }
  }
  document.addEventListener('visibilitychange',leave)
  const open=async()=>{
   const detail=await api.get<ReaderInfo>(`${V1}/catalog/items/${encodeURIComponent(id)}/reader${file?'?file='+encodeURIComponent(file):''}`)
   if(dead)return
   setInfo(detail);position.current=detail.position;setPercent(detail.position.progress)
   endpoint=`${V1}/catalog/items/${encodeURIComponent(id)}/reader?file=${encodeURIComponent(detail.file_id)}`
   const stored=localStorage.getItem(key)
   if(stored) {
    try {
     const p=JSON.parse(stored) as Position
     if(p.revision===detail.position.revision || (p.location===detail.position.location && p.progress===detail.position.progress)) {
      pending=p;await flush();if(pending)detail.position=p;else detail.position=position.current!
     } else {halted=true;setConflict(true);setError('A saved position on this device conflicts with a newer position. Reload to use the server position.')}
    } catch {localStorage.removeItem(key)}
   }
   if(dead)return
   const url=`${V1}/catalog/items/${encodeURIComponent(id)}/reader/${encodeURIComponent(detail.file_id)}`
   const name=detail.files.find(f=>f.id===detail.file_id)!.name
   if(name.toLowerCase().endsWith('.pdf')) {
    const lib=await import('pdfjs-dist/legacy/build/pdf.mjs')
    lib.GlobalWorkerOptions.workerSrc=new URL('pdfjs-dist/legacy/build/pdf.worker.min.mjs',import.meta.url).toString()
    if(dead)return
    const task=lib.getDocument({url,enableXfa:false});loadingPDF=task
    const doc=await task.promise
    if(dead){await task.destroy();return}
    const n=Number(detail.position.location.replace(/^page:/,''))
    setPage(Number.isInteger(n)&&n>0?Math.min(n,doc.numPages):1);setPages(doc.numPages);setPDF(doc);setReady(true);setStatus('Ready')
   } else {
    const response=await fetch(url,{signal:abort.signal})
    if(!response.ok) {const e=await response.json().catch(()=>({}));throw new Error(e.error || 'Cannot open this ebook')}
    const data=await response.arrayBuffer()
    const {default:EPUB}=await import('epubjs')
    if(dead)return
    ebook=EPUB({replacements:'blobUrl'});await ebook.open(data,'binary')
    if(dead)return
    // Content is sanitized by Go; EPUB.js keeps its script-disabled iframe sandbox.
    const view=ebook.renderTo(host.current!,{width:'100%',height:'100%',spread:'none',allowScriptedContent:false})
    rendition.current=view;view.themes.fontSize('110%')
    setTOC((await ebook.loaded.navigation).toc)
    await ebook.locations.generate(1200)
    if(dead)return
    const start=detail.position.location.startsWith('epubcfi(')?detail.position.location:undefined
    await view.display(start)
    if(dead)return
    view.on('relocated',(loc:Location)=>{
     const progress=loc.atEnd?1:ebook!.locations.percentageFromCfi(loc.start.cfi)
     changed.current(loc.start.cfi,Number.isFinite(progress)?progress:0)
    })
    view.on('keydown',(e:KeyboardEvent)=>{if(e.key==='ArrowRight')void view.next();if(e.key==='ArrowLeft')void view.prev()})
    setReady(true);setStatus('Ready')
   }
  }
  void open().catch(e=>{if(!dead){setError(errMsg(e));setStatus('Could not open ebook')}})
  return()=>{dead=true;abort.abort();clearTimeout(timer);document.removeEventListener('visibilitychange',leave);void flush();rendition.current=undefined;ebook?.destroy();void loadingPDF?.destroy()}
 },[id,file,key])
 useEffect(()=>{rendition.current?.themes.fontSize(`${font}%`)},[font])
 useEffect(()=>{
  if(!pdf || !canvas.current || !host.current || !textLayer.current)return
  let cancelled=false,task:{cancel:()=>void}|undefined,text:{cancel:()=>void}|undefined
  const render=async()=>{
   const lib=await import('pdfjs-dist/legacy/build/pdf.mjs')
   const p=await pdf.getPage(page)
   if(cancelled)return
   const available=Math.max(280,host.current!.clientWidth-32)
   const initial=p.getViewport({scale:1});const scale=Math.min(2,available/initial.width)*(font/110)
   const viewport=p.getViewport({scale});const c=canvas.current!;const layer=textLayer.current!
   const dpr=Math.min(window.devicePixelRatio || 1,2)
   c.width=Math.floor(viewport.width*dpr);c.height=Math.floor(viewport.height*dpr);c.style.width=`${viewport.width}px`;c.style.height=`${viewport.height}px`
   layer.replaceChildren();layer.style.width=`${viewport.width}px`;layer.style.height=`${viewport.height}px`;layer.style.setProperty('--total-scale-factor',String(scale))
   const job=p.render({canvas:c,viewport,transform:dpr===1?undefined:[dpr,0,0,dpr,0,0]});task=job;await job.promise
   if(cancelled)return
   const t=new lib.TextLayer({textContentSource:await p.getTextContent(),container:layer,viewport});text=t;await t.render()
   if(cancelled)return
   changed.current(String(page),page/pdf.numPages)
  }
  void render().catch(e=>{if(!cancelled)setError(errMsg(e))})
  return()=>{cancelled=true;task?.cancel();text?.cancel()}
 },[pdf,page,font])
 const move=useCallback((delta:number)=>{if(pdf)setPage(p=>Math.min(pdf.numPages,Math.max(1,p+delta)));else {const v=rendition.current;if(v)void (delta>0?v.next():v.prev()).catch(e=>setError(errMsg(e)))}},[pdf])
 const flatten=(nodes:NavItem[],depth=0):{href:string;label:string}[]=>nodes.flatMap(n=>[{href:n.href,label:'— '.repeat(depth)+n.label},...flatten(n.subitems||[],depth+1)])
 return <main className="ebook-reader">
  <header className="reader-toolbar">
   <Link to={`/book/${encodeURIComponent(id)}`} onClick={()=>void save.current()}>← Book</Link>
   <strong className="ellipsis">{info?.title || 'Reader'}</strong>
   <label>File <select aria-label="File" value={info?.file_id || ''} onChange={e=>{void save.current();window.location.assign(`/read/${encodeURIComponent(id)}?file=${encodeURIComponent(e.target.value)}`)}}>{info?.files.map(f=><option key={f.id} value={f.id}>{f.name}</option>)}</select></label>
   <label>Text size <select aria-label="Text size" value={font} onChange={e=>setFont(Number(e.target.value))}>{[90,100,110,125,150,175,200].map(n=><option key={n} value={n}>{n}%</option>)}</select></label>
   {toc.length>0 && <label>Contents <select aria-label="Contents" defaultValue="" onChange={e=>{if(e.target.value)void rendition.current?.display(e.target.value).catch(e=>setError(errMsg(e)))}}><option value="">Go to chapter…</option>{flatten(toc).map((n,i)=><option key={i} value={n.href}>{n.label}</option>)}</select></label>}
  </header>
  {error && <div className="callout danger" role="alert">{error} {conflict?<button className="btn" onClick={()=>{localStorage.removeItem(key);window.location.reload()}}>Use server position</button>:<button className="btn" onClick={()=>{setError('');void save.current()}}>Retry save</button>}</div>}
  <div className={`reader-content ${isPDF?'reader-pdf':''}`} ref={host} aria-label="Ebook content">{isPDF && <div className="reader-pdf-page"><canvas ref={canvas} aria-label={`Page ${page}`} /><div className="textLayer" ref={textLayer} /></div>}</div>
  <footer className="reader-toolbar">
   <button className="btn" disabled={!ready || !!pdf&&page===1} onClick={()=>move(-1)}>Previous page</button>
   {pdf && <label>Page <input aria-label="Page" type="number" min={1} max={pages} value={page} onChange={e=>{const n=Number(e.target.value);if(Number.isInteger(n)&&n>=1&&n<=pages)setPage(n)}} /> of {pages}</label>}
   <span>{Math.round(percent*100)}%</span><span role="status">{status}</span>
   <button className="btn" disabled={!ready || !!pdf&&page===pages} onClick={()=>move(1)}>Next page</button>
  </footer>
 </main>
}
