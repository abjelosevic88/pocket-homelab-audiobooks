// Small single-series charts for Stats: columns over time/hours/weekdays and
// a ranked bar list. Thin marks (≤24px, 4px rounded data end, square at the
// baseline), hairline grid, a tooltip per mark (hover and keyboard focus) and
// a table view, so no value is only reachable by hovering.
import { useEffect, useMemo, useRef, useState } from 'react'

export interface Point { key: string; label: string; value: number; detail?: string }

function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p
  return 10 * p
}
const compact = (n: number) => n >= 1000 ? `${+(n / 1000).toFixed(n >= 10000 ? 0 : 1)}K` : String(Math.round(n))

export function Columns({ data, height = 180, format = compact, labelEvery = 1, ariaLabel, highlight }: { data: Point[]; height?: number; format?: (n: number) => string; labelEvery?: number; ariaLabel: string; highlight?: string }) {
  const [hover, setHover] = useState<number | null>(null)
  const [table, setTable] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  // drawn in real pixels so corners stay round at every width
  const [W, setW] = useState(0)
  useEffect(() => {
    const el = box.current
    if (!el) return
    const ro = new ResizeObserver(() => setW(el.clientWidth))
    ro.observe(el)
    setW(el.clientWidth)
    return () => ro.disconnect()
  }, [table])
  const max = useMemo(() => niceMax(Math.max(0, ...data.map(d => d.value))), [data])
  const ticks = [0, max / 2, max]
  const padL = 34, padB = 22, padT = 8
  const n = Math.max(1, data.length)
  const plotW = W - padL
  const slot = plotW / n
  const bw = Math.max(1, Math.min(24, slot - 2)) // ≤24px, a 2px gap between touching columns
  const H = height
  const y = (v: number) => padT + (H - padT - padB) * (1 - v / max)
  if (table) return (
    <div className="chart-table-wrap">
      <table className="table compact"><thead><tr><th>{ariaLabel}</th><th className="right">Value</th></tr></thead>
        <tbody>{data.map(d => <tr key={d.key}><td>{d.label}</td><td className="right tnum">{d.detail || format(d.value)}</td></tr>)}</tbody></table>
      <button type="button" className="link-btn small" onClick={() => setTable(false)}>Show the chart</button>
    </div>
  )
  return (
    <div className="chart" ref={box} style={{ minHeight: H }}>
      {W > 0 && <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={ariaLabel} className="chart-svg" width={W} height={H}>
        {ticks.map(t => <line key={t} x1={padL} x2={W} y1={y(t)} y2={y(t)} className="grid" vectorEffect="non-scaling-stroke" />)}
        {data.map((d, i) => {
          const x = padL + i * slot + (slot - bw) / 2
          const top = y(d.value)
          const h = Math.max(0, H - padB - top)
          const r = Math.min(4, bw / 2, h)
          const path = h <= 0 ? '' : `M${x},${H - padB} V${top + r} Q${x},${top} ${x + r},${top} H${x + bw - r} Q${x + bw},${top} ${x + bw},${top + r} V${H - padB} Z`
          return (
            <g key={d.key} className={`col ${hover === i ? 'hi' : ''} ${highlight === d.key ? 'em' : ''}`} tabIndex={0} role="graphics-symbol" aria-label={`${d.label}: ${d.detail || format(d.value)}`}
              onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(null)} onFocus={() => setHover(i)} onBlur={() => setHover(null)}>
              <rect x={padL + i * slot} y={padT} width={slot} height={H - padT - padB} className="hit" />
              {path && <path d={path} className="bar" />}
            </g>
          )
        })}
      </svg>}
      <div className="chart-y">{ticks.map(t => <span key={t} style={{ top: y(t) }}>{format(t)}</span>)}</div>
      <div className="chart-x" style={{ left: W ? `${(padL / W) * 100}%` : padL }}>
        {data.map((d, i) => (i % labelEvery === 0 ? <span key={d.key} style={{ left: `${((i + 0.5) / n) * 100}%` }}>{d.label}</span> : null))}
      </div>
      {hover !== null && data[hover] && (
        <div className="chart-tip" style={{ left: `${((padL + (hover + 0.5) * slot) / W) * 100}%` }}>
          <b>{data[hover].detail || format(data[hover].value)}</b><span>{data[hover].label}</span>
        </div>
      )}
      <button type="button" className="link-btn xsmall chart-table-btn" onClick={() => setTable(true)}>Table</button>
    </div>
  )
}

/** A ranked list with a bar behind each row (top genres): value at the bar's tip. */
export function BarList({ data, format = compact }: { data: Point[]; format?: (n: number) => string }) {
  const max = Math.max(1, ...data.map(d => d.value))
  return (
    <div className="barlist" role="list">
      {data.map(d => (
        <div key={d.key} className="bl-row" role="listitem" title={d.detail || `${d.label}: ${format(d.value)}`}>
          <span className="bl-label ellipsis">{d.label}</span>
          <span className="bl-track"><span className="bl-bar" style={{ width: `${Math.max(2, (d.value / max) * 100)}%` }} /></span>
          <span className="bl-val tnum">{d.detail || format(d.value)}</span>
        </div>
      ))}
    </div>
  )
}
