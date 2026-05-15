import { useEffect, useMemo, useState } from 'react'
import type { ToolCallEvent } from './types'

const MAX_EVENTS = 500
const TOP_STATS = 5

export default function App() {
  const [events, setEvents] = useState<ToolCallEvent[]>([])
  const [connected, setConnected] = useState(false)
  const [search, setSearch] = useState('')
  const [upstreamFilter, setUpstreamFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'' | 'ok' | 'error'>('')

  useEffect(() => {
    const es = new EventSource('/api/events')
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)
    es.onmessage = (msg) => {
      try {
        const e = JSON.parse(msg.data) as ToolCallEvent
        setEvents((prev) => [e, ...prev].slice(0, MAX_EVENTS))
      } catch {
        /* ignore */
      }
    }
    return () => es.close()
  }, [])

  const upstreams = useMemo(
    () => Array.from(new Set(events.map((e) => e.upstream))).sort(),
    [events]
  )

  const filtered = useMemo(() => {
    const q = search.toLowerCase()
    return events.filter((e) => {
      if (q && !e.tool.toLowerCase().includes(q)) return false
      if (upstreamFilter && e.upstream !== upstreamFilter) return false
      if (statusFilter === 'ok' && e.status !== 'ok') return false
      if (statusFilter === 'error' && e.status === 'ok') return false
      return true
    })
  }, [events, search, upstreamFilter, statusFilter])

  const total = filtered.length
  const errors = filtered.filter((e) => e.status !== 'ok').length
  const stats = useMemo(() => computeStats(filtered).slice(0, TOP_STATS), [filtered])

  return (
    <div className="app">
      <header className="header">
        <div className="brand">
          <span className="logo">Aeolus</span>
          <span className="version">v0.2.2-dev</span>
          <span className={`conn conn-${connected ? 'on' : 'off'}`}>
            {connected ? 'live' : 'disconnected'}
          </span>
        </div>
        <div className="upstreams">
          {upstreams.map((u) => (
            <span key={u} className="badge">{u}</span>
          ))}
        </div>
        <div className="stats">
          <Stat label="calls" value={total} />
          <Stat label="errors" value={errors} tone={errors > 0 ? 'error' : 'normal'} />
        </div>
      </header>

      <FilterBar
        search={search}
        onSearch={setSearch}
        upstream={upstreamFilter}
        onUpstream={setUpstreamFilter}
        upstreams={upstreams}
        status={statusFilter}
        onStatus={setStatusFilter}
      />

      {stats.length > 0 && <StatsStrip stats={stats} />}

      <main className="main">
        <section className="calls">
          <h2>Live tool calls</h2>
          {filtered.length === 0 ? (
            <div className="empty">
              {events.length === 0
                ? "No tool calls yet. Use your agent and they'll stream in here."
                : 'No calls match the current filter.'}
            </div>
          ) : (
            <table className="calls-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Upstream</th>
                  <th>Tool</th>
                  <th className="num-th">Latency</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((e, i) => (
                  <tr key={`${e.time}-${i}`}>
                    <td className="mono">{formatTime(e.time)}</td>
                    <td><span className="badge">{e.upstream}</span></td>
                    <td className="mono">{e.tool}</td>
                    <td className="num">
                      {e.latency_ms}
                      <span className="unit">ms</span>
                    </td>
                    <td>
                      <span className={`status status-${statusClass(e.status)}`}>{e.status}</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </main>
    </div>
  )
}

function FilterBar(props: {
  search: string
  onSearch: (s: string) => void
  upstream: string
  onUpstream: (u: string) => void
  upstreams: string[]
  status: '' | 'ok' | 'error'
  onStatus: (s: '' | 'ok' | 'error') => void
}) {
  return (
    <div className="filter-bar">
      <input
        type="search"
        className="search"
        placeholder="Filter by tool name..."
        value={props.search}
        onChange={(e) => props.onSearch(e.target.value)}
      />
      <select
        className="select"
        value={props.upstream}
        onChange={(e) => props.onUpstream(e.target.value)}
      >
        <option value="">All upstreams</option>
        {props.upstreams.map((u) => (
          <option key={u} value={u}>{u}</option>
        ))}
      </select>
      <select
        className="select"
        value={props.status}
        onChange={(e) => props.onStatus(e.target.value as '' | 'ok' | 'error')}
      >
        <option value="">All status</option>
        <option value="ok">ok only</option>
        <option value="error">errors only</option>
      </select>
    </div>
  )
}

type ToolStats = {
  tool: string
  upstream: string
  count: number
  errors: number
  p50_ms: number
  p95_ms: number
}

function StatsStrip({ stats }: { stats: ToolStats[] }) {
  return (
    <div className="stats-strip">
      {stats.map((s) => (
        <div key={s.tool} className="stat-card">
          <div className="stat-card-tool mono">{s.tool}</div>
          <div className="stat-card-row">
            <span className="stat-card-value">{s.count}</span>
            <span className="stat-card-label">calls</span>
            {s.errors > 0 && (
              <>
                <span className="stat-card-divider" />
                <span className="stat-card-value stat-card-error">{s.errors}</span>
                <span className="stat-card-label">err</span>
              </>
            )}
          </div>
          <div className="stat-card-row">
            <span className="stat-card-value">{s.p50_ms}<span className="unit">ms</span></span>
            <span className="stat-card-label">p50</span>
            <span className="stat-card-divider" />
            <span className="stat-card-value">{s.p95_ms}<span className="unit">ms</span></span>
            <span className="stat-card-label">p95</span>
          </div>
        </div>
      ))}
    </div>
  )
}

function Stat({ label, value, tone = 'normal' }: { label: string; value: number; tone?: 'normal' | 'error' }) {
  return (
    <div className="stat">
      <div className={`stat-value stat-${tone}`}>{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  )
}

function statusClass(status: string): string {
  return status === 'ok' ? 'ok' : 'error'
}

function formatTime(iso: string) {
  const d = new Date(iso)
  return d.toLocaleTimeString('en-US', { hour12: false })
}

function computeStats(events: ToolCallEvent[]): ToolStats[] {
  const groups = new Map<string, ToolCallEvent[]>()
  for (const e of events) {
    const arr = groups.get(e.tool)
    if (arr) arr.push(e)
    else groups.set(e.tool, [e])
  }
  const out: ToolStats[] = []
  for (const [tool, items] of groups) {
    const latencies = items.map((i) => i.latency_ms)
    const errors = items.filter((i) => i.status !== 'ok').length
    out.push({
      tool,
      upstream: items[0].upstream,
      count: items.length,
      errors,
      p50_ms: percentile(latencies, 50),
      p95_ms: percentile(latencies, 95),
    })
  }
  return out.sort((a, b) => b.count - a.count)
}

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((a, b) => a - b)
  const idx = Math.floor((p / 100) * (sorted.length - 1))
  return sorted[Math.max(0, Math.min(idx, sorted.length - 1))]
}
