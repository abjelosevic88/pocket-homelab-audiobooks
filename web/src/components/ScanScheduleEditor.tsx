import { useEffect, useState } from 'react'
import { fmtDateTime } from '../lib/format'

// The stored automatic-scan settings of one library (PUT .../watch body).
export type ScanSchedule = { enabled: boolean; interval_seconds: number; window_start: string; window_end: string }

const DAY = 86400
const DAILY_AT = 'daily-at'
const choices: [number, string][] = [
  [900, 'Every 15 minutes'], [1800, 'Every 30 minutes'], [3600, 'Every hour'], [10800, 'Every 3 hours'],
  [21600, 'Every 6 hours'], [43200, 'Every 12 hours'], [DAY, 'Once a day'],
]

const toMin = (v: string) => { const [h, m] = v.split(':').map(Number); return h * 60 + m }
const toClock = (m: number) => `${String(Math.floor(m / 60) % 24).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`
// "Daily at HH:MM" is stored as a one-hour window opening at that time, so a
// busy server still catches up shortly after.
const isDailyAt = (s: ScanSchedule) => s.interval_seconds >= DAY && !!s.window_start && !!s.window_end && (toMin(s.window_end) - toMin(s.window_start) + 1440) % 1440 === 60

function intervalLabel(seconds: number): string {
  const known = choices.find(([v]) => v === seconds)
  if (known) return known[1]
  if (seconds % 3600 === 0) return `Every ${seconds / 3600} hours`
  if (seconds === 60) return 'Every minute'
  return `Every ${Math.round(seconds / 60)} minutes`
}

export function describeSchedule(s: ScanSchedule): string {
  if (!s.enabled) return 'Off'
  if (isDailyAt(s)) return `Daily at ${s.window_start}`
  const base = intervalLabel(s.interval_seconds || 600)
  return s.window_start ? `${base}, only between ${s.window_start} and ${s.window_end}` : base
}

/** Edits a schedule locally and saves it with one button. */
export function ScanScheduleEditor({ value, onSave, saveLabel = 'Save schedule', timezone }: { value: ScanSchedule; onSave: (s: ScanSchedule) => Promise<void>; saveLabel?: string; timezone?: string }) {
  const key = JSON.stringify(value)
  const [draft, setDraft] = useState(value)
  const [saving, setSaving] = useState(false)
  useEffect(() => { setDraft(JSON.parse(key)) }, [key])
  const dirty = JSON.stringify(draft) !== key
  const dailyAt = isDailyAt(draft)
  const mode = dailyAt ? DAILY_AT : String(draft.interval_seconds || 600)
  const legacy = !dailyAt && !choices.some(([v]) => v === (draft.interval_seconds || 600))
  const setMode = (v: string) => {
    if (v === DAILY_AT) { const start = draft.window_start || '03:00'; setDraft({ ...draft, interval_seconds: DAY, window_start: start, window_end: toClock(toMin(start) + 60) }); return }
    const next = { ...draft, interval_seconds: Number(v) }
    if (dailyAt) { next.window_start = ''; next.window_end = '' }
    setDraft(next)
  }
  const save = async () => { setSaving(true); try { await onSave(draft) } finally { setSaving(false) } }
  return <div className="stack">
    <label><input type="checkbox" checked={draft.enabled} onChange={e => setDraft({ ...draft, enabled: e.target.checked })} /> Automatically check for file changes</label>
    {draft.enabled && <>
      <label>How often <select value={mode} onChange={e => setMode(e.target.value)}>
        {legacy && <option value={draft.interval_seconds}>{intervalLabel(draft.interval_seconds)}</option>}
        {choices.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
        <option value={DAILY_AT}>Daily at a set time</option>
      </select></label>
      {dailyAt
        ? <label>Time <input type="time" required value={draft.window_start} onChange={e => e.target.value && setDraft({ ...draft, window_start: e.target.value, window_end: toClock(toMin(e.target.value) + 60) })} /></label>
        : <>
          <label><input type="checkbox" checked={!!draft.window_start} onChange={e => setDraft({ ...draft, window_start: e.target.checked ? '01:00' : '', window_end: e.target.checked ? '06:00' : '' })} /> Only between certain hours</label>
          {draft.window_start && <div className="row wrap">
            <label>From <input type="time" required value={draft.window_start} onChange={e => e.target.value && setDraft({ ...draft, window_start: e.target.value })} /></label>
            <label>to <input type="time" required value={draft.window_end} onChange={e => e.target.value && setDraft({ ...draft, window_end: e.target.value })} /></label>
          </div>}
        </>}
      {(draft.window_start || dailyAt) && <p className="muted small">Times are in the server's time zone{timezone ? ` (${timezone})` : ''}. A window may run past midnight, for example 22:00 to 06:00.</p>}
    </>}
    {(dirty || saving) && <div className="row wrap"><button type="button" className="btn primary" disabled={saving || (!!draft.window_start && draft.window_start === draft.window_end)} onClick={save}>{saveLabel}</button>
      <button type="button" className="btn" disabled={saving} onClick={() => setDraft(value)}>Cancel</button></div>}
  </div>
}

type Job = { status: string; started_at: number; finished_at?: number | null; origin?: string; report: { added: number; updated: number; missing: number; quarantined?: number; invalid?: number; error?: string } }

function summary(job: Job): string {
  if (job.status === 'running') return 'running'
  if (job.status !== 'complete') return job.report.error ? `${job.status}: ${job.report.error}` : job.status
  const r = job.report
  return ['complete', `${r.added} added`, `${r.updated} updated`, `${r.missing} missing`, ...(r.quarantined ? [`${r.quarantined} quarantined`] : [])].join(' · ')
}

/** Shows the latest automatic and manual scan and the server's next run. */
export function ScanScheduleStatus({ jobs, nextAt }: { jobs: Job[]; nextAt?: number | null }) {
  const auto = jobs.find(j => j.origin === 'auto')
  const manual = jobs.find(j => j.origin !== 'auto')
  const line = (label: string, job?: Job) => job && <p>{label}: {fmtDateTime(new Date(job.started_at).toISOString())} · {summary(job)}</p>
  return <div role="status" className="small muted">
    {nextAt ? <p>Next scan: {nextAt <= Date.now() + 60000 ? 'within a minute' : fmtDateTime(new Date(nextAt).toISOString())}</p> : null}
    {line('Last automatic scan', auto)}
    {line('Last manual scan', manual)}
  </div>
}
