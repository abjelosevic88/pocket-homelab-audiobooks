import { NativeLibrarySettings } from '../components/NativeLibrarySettings'
import { AskButton } from '../components/AskButton'
import React, { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { APIToken, Connection, Settings as SettingsT, User } from '../lib/types'
import { usePlayer } from '../lib/player'
import { Player } from '../components/Player'
import { DATE_FORMATS, TIME_FORMATS, fmtBytes, fmtDateTime, type DateFormat, type TimeFormat } from '../lib/format'
import { Avatar, Confirm, CoreHint, Field, Loading, Modal, Segmented, SettingRow, Spinner, Switch, Tabs, errMsg, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { ShortcutsSheet } from '../components/ShortcutsSheet'

type TabId = 'audiobookshelf' | 'playback' | 'notifications' | 'profile' | 'people' | 'tokens' | 'data' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string; admin?: boolean }[] = [
  { id: 'audiobookshelf', label: 'Audiobookshelf', icon: 'server', sub: 'Your Audiobookshelf account: Pocket Audiobooks plays with it, so your place, finished books and bookmarks stay yours everywhere.' },
  { id: 'playback', label: 'Playback & goals', icon: 'play-circle', sub: 'Speed, skipping, what plays after a book, and a daily listening goal.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'A push when you finish a book, and a weekly recap.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'people', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box.' },
  { id: 'tokens', label: 'Apps & API', icon: 'key', sub: 'Audiobookshelf apps on your phone or in the car, Pocket Homelab, Homepage widgets and shortcuts.' },
  { id: 'data', label: 'Backup & data', icon: 'database', sub: 'Download the database, export your history or import it.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, numbers and the server.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const { user } = useApp()
  const [native, setNative] = useState(false)
  useEffect(() => { api.get<Connection>(`${V1}/connection`).then(c => setNative(c.backend === 'native')).catch(() => {}) }, [])
  const tabs = TABS.filter(t => !t.admin || user?.role === 'admin').map(t => native && t.id === 'audiobookshelf' ? { ...t, label: 'Libraries', sub: 'Manage your local books and library scans.' } : t)
  const tab: TabId = tabs.find(t => t.id === raw)?.id || 'audiobookshelf'
  const current = tabs.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })
  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])
  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back to Pocket Audiobooks" title="Back (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={tabs.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {tabs.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title"><h2>{current.label}</h2><p>{current.sub}</p></div>
            {tab === 'audiobookshelf' && (native ? <NativeLibrarySettings /> : <ABSAccount />)}
            {tab === 'playback' && <Playback />}
            {tab === 'notifications' && <Notifications />}
            {tab === 'profile' && <Profile />}
            {tab === 'people' && <Users />}
            {tab === 'tokens' && <Tokens />}
            {tab === 'data' && <Data native={native} />}
            {tab === 'about' && <About />}
          </div>
        </main>
      </div>
      <Player />
    </div>
  )
}

function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function useSettingsForm() {
  const { settings, setSettings, user } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState<SettingsT | null>(settings)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!f && settings) setF(settings) }, [settings]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async (patch?: Partial<SettingsT>) => {
    if (!f) return
    setBusy(true)
    try { const s = await api.put<SettingsT>(`${V1}/settings`, { ...f, ...patch }); setSettings(s); setF(s); toast('Saved') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return { f, setF, save, busy, isAdmin: user?.role === 'admin' }
}

function useTester() {
  const [test, setTest] = useState<Record<string, string>>({})
  const run = async (which: string, url: string, key: string, extra: Record<string, string> = {}) => {
    setTest(t => ({ ...t, [which]: 'testing…' }))
    try { const r = await api.post<{ ok: boolean; message?: string; error?: string }>(`${V1}/integrations/test`, { which, url, key, ...extra }); setTest(t => ({ ...t, [which]: r.ok ? `✓ ${r.message}` : `✕ ${r.error}` })) } catch (e) { setTest(t => ({ ...t, [which]: `✕ ${errMsg(e)}` })) }
  }
  const status = (which: string) => <span className={`small ${test[which]?.startsWith('✓') ? 'ok-text' : test[which]?.startsWith('✕') ? 'danger-text' : 'muted'}`}>{test[which]}</span>
  return { run, status }
}

// ---------- Audiobookshelf ----------

function ABSAccount() {
  const { toast } = useToast()
  const { settings, setSettings } = useApp()
  const [c, setC] = useState<Connection | null>(null)
  const [mode, setMode] = useState<'core' | 'key' | 'direct' | 'off'>('key')
  const [form, setForm] = useState({ url: '', key: '', username: '', password: '' })
  const [busy, setBusy] = useState(false)
  const load = () => api.get<Connection>(`${V1}/connection`).then(x => { setC(x); setMode(x.mode); setForm(f => ({ ...f, url: x.custom_url || '', username: x.username || x.account || '' })) }).catch(e => toast(errMsg(e), 'err'))
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  if (!c) return <Loading />
  const save = async (m = mode) => {
    setBusy(true)
    try { const x = await api.put<Connection>(`${V1}/connection`, { mode: m, ...form }); setC(x); setMode(x.mode); setForm(f => ({ ...f, key: '', password: '' })); toast(x.connected ? 'Connected — reading what you listened to' : x.message || 'Saved', x.connected ? 'ok' : 'err') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const s = c.sync
  const hidden = new Set((settings?.hidden_libraries || '').split(',').filter(Boolean))
  const toggleLib = async (id: string, show: boolean) => {
    if (show) hidden.delete(id); else hidden.add(id)
    try { setSettings(await api.put<SettingsT>(`${V1}/settings`, { hidden_libraries: [...hidden].join(',') })); toast(show ? 'Shown' : 'Hidden') } catch (e) { toast(errMsg(e), 'err') }
  }
  const viaText = c.via === 'core' ? ' — the login Pocket Core keeps for you (Pocket Core → Settings → My logins)' : c.via === 'direct' ? ' — signed in with your password (only Audiobookshelf’s refresh token is kept)' : c.via === 'key' ? ' — with an API key' : ''
  return (
    <div className="stack loose">
      <Section title="Your Audiobookshelf account" icon="server" actions={<span className={`badge ${c.connected ? 'ok' : 'warn'}`}>{c.connected ? `connected${c.via === 'core' ? ' · Pocket Core' : ''}` : 'not connected'}</span>}
        sub="Pocket Audiobooks listens with your own account, so your place, finished books and bookmarks stay yours, and the Audiobookshelf apps on your phone pick up where the web left off (and the other way round).">
        <div className="conn-block">
          <div className="conn-mode">
            <Segmented size="sm" value={mode === 'direct' ? 'key' : mode} onChange={m => { setMode(m); if (m === 'core' || m === 'off') save(m) }} options={[
              { value: 'core', label: 'Pocket Core', title: c.core ? 'The Audiobookshelf login Pocket Core holds for you' : 'Needs Pocket Core', disabled: !c.core_configured },
              { value: 'key', label: 'My account' }, { value: 'off', label: 'Off' }]} />
          </div>
          {!c.core_configured && <CoreHint />}
          <div className={`conn-status ${mode === 'off' ? '' : c.connected ? 'ok' : 'bad'}`}><span className="dot" />
            {c.connected ? <>Signed in as <b>{c.account}</b>{c.admin ? ' (administrator)' : ''} on Audiobookshelf {c.server_version}{viaText}.</> : c.message}
          </div>
          {c.connected && <div className="set-stats">
            <div className="set-stat"><Icon name="book" size={16} /><span className="set-stat-n">{(c.libraries || []).reduce((a, l) => a + l.count, 0).toLocaleString()}</span><span className="set-stat-l">Books</span></div>
            <div className="set-stat"><Icon name="headphones" size={16} /><span className="set-stat-n">{(c.in_progress || 0).toLocaleString()}</span><span className="set-stat-l">In progress</span></div>
            <div className="set-stat"><Icon name="layers" size={16} /><span className="set-stat-n">{(c.libraries || []).length}</span><span className="set-stat-l">Libraries</span></div>
          </div>}
          {(mode === 'key' || mode === 'direct') && (
            <div className="stack">
              {!c.server_url && <Field label="Audiobookshelf address" help="e.g. http://audiobookshelf:80"><input value={form.url} onChange={e => setForm({ ...form, url: e.target.value })} placeholder="https://audiobooks.example.com" /></Field>}
              {c.server_url && <p className="muted small">Audiobookshelf is set on this box{settings?.abs_public_url ? ` (${settings.abs_public_url})` : ''}. <button type="button" className="link-btn" onClick={() => setForm({ ...form, url: form.url || 'https://' })}>Use another server</button></p>}
              {c.server_url && form.url && <Field label="Other Audiobookshelf address"><input value={form.url} onChange={e => setForm({ ...form, url: e.target.value })} /></Field>}
              <Segmented size="sm" value={mode} onChange={setMode} options={[{ value: 'key', label: 'API key' }, { value: 'direct', label: 'Password', disabled: c.password_login === false, title: c.password_login === false ? 'This Audiobookshelf only signs in with single sign-on: use an API key' : undefined }]} />
              {mode === 'key' ? (
                <form className="stack" onSubmit={e => { e.preventDefault(); save('key') }}>
                  <Field label="API key" help="In Audiobookshelf an administrator makes one under Settings → API Keys, for your account. It is sealed on this server and never shown again.">
                    <input value={form.key} onChange={e => setForm({ ...form, key: e.target.value })} autoComplete="off" placeholder={c.connected && c.via === 'key' ? '•••••• (saved)' : 'eyJhbGciOi…'} />
                  </Field>
                  <div className="row"><button className="btn primary" disabled={busy || !form.key.trim()}>{busy ? <Spinner /> : null}{c.connected && c.via === 'key' ? 'Replace the key' : 'Connect'}</button></div>
                </form>
              ) : (
                <form className="stack" onSubmit={e => { e.preventDefault(); save('direct') }}>
                  <div className="two-col">
                    <Field label="Username"><input value={form.username} onChange={e => setForm({ ...form, username: e.target.value })} autoComplete="username" /></Field>
                    <Field label="Password" help="Used once to sign in; only Audiobookshelf’s refresh token is kept."><input type="password" value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} autoComplete="current-password" /></Field>
                  </div>
                  <div className="row"><button className="btn primary" disabled={busy}>{busy ? <Spinner /> : null}{c.connected && c.via === 'direct' ? 'Sign in again' : 'Sign in'}</button></div>
                </form>
              )}
            </div>
          )}
          {c.connected && c.public_url && <div className="row"><a className="btn sm" href={c.public_url} target="_blank" rel="noreferrer"><Icon name="external" size={13} />Open Audiobookshelf</a></div>}
        </div>
      </Section>
      {c.connected && c.libraries && c.libraries.length > 0 && (
        <Section title="Libraries" icon="library" sub="Book libraries you can open. Hide the ones you don't want on Home and in the library (podcast libraries belong to Pocket Podcasts).">
          <div className="stack">
            {c.libraries.map(l => (
              <SettingRow key={l.id} title={l.name} help={`${l.count.toLocaleString()} books`}><Switch checked={!hidden.has(l.id)} onChange={v => toggleLib(l.id, v)} label={`Show ${l.name}`} /></SettingRow>
            ))}
          </div>
        </Section>
      )}
      {c.connected && (
        <Section title="Your history from Audiobookshelf" icon="history" sub="Every listening session in every Audiobookshelf app (and this one) is copied here every few minutes; the first time, everything you ever listened to.">
          <p className="muted small">{s.synced_at ? `Last read ${fmtDateTime(s.synced_at)}.` : 'Not read yet.'}{s.error ? <span className="danger-text"> {s.error}</span> : null}{c.syncing ? ' Reading now…' : ''}</p>
          <div className="row wrap mt-s">
            <button type="button" className="btn sm" onClick={() => api.post(`${V1}/sync`).then(() => toast('Reading your history'))}><Icon name="refresh" size={14} />Read now</button>
            <button type="button" className="btn sm ghost" onClick={() => api.post(`${V1}/sync?full=1`).then(() => toast('Reading everything again'))}>Read everything again</button>
          </div>
        </Section>
      )}
    </div>
  )
}

// ---------- playback ----------

function Playback() {
  const { f, setF, save, busy } = useSettingsForm()
  const p = usePlayer()
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Playing" icon="headphones">
        <SettingRow title="Speed" help="For books you haven't set a speed for; the player remembers a change per book.">
          <Select ariaLabel="Speed" value={String(f.speed)} onChange={v => setF({ ...f, speed: Number(v) })} options={[0.8, 0.9, 1, 1.1, 1.2, 1.25, 1.3, 1.4, 1.5, 1.6, 1.75, 2, 2.25, 2.5, 3].map(r => ({ value: String(r), label: `${r}×` }))} />
        </SettingRow>
        <SettingRow title="Skip back"><Segmented size="sm" value={f.skip_back} onChange={v => setF({ ...f, skip_back: v })} options={[5, 10, 15, 30].map(n => ({ value: n, label: `${n} s` }))} /></SettingRow>
        <SettingRow title="Skip forward"><Segmented size="sm" value={f.skip_forward} onChange={v => setF({ ...f, skip_forward: v })} options={[10, 15, 30, 45, 60].map(n => ({ value: n, label: `${n} s` }))} /></SettingRow>
        <SettingRow title="Go back after a pause" help="After five minutes or more away (and when you come back to a book), start a few seconds earlier to pick up the thread.">
          <Segmented size="sm" value={f.rewind_on_resume} onChange={v => setF({ ...f, rewind_on_resume: v })} options={[{ value: 0, label: 'Off' }, { value: 3, label: '3 s' }, { value: 5, label: '5 s' }, { value: 10, label: '10 s' }, { value: 30, label: '30 s' }]} />
        </SettingRow>
        <SettingRow title="When a book ends" help="Go on with the first book on Up next, or else the next book of its series."><Switch checked={f.play_next} onChange={v => setF({ ...f, play_next: v })} label="Play the next book" /></SettingRow>
      </Section>
      <Section title="Daily goal" icon="zap" sub="Minutes a day. Home shows how far you are today, and streaks count days that reach it.">
        <SettingRow title="Goal"><Segmented size="sm" value={f.daily_goal} onChange={v => setF({ ...f, daily_goal: v })} options={[{ value: 0, label: 'None' }, { value: 15, label: '15 min' }, { value: 30, label: '30 min' }, { value: 45, label: '45 min' }, { value: 60, label: '1 h' }, { value: 120, label: '2 h' }]} /></SettingRow>
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={async () => { await save(); if (!p.book) p.setRate(f.speed) }} disabled={busy}>Save changes</button></div>
    </div>
  )
}

// ---------- notifications ----------

function Notifications() {
  const { f, setF, save, busy } = useSettingsForm()
  const { run, status } = useTester()
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Push" icon="bell" actions={<span className={`badge ${f.notify_configured ? 'ok' : ''}`}>{f.notify_configured ? 'on' : 'not set'}</span>}
        sub={<>Apprise gateway (<code>…/notify/&lt;key&gt;</code>), an ntfy topic URL, or any webhook that takes JSON.</>}>
        <SettingRow title="URL" stack><input value={f.notify_url} onChange={e => setF({ ...f, notify_url: e.target.value })} placeholder="http://apprise:8000/notify/audiobooks" /></SettingRow>
        <SettingRow title="Format"><Segmented value={f.notify_format} onChange={v => setF({ ...f, notify_format: v })} options={[{ value: 'apprise', label: 'Apprise' }, { value: 'ntfy', label: 'ntfy' }, { value: 'json', label: 'JSON' }]} size="sm" /></SettingRow>
        <SettingRow title="A book is finished" help="Wherever you finished it: here or in an Audiobookshelf app."><Switch checked={f.notify_finished} onChange={v => setF({ ...f, notify_finished: v })} label="Push finished books" /></SettingRow>
        <SettingRow title="Weekly recap" help="Monday morning: how much you listened, on how many days, the book you heard most."><Switch checked={f.notify_weekly} onChange={v => setF({ ...f, notify_weekly: v })} label="Weekly recap" /></SettingRow>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button>
        <button type="button" className="btn" onClick={() => run('notify', f.notify_url, '', { format: f.notify_format })} disabled={!f.notify_url}><Icon name="mail" size={15} />Send a test</button>
        {status('notify')}
      </div>
      <Section title="Date & time">
        <SettingRow title="Date format"><Select value={f.date_format} onChange={v => setF({ ...f, date_format: v as DateFormat })} options={DATE_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
        <SettingRow title="Time format"><Select value={f.time_format} onChange={v => setF({ ...f, time_format: v as TimeFormat })} options={TIME_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
      </Section>
    </div>
  )
}

// ---------- people, profile, tokens, data, about ----------

function Users() {
  const { user, users, reloadMeta, status } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const isAdmin = user?.role === 'admin'
  return (
    <Section title="People on this box" actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit({ role: 'member' })}><Icon name="plus" size={14} />Add person</button>}
      sub="Everyone signs in with their own account: their own place in every book, history, bookmarks and Up next. Admins manage accounts; they don't see anyone else's listening.">
      <div className="set-people">
        {users.map(u => (
          <div key={u.id} className="set-person">
            <Avatar name={u.name} email={u.email} size={34} />
            <div className="grow">
              <div className="set-person-name">{u.name || <span className="muted">—</span>}{u.id === user?.id && <span className="badge">you</span>}{u.role === 'admin' && <span className="badge accent">admin</span>}</div>
              <div className="muted small ellipsis">{u.email}</div>
            </div>
            {isAdmin && <span className="row" style={{ justifyContent: 'flex-end' }}><button type="button" className="btn ghost sm" onClick={() => setEdit(u)}>Edit</button>{u.id !== user?.id && <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(u)}>Remove</button>}</span>}
          </div>
        ))}
      </div>
      {status?.auth_mode !== 'local' && <p className="muted small mt">Auth mode is <code>{status?.auth_mode}</code>: users are created automatically on first visit.</p>}
      {edit && <UserModal user={edit} local={status?.auth_mode === 'local'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadMeta() }} />}
      {del && <Confirm confirmLabel="Remove" title={`Remove ${del.name || del.email}?`} message="Their account is deleted here with their listening history and Up next. Shared library books stay." onCancel={() => setDel(null)}
        onConfirm={async () => { const d = del; setDel(null); try { await api.del(`${V1}/users/${d.id}`); await reloadMeta(); toast('Removed') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Section>
  )
}

function UserModal({ user, local, onClose, onSaved }: { user: Partial<User>; local: boolean; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: user.name || '', email: user.email || '', password: '', role: user.role || 'member' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { if (user.id) await api.put(`${V1}/users/${user.id}`, f); else await api.post(`${V1}/users`, f); toast(user.id ? 'Saved' : 'Added'); onSaved() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={user.id ? 'Edit person' : 'Add a person'} onClose={onClose} size="sm">
      <form onSubmit={save} className="grid" style={{ gap: 14 }}>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus required maxLength={60} /></Field>
        <Field label="Email" help="Used to sign in."><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} required /></Field>
        {local && <Field label={user.id ? 'New password (leave empty to keep)' : 'Password'} help="At least 8 characters."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} minLength={user.id ? 0 : 8} required={!user.id} autoComplete="new-password" /></Field>}
        <Field group label="Role"><Segmented value={f.role} onChange={v => setF({ ...f, role: v })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>{user.id ? 'Save' : 'Add person'}</button></div>
      </form>
    </Modal>
  )
}

function Profile() {
  const { user, setUser, status } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '', current_password: '', new_password: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { setUser(await api.put(`${V1}/auth/me`, f)); setF(x => ({ ...x, current_password: '', new_password: '' })); toast('Profile saved') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack loose">
      <div className="set-profile">
        <Avatar name={user?.name} email={user?.email} size={56} />
        <div className="grow"><h3>{user?.name || user?.email}</h3><p className="muted small">{user?.email} · {user?.role}</p></div>
      </div>
      <Section title="You"><div className="stack">
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
      </div></Section>
      {status?.auth_mode === 'local' && <Section title="Password" sub="Leave empty to keep the current one."><div className="stack">
        <Field label="Current password"><input type="password" value={f.current_password} onChange={e => setF({ ...f, current_password: e.target.value })} autoComplete="current-password" /></Field>
        <Field label="New password" help="At least 8 characters."><input type="password" value={f.new_password} onChange={e => setF({ ...f, new_password: e.target.value })} autoComplete="new-password" /></Field>
      </div></Section>}
      <div className="row"><button className="btn primary" disabled={busy}>Save changes</button></div>
    </form>
  )
}

function Tokens() {
  const { toast } = useToast()
  const { settings } = useApp()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState('')
  const [del, setDel] = useState<APIToken | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: name || 'token' }); setFresh(r.token); setName(''); reload() } catch (e) { toast(errMsg(e), 'err') }
  }
  const base = settings?.base_url || window.location.origin
  const { user } = useApp()
  const [appPw, setAppPw] = useState('')
  const makeAppPassword = async () => {
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: 'Audiobookshelf app' }); setAppPw(r.token); reload() } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <div className="stack loose">
      <Section title="Audiobookshelf apps" icon="headphones" sub="The Audiobookshelf app, AudioBooth, Plappa, ShelfPlayer and other Audiobookshelf players can use Pocket Audiobooks as their server: the same books, your place, bookmarks and history, using your own Pocket Audiobooks account.">
        <dl className="set-dl">
          <div><dt>Server address</dt><dd><code>{base}</code> <span className="muted small">(or <code>{base}/audiobookshelf</code> if the app wants a path)</span></dd></div>
          <div><dt>Username</dt><dd><code>{user?.email}</code></dd></div>
          <div><dt>Password</dt><dd>an app password — one per app, so you can revoke it alone below</dd></div>
        </dl>
        {appPw ? <div className="set-fresh mt"><div className="row"><Icon name="check-circle" size={16} /><b>Your app password — paste it into the app now, it is shown once.</b></div><pre className="mono">{appPw}</pre></div>
          : <div className="row mt"><button type="button" className="btn primary" onClick={makeAppPassword}><Icon name="key" size={15} />Create an app password</button></div>}
        <p className="muted small mt">Sign in with username and password (not OpenID). Your Pocket Audiobooks password works too where you have one; the app then gets its own token.</p>
      </Section>
      <Section title="Connect Pocket Homelab" icon="smartphone" sub="Listen in the app with the same library and saved progress.">
        <p>With Pocket Core, select Pocket Audiobooks for your device and refresh its configuration. Your paired device token handles sign-in automatically.</p><p>For a manual connection, set the server to <code>{base}</code> in the app’s Audiobookshelf settings and use a personal token created below.</p>
        <p className="muted">If you paired through Pocket Core, choose Pocket Audiobooks under Devices → Apps on your devices, then refresh the app’s Core configuration. Core supplies the address and your device token.</p>
      </Section>
      <Section title="Personal tokens" icon="key" sub={<>Use as <code>Authorization: Bearer &lt;token&gt;</code>. A token acts as you. See <code>docs/api.md</code>.</>}>
        <form onSubmit={create} className="row mb"><input value={name} onChange={e => setName(e.target.value)} placeholder="Token name (e.g. Homepage widget)" /><button className="btn primary"><Icon name="plus" size={15} />Create</button></form>
        {fresh && <div className="set-fresh mb"><div className="row"><Icon name="check-circle" size={16} /><b>Copy it now — it is shown once.</b></div><pre className="mono">{fresh}</pre></div>}
        {data && data.length > 0 ? (
          <table className="table compact">
            <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th>Created</th><th /></tr></thead>
            <tbody>{data.map(t => <tr key={t.id}><td>{t.name}</td><td className="mono">{t.prefix}…</td><td className="muted">{t.last_used_at ? fmtDateTime(t.last_used_at) : 'never'}</td><td className="muted">{fmtDateTime(t.created_at)}</td><td className="right"><button type="button" className="btn ghost sm danger-text" onClick={() => setDel(t)}>Revoke</button></td></tr>)}</tbody>
          </table>
        ) : <p className="muted">No tokens yet.</p>}
        {del && <Confirm confirmLabel="Revoke" title={`Revoke “${del.name}”?`} message="Anything using this token stops working immediately." onCancel={() => setDel(null)} onConfirm={async () => { await api.del(`${V1}/tokens/${del.id}`); setDel(null); reload() }} />}
      </Section>
      <Section title="Handy endpoints" icon="smartphone" sub={<>At <code>{base}/api/v1</code>.</>}>
        <dl className="set-dl">
          <div><dt><code>GET /dashboard</code></dt><dd className="muted">Now playing, time listened today and this week, the current book, books finished this year — for a Homepage widget.</dd></div>
          <div><dt><code>GET /day/2026-10-24</code></dt><dd className="muted">One day's listening in a sentence and a list (Pocket Day uses it).</dd></div>
          <div><dt><code>GET /history</code> · <code>GET /finished</code> · <code>GET /stats?range=30d</code></dt><dd className="muted">For reading logs, dashboards and scripts.</dd></div>
        </dl>
      </Section>
    </div>
  )
}

function Data({native}: {native: boolean}) {
  const { toast } = useToast()
  const { user } = useApp()
  const [busy, setBusy] = useState(false)
  const importJSON = async (file: File) => {
    setBusy(true)
    try { const r = await api.post<Record<string, number>>(`${V1}/import`, JSON.parse(await file.text())); toast('Imported: ' + (Object.entries(r).map(([k, v]) => `${v} ${k.replace('_', ' ')}`).join(', ') || 'nothing new')) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <div className="stack loose">
      {user?.role === 'admin' && <Section title="Application backup" icon="database" sub="All accounts — admins only.">
        <p className="muted">Save the database, encryption key, Core connection and managed artwork together. The bundle includes checksums and can be restored into a new installation.</p>
        <p className="muted">{native ? 'Your places, bookmarks, library metadata and reading history are in this database. Audio, ebooks, imported artwork and Trash in external library folders need their own media backup.' : 'Books, places and bookmarks are still held by Audiobookshelf; back up that server too.'}</p>
        <div className="row wrap"><a className="btn primary" href={`${V1}/backup?format=bundle`}><Icon name="download" size={15} />Download application backup</a><a className="btn" href={`${V1}/backup`}>Database only</a></div>
        <p className="small muted">Application backups contain credentials. Keep them private. Restore with the server command into a new data directory.</p>
      </Section>}
      <Section title="Your history" icon="archive" sub="Everything you listened to, finished books and Up next as JSON, to keep or to move to another box.">
        <div className="row wrap">
          <a className="btn" href={`${V1}/export.json`}><Icon name="download" size={15} />Export JSON</a>
          <label className="btn"><Icon name="upload" size={15} />{busy ? 'Importing…' : 'Import JSON…'}<input type="file" accept="application/json,.json" hidden onChange={e => e.target.files?.[0] && importJSON(e.target.files[0])} /></label>
        </div>
      </Section>
    </div>
  )
}

function About() {
  const { data, error } = useAsync(() => api.get<Record<string, unknown>>(`${V1}/settings/system`), [])
  const [keys, setKeys] = useState(false)
  if (error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>
  if (!data) return <Loading />
  const counts = (data.counts || {}) as Record<string, number>
  const tiles: [string, number | undefined, string][] = [['Listening sessions', counts.listens, 'history'], ['Books finished', counts.finishes, 'check-circle'], ['Up next', counts.up_next, 'list-ordered']]
  const core = (data.core || {}) as { configured: boolean; registered: boolean; url: string; error: string }
  const rows: [string, React.ReactNode][] = [
    ['Version', <code key="v">{String(data.version)}</code>],
    ['Uptime', String(data.uptime)],
    ['Runtime', `${String(data.go)} · ${String(data.os)}/${String(data.arch)}`],
    ['Time zone', String(data.timezone)],
    ['Sign-in', String(data.auth_mode)],
    ['Library server', data.backend==='native'?'Pocket Audiobooks':(data.abs_url ? <code key="n">{String(data.abs_url)}</code> : 'Audiobookshelf')],
    ['Pocket Core', core.configured ? (core.registered ? <span key="c">registered at <code>{core.url}</code></span> : <span key="c" className="danger-text">not registered: {core.error}</span>) : 'not configured'],
    ['Database', <span key="db"><code>{String(data.db_path)}</code> · {fmtBytes(Number(data.db_bytes))}</span>],
    ['People', `${counts.users ?? 0} accounts on this box`],
  ]
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket Audiobooks</h3>
          <p className="muted small">Audiobooks for the whole household: your library, chapters, bookmarks and saved places, with a web player and connected apps. Browse by author or series, keep an Up next list and follow your listening history. One Go server and one SQLite database.</p>
        </div>
      </div>
      <div className="set-stats">
        {tiles.map(([label, n, icon]) => <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{(n ?? 0).toLocaleString()}</span><span className="set-stat-l">{label}</span></div>)}
      </div>
      <Section title="This server"><dl className="set-dl">{rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}</dl></Section>
      <div className="row wrap">
        <button type="button" className="btn" onClick={() => setKeys(true)}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-audiobooks" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub</a>
      </div>
      <p className="muted small">Open source (MIT). Part of the Pocket apps: Pocket Core connects them — Day, Bookshelf, Music, Podcasts and more.</p>
      {keys && <ShortcutsSheet onClose={() => setKeys(false)} />}
    </div>
  )
}
