import { useEffect, useState } from 'react'
import type { ToolCallEvent } from './types'

const MAX_EVENTS = 500

export default function App() {
  const [events, setEvents] = useState<ToolCallEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    const es = new EventSource('/api/events')
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)
    es.onmessage = (msg) => {
      try {
        const e = JSON.parse(msg.data) as ToolCallEvent
        setEvents((prev) => [e, ...prev].slice(0, MAX_EVENTS))
      } catch {
        // ignore malformed event
      }
    }
    return () => es.close()
  }, [])

  const upstreams = Array.from(new Set(events.map((e) => e.upstream))).sort()
  const total = events.length
  const errors = events.filter((e) => e.status === 'error' || e.status === 'transport_error').length

  return (
    <div className="app">
      <header className="header">
        <div className="brand">
          <span className="logo">Aeolus</span>
          <span className="version">v0.2.0-dev</span>
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

      <main className="main">
        <section className="calls">
          <h2>Live tool calls</h2>
          {events.length === 0 ? (
            <div className="empty">
              No tool calls yet. Use your agent and they'll stream in here.
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
                {events.map((e, i) => (
                  <tr key={`${e.time}-${i}`}>
                    <td className="mono">{formatTime(e.time)}</td>
                    <td><span className="badge">{e.upstream}</span></td>
                    <td className="mono">{e.tool}</td>
                    <td className="num">{e.latency_ms}<span className="unit">ms</span></td>
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
