import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api, V1 } from './api'
import { flushReports, queueReport } from './playback-outbox'
import { useApp } from './app-context'
import { useToast } from '../components/ui'
import { CLIENT_ID, clock, cover, deviceName, markUndecodable, playableTypes } from './books'
import type { Book, Chapter, NowPlaying, PlayInfo, PlayTrack } from './types'

/** The one audio element of the app and everything around it: the book that
 *  plays (one or many files, played as one timeline), its Audiobookshelf
 *  session and reports, chapters, speed, the sleep timer, bookmarks,
 *  lock-screen controls, keyboard shortcuts, and what plays on other devices. */

interface PlayerState {
  book: Book | null
  playing: boolean
  loading: boolean
  time: number
  duration: number
  rate: number
  chapters: Chapter[]
  chapter: Chapter | null
  chapterIndex: number
  sleepAt: number | null
  sleepChapter: boolean
  expanded: boolean
  play: (b: Book, opts?: { at?: number; transcode?: boolean }) => void
  toggle: () => void
  pause: () => void
  seek: (t: number) => void
  skip: (delta: number) => void
  chapterStep: (dir: number) => void
  setRate: (r: number) => void
  setSleep: (minutes: number | null | 'chapter') => void
  stop: () => void
  bookmark: () => void
  setExpanded: (v: boolean) => void
  isCurrent: (id: string) => boolean
  remote: NowPlaying | null
  remoteVisible: boolean
  clockOffset: number
}

const Ctx = createContext<PlayerState>(null!)
export function usePlayer() { return useContext(Ctx) }

const LAST_KEY = 'pab-last-book'
const REPORT_EVERY = 15 // seconds, like the Audiobookshelf apps
const rateKey = (id: string) => `pab-rate-${id}`

interface Loaded { owner: number; native: boolean; book: Book; session: string; method: 'direct' | 'hls'; tracks: PlayTrack[]; chapters: Chapter[]; duration: number }

export function PlayerProvider({ children }: { children: React.ReactNode }) {
  const { settings, user } = useApp()
  const owner = useRef(user?.id)
  owner.current = user?.id
  const reportClock = useRef({ server: Date.now(), monotonic: performance.now(), last: 0 })
  const observedAt = () => { const c = reportClock.current; c.last = Math.max(c.last + 1, Math.round(c.server + performance.now() - c.monotonic)); return c.last }
  const lastKey = `${LAST_KEY}:${user?.id || 'none'}`
  const { toast } = useToast()
  const audio = useMemo(() => { const a = new Audio(); a.preload = 'auto'; return a }, [])
  const [book, setBook] = useState<Book | null>(null)
  const [playing, setPlaying] = useState(false)
  const [loading, setLoading] = useState(false)
  const [time, setTime] = useState(0)
  const [duration, setDuration] = useState(0)
  const [rate, setRateState] = useState(1)
  const [chapters, setChapters] = useState<Chapter[]>([])
  const [sleepAt, setSleepAt] = useState<number | null>(null)
  const [sleepChapter, setSleepChapter] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const cur = useRef<Loaded | null>(null)   // the open session
  const pending = useRef<Book | null>(null) // restored after a reload: opens a session on play
  const trackIdx = useRef(0)
  const listened = useRef(0)                // seconds heard since the last report
  const lastT = useRef(0)
  const lastReport = useRef(0)
  const pausedAt = useRef(0)
  const sleepTimer = useRef<number | null>(null)
  const sleepChapterEnd = useRef<number | null>(null)
  const hls = useRef<{ destroy: () => void } | null>(null)
  const settingsRef = useRef(settings)
  useEffect(() => { settingsRef.current = settings }, [settings])

  const globalTime = useCallback(() => {
    const c = cur.current
    if (!c) return 0
    return (c.tracks[trackIdx.current]?.start || 0) + (audio.currentTime || 0)
  }, [audio])

  // ---- reports ----
  const report = useCallback(async (opts: { close?: boolean; ended?: boolean; force?: boolean } = {}) => {
    const c = cur.current
    if (!c) return
    const l = listened.current
    if (!opts.force && !opts.close && !opts.ended && l < 1) return
    listened.current = 0
    lastReport.current = Date.now()
    const t = opts.ended ? c.duration : globalTime()
    try {
      if (c.native) {
        const body = { report_id: crypto.randomUUID(), observed_at: observedAt(), current_time: t, listened: l, duration: c.duration, playing: !audio.paused, close: !!opts.close, ended: !!opts.ended, item_id: c.book.id, client: CLIENT_ID, device: deviceName() }
        queueReport(c.owner, c.session, body)
        await flushReports(c.owner, () => owner.current === c.owner)
        window.dispatchEvent(new CustomEvent('pab:progress', { detail: { id: c.book.id, time: t, ended: !!opts.ended } }))
        return
      }
      const r = await api.post<{ session_id?: string; method?: 'direct' | 'hls'; tracks?: PlayTrack[] }>(`${V1}/sessions/${encodeURIComponent(c.session)}/sync`, {
        current_time: t, listened: l, duration: c.duration, playing: !audio.paused, close: opts.close, ended: opts.ended, item_id: c.book.id, client: CLIENT_ID, device: deviceName(),
      })
      if (r?.session_id && cur.current === c) {
        // Audiobookshelf had forgotten the session (a restart): carry on in the new one
        c.session = r.session_id
        if (r.method === 'hls' || c.method === 'hls') { c.method = r.method || c.method; c.tracks = r.tracks || c.tracks; loadTrackAt(t, !audio.paused) }
      }
      if (cur.current === c) window.dispatchEvent(new CustomEvent('pab:progress', { detail: { id: c.book.id, time: t, ended: !!opts.ended } }))
    } catch { /* offline: the next report carries the seconds */ if (!c.native) listened.current += l }
  }, [audio, globalTime, user]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- loading files ----
  const attach = useCallback(async (tr: PlayTrack, at: number, autoplay: boolean) => {
    hls.current?.destroy(); hls.current = null
    const isHLS = tr.url.endsWith('.m3u8')
    const onMeta = () => {
      audio.removeEventListener('loadedmetadata', onMeta)
      if (at > 0.5) audio.currentTime = at
      lastT.current = audio.currentTime
      audio.playbackRate = rateRef.current
      if (autoplay) audio.play().catch(err => { setLoading(false); if ((err as Error).name !== 'AbortError') toast('Could not play: ' + (err as Error).message, 'err') })
    }
    audio.addEventListener('loadedmetadata', onMeta)
    if (isHLS && !audio.canPlayType('application/vnd.apple.mpegurl')) {
      const { default: Hls } = await import('hls.js')
      const h = new Hls({ startPosition: at })
      h.loadSource(tr.url)
      h.attachMedia(audio)
      hls.current = h
    } else {
      audio.src = tr.url
      audio.load()
    }
  }, [audio, toast])

  const rateRef = useRef(1)
  const wantAt = useRef(0) // where the last load meant to start (the place survives a file that fails to load)
  const loadTrackAt = useCallback((t: number, autoplay: boolean) => {
    const c = cur.current
    if (!c || !c.tracks.length) return
    wantAt.current = t
    let i = c.tracks.findIndex(x => t >= x.start && t < x.start + x.duration)
    if (i < 0) i = t >= c.duration ? c.tracks.length - 1 : 0
    trackIdx.current = i
    setLoading(autoplay)
    attach(c.tracks[i], Math.max(0, t - c.tracks[i].start), autoplay)
  }, [attach])

  const applyMediaSession = useCallback((b: Book | null) => {
    if (!('mediaSession' in navigator)) return
    if (!b) { navigator.mediaSession.metadata = null; return }
    const art = b.cover ? new URL(cover(b.cover, 512), window.location.origin).href : ''
    navigator.mediaSession.metadata = new MediaMetadata({ title: b.title, artist: b.author, album: b.series || b.title, artwork: art ? [{ src: art, sizes: '512x512' }] : [] })
  }, [])

  // ---- playing a book ----
  const play = useCallback(async (b: Book, opts: { at?: number; transcode?: boolean } = {}) => {
    const same = cur.current?.book.id === b.id
    if (same && !opts.transcode && opts.at === undefined) { audio.play().catch(() => {}); return }
    if (same && !opts.transcode && opts.at !== undefined) { seekTo(opts.at); audio.play().catch(() => {}); return }
    if (cur.current && !opts.transcode) { await report({ close: true, force: true }); audio.pause() }
    pending.current = null
    setLoading(true)
    setBook(b)
    applyMediaSession(b)
    try {
      if (user) await flushReports(user.id, () => owner.current === user.id)
      const info = await api.post<PlayInfo>(`${V1}/play/${encodeURIComponent(b.id)}`, { mime_types: playableTypes(), client: CLIENT_ID, device: deviceName(), force_transcode: !!opts.transcode })
      if (!user || owner.current !== user.id) return
      if (info.server_time) reportClock.current = { server: info.server_time, monotonic: performance.now(), last: 0 }
      const loaded: Loaded = { owner: user.id, native: info.backend === 'native', book: info.book, session: info.session_id, method: info.method, tracks: info.tracks, chapters: info.chapters || [], duration: info.duration }
      cur.current = loaded
      setBook(info.book)
      setChapters(loaded.chapters)
      setDuration(info.duration)
      localStorage.setItem(lastKey, b.id)
      const r = Number(localStorage.getItem(rateKey(b.id))) || settingsRef.current?.speed || info.speed || 1
      rateRef.current = r
      setRateState(r)
      listened.current = 0
      let start = opts.at ?? info.current_time ?? 0
      // after a while away, go back a few seconds to pick up the thread
      const back = settingsRef.current?.rewind_on_resume ?? 5
      if (opts.at === undefined && !opts.transcode && back > 0 && start > 30 && (!b.last_played || Date.now() - b.last_played > 10 * 60_000)) start -= back
      if (start >= info.duration - 1) start = 0
      setTime(start)
      loadTrackAt(start, true)
    } catch (e) {
      setLoading(false)
      toast((e as Error).message, 'err')
    }
  }, [audio, report, applyMediaSession, loadTrackAt, toast, user]) // eslint-disable-line react-hooks/exhaustive-deps

  const seekTo = useCallback((t: number) => {
    const c = cur.current
    if (!c) {
      // a restored book that hasn't opened a session yet
      if (pending.current) { const b = pending.current; setTime(Math.max(0, Math.min(t, b.duration))); pending.current = { ...b, current_time: t } }
      return
    }
    t = Math.max(0, Math.min(t, c.duration - 0.5))
    const tr = c.tracks[trackIdx.current]
    if (c.method === 'direct' && tr && (t < tr.start || t >= tr.start + tr.duration)) {
      loadTrackAt(t, !audio.paused)
    } else {
      audio.currentTime = t - (tr?.start || 0)
    }
    lastT.current = t - (c.tracks[trackIdx.current]?.start || 0)
    setTime(t)
  }, [audio, loadTrackAt])

  const pause = useCallback(() => audio.pause(), [audio])
  const toggle = useCallback(() => {
    if (!cur.current && pending.current) { const b = pending.current; play(b, { at: b.current_time }); return }
    if (!cur.current) return
    if (audio.paused) {
      // a long pause: a few seconds back
      const back = settingsRef.current?.rewind_on_resume ?? 5
      if (back > 0 && pausedAt.current && Date.now() - pausedAt.current > 5 * 60_000) seekTo(globalTime() - back)
      audio.play().catch(() => {})
    } else audio.pause()
  }, [audio, play, seekTo, globalTime])
  const skip = useCallback((d: number) => seekTo((cur.current ? globalTime() : time) + d), [seekTo, globalTime, time])
  const setRate = useCallback((r: number) => {
    r = Math.round(r * 100) / 100
    rateRef.current = r
    audio.playbackRate = r
    setRateState(r)
    const id = cur.current?.book.id || pending.current?.id
    if (id) localStorage.setItem(rateKey(id), String(r))
  }, [audio])

  const chapterIndex = useMemo(() => {
    let idx = -1
    chapters.forEach((c, i) => { if (c.start <= time + 0.25) idx = i })
    return idx
  }, [chapters, time])
  const chapter = chapterIndex >= 0 ? chapters[chapterIndex] : null
  const chapterStep = useCallback((dir: number) => {
    if (!chapters.length) { skip(dir * 300); return }
    const t = cur.current ? globalTime() : time
    let i = chapterIndex
    // "previous" within the first seconds of a chapter goes to the one before
    if (dir < 0 && i >= 0 && t - chapters[i].start > 3) { seekTo(chapters[i].start); return }
    i = Math.max(0, Math.min(chapters.length - 1, i + dir))
    seekTo(chapters[i].start)
  }, [chapters, chapterIndex, seekTo, skip, globalTime, time])

  const stop = useCallback(() => {
    if (cur.current) report({ close: true, force: true })
    audio.pause(); hls.current?.destroy(); hls.current = null; audio.removeAttribute('src'); audio.load()
    cur.current = null; pending.current = null
    setBook(null); setPlaying(false); setChapters([]); applyMediaSession(null); localStorage.removeItem(lastKey)
  }, [audio, report, applyMediaSession])

  const setSleep = useCallback((m: number | null | 'chapter') => {
    if (sleepTimer.current) { clearTimeout(sleepTimer.current); sleepTimer.current = null }
    sleepChapterEnd.current = null
    setSleepChapter(false)
    if (!m) { setSleepAt(null); return }
    if (m === 'chapter') {
      const end = chapter ? chapter.end : null
      if (!end) { toast('This book has no chapters', 'err'); setSleepAt(null); return }
      sleepChapterEnd.current = end
      setSleepChapter(true)
      setSleepAt(Date.now() + ((end - time) / (rateRef.current || 1)) * 1000)
      return
    }
    setSleepAt(Date.now() + m * 60000)
    sleepTimer.current = window.setTimeout(() => { audio.pause(); setSleepAt(null); sleepTimer.current = null; toast('Sleep timer: paused') }, m * 60000)
  }, [audio, chapter, time, toast])

  const bookmark = useCallback(async () => {
    const c = cur.current
    const b = c?.book || pending.current
    if (!b) return
    const t = c ? globalTime() : time
    try {
      await api.post(`${V1}/books/${encodeURIComponent(b.id)}/bookmarks`, { time: Math.floor(t), title: chapter ? `${chapter.title} · ${clock(t)}` : '' })
      toast(`Bookmarked ${clock(t)}`)
      window.dispatchEvent(new CustomEvent('pab:bookmarks', { detail: { id: b.id } }))
    } catch (e) { toast((e as Error).message, 'err') }
  }, [globalTime, time, chapter, toast])

  const next = useCallback(async () => {
    const c = cur.current
    if (!c || !(settingsRef.current?.play_next ?? true)) return
    try {
      const r = await api.get<{ book: Book | null; why?: string }>(`${V1}/next?after=${encodeURIComponent(c.book.id)}`)
      if (r.book) { toast(`Up next: ${r.book.title}${r.why ? ` (${r.why})` : ''}`); play(r.book, { at: r.book.finished ? 0 : undefined }) }
    } catch { /* ignore */ }
  }, [play, toast])

  // ---- audio events ----
  useEffect(() => {
    const onPlay = () => { setPlaying(true); lastT.current = audio.currentTime; pausedAt.current = 0 }
    const onPlaying = () => setLoading(false)
    const onPause = () => {
      setPlaying(false)
      pausedAt.current = Date.now()
      if (cur.current && !audio.ended) report({ force: true })
    }
    const onWaiting = () => setLoading(true)
    const onEnded = async () => {
      const c = cur.current
      if (!c) return
      if (trackIdx.current < c.tracks.length - 1) {
        // the next file of the same book
        const nextTr = c.tracks[trackIdx.current + 1]
        trackIdx.current++
        attach(nextTr, 0, true)
        return
      }
      setPlaying(false)
      await report({ ended: true, force: true })
      cur.current = null
      pending.current = { ...c.book, current_time: 0, finished: true } // play again from the start
      setTime(c.duration)
      localStorage.removeItem(lastKey)
      toast(`Finished “${c.book.title}”`)
      next()
    }
    const onTime = () => {
      const c = cur.current
      if (!c) return
      const t = audio.currentTime
      if (!audio.paused) {
        const d = t - lastT.current
        if (d > 0 && d < 5) listened.current += d / (audio.playbackRate || 1) // seconds of the person's time
      }
      lastT.current = t
      const g = (c.tracks[trackIdx.current]?.start || 0) + t
      setTime(g)
      if (sleepChapterEnd.current !== null && g >= sleepChapterEnd.current - 0.3) {
        sleepChapterEnd.current = null; setSleepChapter(false); setSleepAt(null); audio.pause(); toast('Sleep timer: end of the chapter')
      }
      if (Date.now() - lastReport.current > REPORT_EVERY * 1000) report()
    }
    const onError = () => {
      setLoading(false)
      const c = cur.current
      if (!c || !audio.src) return
      const code = audio.error?.code
      // the browser said it plays this type but can't decode the file: Audiobookshelf converts it
      if (c.method === 'direct' && (code === 3 || code === 4)) {
        markUndecodable(c.tracks[trackIdx.current]?.mime || '')
        const at = audio.currentTime > 0 ? globalTime() : wantAt.current
        toast('Converting this file for your browser')
        play(c.book, { at, transcode: true })
        return
      }
      toast('Playback error: the file could not be played here', 'err')
    }
    audio.addEventListener('play', onPlay); audio.addEventListener('playing', onPlaying); audio.addEventListener('pause', onPause); audio.addEventListener('waiting', onWaiting)
    audio.addEventListener('ended', onEnded); audio.addEventListener('timeupdate', onTime); audio.addEventListener('error', onError)
    return () => {
      audio.removeEventListener('play', onPlay); audio.removeEventListener('playing', onPlaying); audio.removeEventListener('pause', onPause); audio.removeEventListener('waiting', onWaiting)
      audio.removeEventListener('ended', onEnded); audio.removeEventListener('timeupdate', onTime); audio.removeEventListener('error', onError)
    }
  }, [audio, report, attach, next, toast, play, globalTime])

  // ---- lock screen / keyboard ----
  useEffect(() => {
    if (!('mediaSession' in navigator)) return
    const ms = navigator.mediaSession
    const back = settings?.skip_back || 10, fwd = settings?.skip_forward || 30
    try {
      ms.setActionHandler('play', () => toggle())
      ms.setActionHandler('pause', () => audio.pause())
      ms.setActionHandler('seekbackward', d => skip(-(d.seekOffset || back)))
      ms.setActionHandler('seekforward', d => skip(d.seekOffset || fwd))
      ms.setActionHandler('seekto', d => { if (d.seekTime !== undefined) seekTo(d.seekTime) })
      ms.setActionHandler('previoustrack', () => chapterStep(-1))
      ms.setActionHandler('nexttrack', () => chapterStep(1))
    } catch { /* older browsers */ }
  }, [audio, settings?.skip_back, settings?.skip_forward, skip, seekTo, toggle, chapterStep])
  useEffect(() => {
    if (!('mediaSession' in navigator) || !book || !duration) return
    try { navigator.mediaSession.setPositionState({ duration, playbackRate: rate, position: Math.min(time, duration) }) } catch { /* ignore */ }
    try { navigator.mediaSession.playbackState = playing ? 'playing' : 'paused' } catch { /* ignore */ }
  }, [book, duration, time, rate, playing])
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      const t = ev.target as HTMLElement
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return
      if (ev.metaKey || ev.ctrlKey || ev.altKey) return
      if (!cur.current && !pending.current) return
      if (document.querySelector('.modal-backdrop, .palette-backdrop')) return
      const st = settingsRef.current
      if (ev.code === 'Space' || ev.key === 'k') { ev.preventDefault(); toggle() }
      else if (ev.key === 'ArrowRight') { ev.preventDefault(); if (ev.shiftKey) chapterStep(1); else skip(st?.skip_forward || 30) }
      else if (ev.key === 'ArrowLeft') { ev.preventDefault(); if (ev.shiftKey) chapterStep(-1); else skip(-(st?.skip_back || 10)) }
      else if (ev.key === '>') setRate(Math.min(3.5, (rateRef.current || 1) + 0.1))
      else if (ev.key === '<') setRate(Math.max(0.5, (rateRef.current || 1) - 0.1))
      else if (ev.key === 'b') { ev.preventDefault(); bookmark() }
      else if (ev.key === 'p') { ev.preventDefault(); setExpanded(e => !e) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [toggle, skip, chapterStep, setRate, bookmark])

  // report before the tab goes away
  useEffect(() => {
    const beacon = () => {
      const c = cur.current
      if (!c) return
      const payload = { report_id: crypto.randomUUID(), observed_at: observedAt(), current_time: globalTime(), listened: listened.current, duration: c.duration, playing: false, close: true, item_id: c.book.id, client: CLIENT_ID }
      if (c.native) queueReport(c.owner, c.session, payload)
      const body = JSON.stringify(payload)
      listened.current = 0
      if (c.owner === owner.current) navigator.sendBeacon?.(`${V1}/sessions/${encodeURIComponent(c.session)}/sync`, new Blob([body], { type: 'application/json' }))
    }
    const vis = () => { if (document.visibilityState === 'hidden') report({ force: true }) }
    window.addEventListener('pagehide', beacon)
    document.addEventListener('visibilitychange', vis)
    return () => { window.removeEventListener('pagehide', beacon); document.removeEventListener('visibilitychange', vis) }
  }, [report, globalTime, user])

  useEffect(() => {
    if (!user) return
    let active = true
    const flush = () => { void flushReports(user.id, () => active && owner.current === user.id).catch(() => {}) }
    flush()
    const timer = window.setInterval(flush, 15000)
    window.addEventListener('online', flush)
    return () => { active = false; clearInterval(timer); window.removeEventListener('online', flush) }
  }, [user?.id])

  // Changing identity clears all audible/private player state before restoring
  // the new account's own last-book key. Native pending receipts remain scoped
  // to their original owner and can be delivered when that owner signs in again.
  const previousOwner = useRef(user?.id)
  useEffect(() => {
    if (previousOwner.current === user?.id) return
    const c = cur.current
    if (c?.native) queueReport(c.owner, c.session, { report_id: crypto.randomUUID(), observed_at: observedAt(), current_time: globalTime(), listened: listened.current, duration: c.duration, playing: false, close: true, item_id: c.book.id, client: CLIENT_ID })
    previousOwner.current = user?.id
    cur.current = null; pending.current = null; listened.current = 0
    audio.pause(); hls.current?.destroy(); hls.current = null; audio.removeAttribute('src'); audio.load()
    setBook(null); setPlaying(false); setLoading(false); setChapters([]); setTime(0); setDuration(0); applyMediaSession(null)
  }, [user?.id, audio, globalTime, applyMediaSession])

  // restore the last book (paused) after a reload; the session opens on play
  useEffect(() => {
    const id = localStorage.getItem(lastKey)
    if (!user || !id || cur.current) return
    api.get<{ book: Book }>(`${V1}/books/${encodeURIComponent(id)}`).then(({ book: b }) => {
      if (owner.current !== user.id) return
      if (cur.current || b.finished) { if (b.finished) localStorage.removeItem(lastKey); return }
      pending.current = b
      setBook(b); setDuration(b.duration); setTime(b.current_time || 0); setChapters(b.chapter_list || [])
      const r = Number(localStorage.getItem(rateKey(b.id))) || settingsRef.current?.speed || 1
      rateRef.current = r; setRateState(r)
      applyMediaSession(b)
    }).catch(() => localStorage.removeItem(lastKey))
  }, [applyMediaSession, user?.id, lastKey])
  useEffect(() => { if (!cur.current && !pending.current && settings?.speed) { rateRef.current = settings.speed; setRateState(settings.speed) } }, [settings?.speed])

  // what plays elsewhere: another browser, or an Audiobookshelf app on the phone
  const [remote, setRemote] = useState<NowPlaying | null>(null)
  const [clockOffset, setClockOffset] = useState(0)
  useEffect(() => {
    let t: number | undefined, live = true
    const poll = async () => {
      if (document.visibilityState === 'visible') {
        try {
          const sid = cur.current?.session || ''
          const r = await api.get<NowPlaying>(`${V1}/now-playing?client=${CLIENT_ID}&session=${encodeURIComponent(sid)}`)
          if (!live) return
          setClockOffset(r.server_time - Date.now())
          setRemote(r.active ? r : null)
        } catch { /* ignore */ }
      }
      if (live) t = window.setTimeout(poll, 15000)
    }
    poll()
    const vis = () => { if (document.visibilityState === 'visible') { clearTimeout(t); poll() } }
    document.addEventListener('visibilitychange', vis)
    return () => { live = false; clearTimeout(t); document.removeEventListener('visibilitychange', vis) }
  }, [])
  // another device wins while it plays and this tab does not; a paused one shows only when nothing is loaded here
  const remoteVisible = !!remote && !playing && (!!remote.playing || !book)

  const value: PlayerState = {
    book, playing, loading, time, duration: duration || book?.duration || 0, rate, chapters, chapter, chapterIndex, sleepAt, sleepChapter, expanded,
    play, toggle, pause, seek: seekTo, skip, chapterStep, setRate, setSleep, stop, bookmark, setExpanded,
    isCurrent: id => (cur.current?.book.id || pending.current?.id) === id,
    remote, remoteVisible, clockOffset,
  }
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}
