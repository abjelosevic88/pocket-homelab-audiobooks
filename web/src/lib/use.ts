import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'

/** GET a URL; says when Audiobookshelf isn't connected (409 not_connected) apart from other errors. */
export function useApi<T>(url: string | null, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [notConnected, setNotConnected] = useState('')
  const [loading, setLoading] = useState(true)
  const reload = useCallback(() => {
    if (!url) return Promise.resolve()
    setLoading(true)
    return api.get<T>(url).then(d => { setData(d); setError(''); setNotConnected('') }).catch(e => {
      if (e instanceof ApiError && e.data.code === 'not_connected') setNotConnected(e.message)
      else setError((e as Error).message)
    }).finally(() => setLoading(false))
  }, [url, ...deps]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { reload() }, [reload])
  return { data, setData, error, notConnected, loading, reload }
}
