// Library toolbar link to the match review queue: shown when this library's
// latest finished quick match left books that still need a decision.
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'

type Job = { id: string; status: string; counts: Record<string, number> }

export function ReviewMatchesButton({ library, revision = 0 }: { library: string; revision?: number }) {
  const [todo, setTodo] = useState<{ job: Job; pending: number }>()
  useEffect(() => {
    let live = true
    setTodo(undefined)
    ;(async () => {
      const jobs = await api.get<Job[]>(`${V1}/catalog/match-jobs?library=${encodeURIComponent(library)}`)
      const job = jobs.find(j => j.status !== 'running' && j.status !== 'queued')
      if (!job || !((job.counts.low_confidence || 0) + (job.counts.not_found || 0))) return
      const q = await api.get<{ counts: { pending: number } }>(`${V1}/catalog/match-jobs/${job.id}/review?status=all`)
      if (live && q.counts.pending > 0) setTodo({ job, pending: q.counts.pending })
    })().catch(() => {})
    return () => { live = false }
  }, [library, revision])
  if (!todo) return null
  return <Link className="btn" to={`/review/${todo.job.id}${todo.job.counts.low_confidence ? '' : '?status=not_found'}`} title="Walk the books quick match was not sure about">Review {todo.pending} uncertain {todo.pending === 1 ? 'match' : 'matches'}</Link>
}
