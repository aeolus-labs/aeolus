import { useEffect, useMemo, useState } from 'react'
import type { ToolCallEvent } from './types'
import Settings from './Settings'
import Dropdown from './Dropdown'
import { useDashboardState } from './state'

type View = 'servers' | 'live'

const MAX_EVENTS = 500
const TOP_STATS = 5

export default function App() {
  const [view, setView] = useState<View>('servers')

  // Sidebar collapsed state is persisted server-side via dashboard
  // state so it survives browser switches. While the initial state
  // is loading we default to "expanded" — flipping it once the value
  // lands is a one-off; not worth a skeleton.
  const { state: dashState, update: updateDashState } = useDashboardState()
  const sidebarCollapsed = dashState?.sidebar_collapsed ?? false
  function setSidebarCollapsed(next: boolean) {
    updateDashState({ sidebar_collapsed: next })
  }

  // openTour navigates to the Servers tab (where TourController lives)
  // and then triggers the tour. Shared by the sidebar Help button and
  // the Live tab empty state.
  function openTour() {
    sessionStorage.setItem('aeolus.tourPending', '1')
    window.dispatchEvent(new Event('aeolus:open-tour'))
    setView('servers')
  }

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
            <SidebarItem
              active={view === 'live'}
              onClick={() => setView('live')}
              label="Live"
              tourId="live-tab"
            >
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
          <div className="sidebar-divider" />
          <button
            className="sidebar-toggle sidebar-help"
            onClick={openTour}
            title="Show the guided tour"
            aria-label="Show the guided tour"
          >
            <span className="sidebar-icon"><HelpIcon /></span>
            {!sidebarCollapsed && <span className="sidebar-label">Help</span>}
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
          connected={connected}
          onOpenTour={openTour}
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
  tourId,
  children,
}: {
  active: boolean
  onClick: () => void
  label: string
  tourId?: string
  children: React.ReactNode
}) {
  return (
    <button
      className={`sidebar-item ${active ? 'sidebar-item-active' : ''}`}
      onClick={onClick}
      title={label}
      data-tour={tourId}
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

function HelpIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="12" cy="12" r="10" />
      <path d="M9.5 9a2.5 2.5 0 1 1 4.5 1.5c-.83 .67 -2 1.5 -2 3" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  )
}

function LiveView(props: {
  events: ToolCallEvent[]
  filtered: ToolCallEvent[]
  stats: ToolStats[]
  upstreams: string[]
  connected: boolean
  onOpenTour: () => void
  search: string
  onSearch: (s: string) => void
  upstreamFilter: string
  onUpstreamFilter: (u: string) => void
  statusFilter: '' | 'ok' | 'error'
  onStatusFilter: (s: '' | 'ok' | 'error') => void
}) {
  const { events, filtered, stats, upstreams, connected, onOpenTour } = props
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
            events.length === 0 ? (
              <LiveEmptyState connected={connected} onOpenTour={onOpenTour} />
            ) : (
              <div className="empty">No calls match the current filter.</div>
            )
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
  const upstreamOptions = [
    { value: '', label: 'All servers' },
    ...props.upstreams.map((u) => ({ value: u, label: u })),
  ]
  const statusOptions = [
    { value: '', label: 'All status' },
    { value: 'ok', label: 'ok only' },
    { value: 'error', label: 'errors only' },
  ]
  return (
    <div className="filter-bar">
      <input
        type="search"
        className="search"
        placeholder="Filter by tool name..."
        value={props.search}
        onChange={(e) => props.onSearch(e.target.value)}
      />
      <Dropdown
        value={props.upstreamFilter}
        options={upstreamOptions}
        onChange={props.onUpstreamFilter}
        width={180}
        title="Filter by server (mirrors the server card selection)"
      />
      <Dropdown
        value={props.status}
        options={statusOptions}
        onChange={(v) => props.onStatus(v as '' | 'ok' | 'error')}
        width={140}
      />
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
      <CallDetailSection label="Arguments" value={event.arguments} />
      <CallDetailSection label="Response" value={event.response} />
    </div>
  )
}

function CallDetailSection({ label, value }: { label: string; value: unknown }) {
  const [copied, setCopied] = useState(false)

  if (value === undefined || value === null) {
    return (
      <div className="call-detail-section">
        <div className="call-detail-header">
          <span className="call-detail-label">{label}</span>
        </div>
        <div className="call-detail-empty">— not recorded —</div>
      </div>
    )
  }

  const text = stringifyJson(value)
  const truncated = isTruncatedPayload(value)

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    } catch {
      /* ignore */
    }
  }

  return (
    <div className="call-detail-section">
      <div className="call-detail-header">
        <span className="call-detail-label">{label}</span>
        <button
          type="button"
          className="call-detail-copy"
          onClick={copy}
          title={`Copy ${label.toLowerCase()} JSON to clipboard`}
        >
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
      {truncated && (
        <div className="call-detail-truncated">
          Truncated — Aeolus caps recorded payloads at 16 KiB
          {truncated.original ? ` (original was ${truncated.original} bytes)` : ''}.
        </div>
      )}
      <pre className="call-detail-json">
        <HighlightedJson text={text} />
      </pre>
    </div>
  )
}

function stringifyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

// isTruncatedPayload recognizes the placeholder shape the engine emits
// when args/response are larger than the 16 KiB cap. Returns the
// original byte count if present so the UI can name it.
function isTruncatedPayload(value: unknown): { original?: number } | null {
  if (
    value &&
    typeof value === 'object' &&
    !Array.isArray(value) &&
    '_truncated' in (value as Record<string, unknown>) &&
    (value as { _truncated: unknown })._truncated === true
  ) {
    const orig = (value as { original_bytes?: number }).original_bytes
    return { original: typeof orig === 'number' ? orig : undefined }
  }
  return null
}

// HighlightedJson colors a pretty-printed JSON string by token type
// (key, string, number, boolean, null). Single regex pass, no
// library. Stable enough for our payloads — the cases where the
// regex misclassifies are degenerate strings that contain unescaped
// quotes, which shouldn't happen in JSON.stringify output.
const JSON_TOKEN = /("(?:\\u[a-fA-F0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g

function HighlightedJson({ text }: { text: string }) {
  const out: React.ReactNode[] = []
  let last = 0
  let m: RegExpExecArray | null
  let i = 0
  while ((m = JSON_TOKEN.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const tok = m[1]
    let cls = 'json-number'
    if (tok.startsWith('"')) {
      cls = m[2] ? 'json-key' : 'json-string'
    } else if (tok === 'true' || tok === 'false') {
      cls = 'json-bool'
    } else if (tok === 'null') {
      cls = 'json-null'
    }
    out.push(
      <span key={i++} className={cls}>
        {tok}
      </span>,
    )
    last = m.index + tok.length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
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

function LiveEmptyState({
  connected,
  onOpenTour,
}: {
  connected: boolean
  onOpenTour: () => void
}) {
  return (
    <div className="live-empty">
      <div className="live-empty-illustration" aria-hidden="true">⏳</div>
      <h3>No tool calls yet</h3>
      {connected ? (
        <>
          <p>
            Aeolus is connected and watching. Have your MCP client call a tool
            and it'll stream in here in real time.
          </p>
          <p className="muted">
            Each call shows its arguments, response, latency, and which client
            made it. Click any row once they arrive to expand the JSON detail.
          </p>
        </>
      ) : (
        <>
          <p>
            The dashboard isn't currently connected to the daemon's event
            stream.
          </p>
          <p className="muted">
            Make sure <code className="mono">aeolus service status</code> reports
            running, then refresh this page.
          </p>
        </>
      )}
      <div className="live-empty-actions">
        <button className="btn-primary" onClick={onOpenTour}>
          Show me the tour
        </button>
      </div>
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
