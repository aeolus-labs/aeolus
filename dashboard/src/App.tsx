import { useEffect, useMemo, useState } from 'react'
import type { ToolCallEvent } from './types'
import Settings from './Settings'

type View = 'servers' | 'live'

const MAX_EVENTS = 500
const TOP_STATS = 5

const SIDEBAR_COLLAPSED_KEY = 'aeolus.sidebarCollapsed'

export default function App() {
  const [view, setView] = useState<View>('servers')
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1'
    } catch {
      return false
    }
  })

  useEffect(() => {
    try {
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, sidebarCollapsed ? '1' : '0')
    } catch {
      /* ignore */
    }
  }, [sidebarCollapsed])

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
          <span className="version">v0.4.0-dev</span>
          <span className={`conn conn-${connected ? 'on' : 'off'}`}>
            {connected ? 'live' : 'disconnected'}
          </span>
        </div>
        <div className="stats">
          <Stat label="calls" value={total} />
          <Stat label="errors" value={errors} tone={errors > 0 ? 'error' : 'normal'} />
        </div>
      </header>

      <div className="body">
        <nav className={`sidebar ${sidebarCollapsed ? 'sidebar-collapsed' : ''}`}>
          <div className="sidebar-items">
            <SidebarItem active={view === 'servers'} onClick={() => setView('servers')} label="Servers">
              <ServersIcon />
            </SidebarItem>
            <SidebarItem active={view === 'live'} onClick={() => setView('live')} label="Live">
              <LiveIcon />
            </SidebarItem>
          </div>
          <button
            className="sidebar-toggle"
            onClick={() => setSidebarCollapsed(!sidebarCollapsed)}
            title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          >
            <ChevronIcon flipped={sidebarCollapsed} />
            {!sidebarCollapsed && <span className="sidebar-label">Collapse</span>}
          </button>
        </nav>

        <div className="main-col">

      {view === 'servers' ? (
        <Settings />
      ) : (
        <LiveView
          events={events}
          filtered={filtered}
          stats={stats}
          upstreams={upstreams}
          search={search}
          onSearch={setSearch}
          upstreamFilter={upstreamFilter}
          onUpstreamFilter={setUpstreamFilter}
          statusFilter={statusFilter}
          onStatusFilter={setStatusFilter}
        />
      )}
        </div>
      </div>
    </div>
  )
}

function SidebarItem({
  active,
  onClick,
  label,
  children,
}: {
  active: boolean
  onClick: () => void
  label: string
  children: React.ReactNode
}) {
  return (
    <button
      className={`sidebar-item ${active ? 'sidebar-item-active' : ''}`}
      onClick={onClick}
      title={label}
    >
      <span className="sidebar-icon">{children}</span>
      <span className="sidebar-label">{label}</span>
    </button>
  )
}

function ServersIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="4" width="18" height="6" rx="2" />
      <rect x="3" y="14" width="18" height="6" rx="2" />
      <line x1="7" y1="7" x2="7.01" y2="7" />
      <line x1="7" y1="17" x2="7.01" y2="17" />
    </svg>
  )
}

function LiveIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
    </svg>
  )
}

function ChevronIcon({ flipped }: { flipped: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ transform: flipped ? 'rotate(180deg)' : 'none', transition: 'transform 0.15s' }}
    >
      <polyline points="15 18 9 12 15 6" />
    </svg>
  )
}

function LiveView(props: {
  events: ToolCallEvent[]
  filtered: ToolCallEvent[]
  stats: ToolStats[]
  upstreams: string[]
  search: string
  onSearch: (s: string) => void
  upstreamFilter: string
  onUpstreamFilter: (u: string) => void
  statusFilter: '' | 'ok' | 'error'
  onStatusFilter: (s: '' | 'ok' | 'error') => void
}) {
  const { events, filtered, stats, upstreams } = props
  const [expanded, setExpanded] = useState<string | null>(null)

  const servers = useMemo(() => summarizeByServer(events, upstreams), [events, upstreams])

  return (
    <div className="live-view">
      <FilterBar
        search={props.search}
        onSearch={props.onSearch}
        status={props.statusFilter}
        onStatus={props.onStatusFilter}
        upstreams={upstreams}
        upstreamFilter={props.upstreamFilter}
        onUpstreamFilter={props.onUpstreamFilter}
      />

      <ServerCardsRow
        events={events}
        servers={servers}
        active={props.upstreamFilter}
        onSelect={props.onUpstreamFilter}
      />

      {stats.length > 0 && <StatsStrip stats={stats} />}

      <section className="calls">
        <h2>Live tool calls</h2>
        <div className="calls-scroll">
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
                  <th aria-label="Expand" />
                  <th>Time</th>
                  <th>Upstream</th>
                  <th>Tool</th>
                  <th>Client</th>
                  <th className="num-th">Latency</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((e, i) => {
                  const key = `${e.time}-${i}`
                  const isOpen = expanded === key
                  return (
                    <CallRow
                      key={key}
                      event={e}
                      open={isOpen}
                      onToggle={() => setExpanded(isOpen ? null : key)}
                    />
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      </section>
    </div>
  )
}

function FilterBar(props: {
  search: string
  onSearch: (s: string) => void
  status: '' | 'ok' | 'error'
  onStatus: (s: '' | 'ok' | 'error') => void
  upstreams: string[]
  upstreamFilter: string
  onUpstreamFilter: (s: string) => void
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
        value={props.upstreamFilter}
        onChange={(e) => props.onUpstreamFilter(e.target.value)}
        title="Filter by server (mirrors the server card selection)"
      >
        <option value="">All servers</option>
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

type ServerSummary = {
  name: string
  count: number
  errors: number
  p95_ms: number
  lastSeen: number // epoch ms, 0 if never
  sparkline: number[]
}

function ServerCardsRow(props: {
  events: ToolCallEvent[]
  servers: ServerSummary[]
  active: string
  onSelect: (name: string) => void
}) {
  const { events, servers, active, onSelect } = props
  const totalCount = events.length
  const totalErrors = events.filter((e) => e.status !== 'ok').length
  const allLatencies = events.map((e) => e.latency_ms)
  const allP95 = percentile(allLatencies, 95)
  const allSparkline = sparklineCounts(events)

  return (
    <div className="server-cards">
      <ServerCard
        name="All servers"
        count={totalCount}
        errors={totalErrors}
        p95_ms={allP95}
        sparkline={allSparkline}
        active={active === ''}
        onClick={() => onSelect('')}
        idle={false}
      />
      <div className="server-cards-divider" aria-hidden="true" />
      {servers.map((s) => (
        <ServerCard
          key={s.name}
          name={s.name}
          count={s.count}
          errors={s.errors}
          p95_ms={s.p95_ms}
          sparkline={s.sparkline}
          active={active === s.name}
          onClick={() => onSelect(s.name)}
          idle={s.count === 0}
        />
      ))}
    </div>
  )
}

function ServerCard(props: {
  name: string
  count: number
  errors: number
  p95_ms: number
  sparkline: number[]
  active: boolean
  onClick: () => void
  idle: boolean
}) {
  const classes = ['server-card']
  if (props.active) classes.push('server-card-active')
  if (props.idle) classes.push('server-card-idle')
  return (
    <button className={classes.join(' ')} onClick={props.onClick}>
      <div className="server-card-head">
        <span className={`server-dot server-dot-${props.errors > 0 ? 'warn' : props.count > 0 ? 'on' : 'off'}`} />
        <span className="server-card-name">{props.name}</span>
      </div>
      <Sparkline values={props.sparkline} />
      <div className="server-card-row">
        <span className="server-card-num">{props.count}</span>
        <span className="server-card-label">calls</span>
        {props.errors > 0 && (
          <>
            <span className="stat-card-divider" />
            <span className="server-card-num server-card-error">{props.errors}</span>
            <span className="server-card-label">err</span>
          </>
        )}
        <span className="stat-card-divider" />
        <span className="server-card-num">
          {props.p95_ms}
          <span className="unit">ms</span>
        </span>
        <span className="server-card-label">p95</span>
      </div>
    </button>
  )
}

function Sparkline({ values }: { values: number[] }) {
  const w = 120
  const h = 24
  const max = Math.max(1, ...values)
  const barW = w / Math.max(1, values.length)
  return (
    <svg className="sparkline" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" width={w} height={h}>
      {values.map((v, i) => {
        const bh = Math.max(v > 0 ? 1 : 0, (v / max) * (h - 2))
        return (
          <rect
            key={i}
            x={i * barW}
            y={h - bh}
            width={Math.max(1, barW - 1)}
            height={bh}
            className="sparkline-bar"
          />
        )
      })}
    </svg>
  )
}

function CallRow(props: { event: ToolCallEvent; open: boolean; onToggle: () => void }) {
  const { event, open, onToggle } = props
  return (
    <>
      <tr className={`call-row ${open ? 'call-row-open' : ''}`} onClick={onToggle}>
        <td className="call-toggle">
          <ChevronIcon flipped={open} />
        </td>
        <td className="mono">{formatTime(event.time)}</td>
        <td><span className="badge">{event.upstream}</span></td>
        <td className="mono">{event.tool}</td>
        <td className="client-cell">{event.client || <span className="muted">—</span>}</td>
        <td className="num">
          {event.latency_ms}
          <span className="unit">ms</span>
        </td>
        <td>
          <span className={`status status-${statusClass(event.status)}`}>{event.status}</span>
        </td>
      </tr>
      {open && (
        <tr className="call-detail-row">
          <td colSpan={7}>
            <CallDetail event={event} />
          </td>
        </tr>
      )}
    </>
  )
}

function CallDetail({ event }: { event: ToolCallEvent }) {
  return (
    <div className="call-detail">
      <div className="call-detail-section">
        <div className="call-detail-label">Arguments</div>
        <JsonView value={event.arguments} />
      </div>
      <div className="call-detail-section">
        <div className="call-detail-label">Response</div>
        <JsonView value={event.response} />
      </div>
    </div>
  )
}

function JsonView({ value }: { value: unknown }) {
  if (value === undefined || value === null) {
    return <div className="call-detail-empty">— not recorded —</div>
  }
  let text: string
  try {
    text = JSON.stringify(value, null, 2)
  } catch {
    text = String(value)
  }
  return <pre className="call-detail-json">{text}</pre>
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
    <div className="stats-strip-wrap">
      <div className="stats-strip-label">Top tools</div>
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

// summarizeByServer produces a per-upstream rollup driven by the
// unfiltered event window so each server card shows real activity even
// when the table is filtered.
function summarizeByServer(events: ToolCallEvent[], known: string[]): ServerSummary[] {
  const byName = new Map<string, ToolCallEvent[]>()
  for (const name of known) byName.set(name, [])
  for (const e of events) {
    const arr = byName.get(e.upstream) ?? []
    arr.push(e)
    byName.set(e.upstream, arr)
  }
  const out: ServerSummary[] = []
  for (const [name, items] of byName) {
    const latencies = items.map((i) => i.latency_ms)
    const errors = items.filter((i) => i.status !== 'ok').length
    const lastSeen = items.length > 0 ? new Date(items[0].time).getTime() : 0
    out.push({
      name,
      count: items.length,
      errors,
      p95_ms: percentile(latencies, 95),
      lastSeen,
      sparkline: sparklineCounts(items),
    })
  }
  // Active servers first (recent activity), then idle ones by name.
  return out.sort((a, b) => {
    if (b.lastSeen !== a.lastSeen) return b.lastSeen - a.lastSeen
    return a.name.localeCompare(b.name)
  })
}

// sparklineCounts buckets events into the last SPARKLINE_WINDOW_MS,
// returning a fixed-length array of bucket counts. Newest activity is
// on the right edge of the resulting array so it reads left-to-right
// like a normal timeline.
const SPARKLINE_BUCKETS = 30
const SPARKLINE_WINDOW_MS = 5 * 60 * 1000 // last 5 minutes

function sparklineCounts(events: ToolCallEvent[]): number[] {
  const now = Date.now()
  const bucketSize = SPARKLINE_WINDOW_MS / SPARKLINE_BUCKETS
  const counts = new Array<number>(SPARKLINE_BUCKETS).fill(0)
  for (const e of events) {
    const t = new Date(e.time).getTime()
    const ageMs = now - t
    if (ageMs < 0 || ageMs >= SPARKLINE_WINDOW_MS) continue
    const idx = SPARKLINE_BUCKETS - 1 - Math.floor(ageMs / bucketSize)
    if (idx >= 0 && idx < SPARKLINE_BUCKETS) counts[idx]++
  }
  return counts
}
