import { useEffect, useMemo, useState } from 'react'
import { api } from './api'
import type { AeolusConfig, CatalogEntry, Upstream } from './types'
import AddUpstream from './AddUpstream'
import { applyCatalogFilters, type CatalogFilters } from './catalogFilters'
import { loadKnownBad } from './knownBad'

const CATALOG_DISPLAY_LIMIT = 60

type CatalogBatchMessage = {
  batch: CatalogEntry[]
  done: boolean
}

export default function Settings() {
  const [config, setConfig] = useState<AeolusConfig | null>(null)
  const [catalog, setCatalog] = useState<CatalogEntry[]>([])
  const [catalogLoading, setCatalogLoading] = useState(true)
  const [catalogSearch, setCatalogSearch] = useState('')
  const [catalogFilters, setCatalogFilters] = useState<CatalogFilters>({
    transport: 'all',
    auth: 'all',
  })
  const [error, setError] = useState<string | null>(null)
  const [showAdd, setShowAdd] = useState(false)
  const [editing, setEditing] = useState<Upstream | null>(null)
  const [prefill, setPrefill] = useState<CatalogEntry | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)
  const [settingsTab, setSettingsTab] = useState<'upstreams' | 'catalog'>('upstreams')
  const [knownBad, setKnownBad] = useState<Set<string>>(() => loadKnownBad())

  useEffect(() => {
    api.config().then(setConfig).catch((err) => setError(err.message))
  }, [])

  useEffect(() => {
    const es = new EventSource('/api/catalog/stream')
    es.onmessage = (msg) => {
      try {
        const data = JSON.parse(msg.data) as CatalogBatchMessage
        if (data.batch && data.batch.length > 0) {
          setCatalog((prev) => {
            const seen = new Set(prev.map((e) => e.id))
            const additions = data.batch.filter((e) => !seen.has(e.id))
            return additions.length > 0 ? [...prev, ...additions] : prev
          })
        }
        if (data.done) {
          setCatalogLoading(false)
        }
      } catch {
        /* ignore */
      }
    }
    es.onerror = () => setCatalogLoading(false)
    return () => es.close()
  }, [])

  const filteredCatalog = useMemo(
    () => applyCatalogFilters(catalog, catalogSearch, catalogFilters),
    [catalog, catalogSearch, catalogFilters]
  )

  const visibleCatalog = filteredCatalog.slice(0, CATALOG_DISPLAY_LIMIT)
  const hiddenCount = filteredCatalog.length - visibleCatalog.length

  async function removeUpstream(u: Upstream) {
    if (!config) return
    if (!confirm(`Remove upstream "${u.name}"? Its allow rules will also be cleared.`)) return
    setRemoving(u.name)
    setError(null)
    try {
      const prefix = `${u.name}.`
      const filteredAllow = (config.tools?.allow ?? []).filter(
        (r) => r !== `${u.name}.*` && !r.startsWith(prefix)
      )
      const filteredDeny = (config.tools?.deny ?? []).filter(
        (r) => r !== `${u.name}.*` && !r.startsWith(prefix)
      )
      const next: AeolusConfig = {
        ...config,
        upstreams: config.upstreams.filter((x) => x.name !== u.name),
        tools: { allow: filteredAllow, deny: filteredDeny },
      }
      const r = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(next),
      })
      if (!r.ok) throw new Error(await r.text())
      const saved = (await r.json()) as AeolusConfig
      setConfig(saved)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRemoving(null)
    }
  }

  if (error) return <div className="settings-error">Failed to load settings: {error}</div>
  if (!config) return <div className="settings-loading">Loading…</div>

  const tools = config.tools ?? {}
  const allowList = tools.allow ?? []
  const denyList = tools.deny ?? []

  return (
    <div className="settings">
      <nav className="subtabs">
        <button
          className={`subtab ${settingsTab === 'upstreams' ? 'subtab-active' : ''}`}
          onClick={() => setSettingsTab('upstreams')}
        >
          Upstreams ({config.upstreams.length})
        </button>
        <button
          className={`subtab ${settingsTab === 'catalog' ? 'subtab-active' : ''}`}
          onClick={() => setSettingsTab('catalog')}
        >
          Catalog ({catalog.length}{catalogLoading ? '+' : ''})
        </button>
      </nav>

      {settingsTab === 'upstreams' && (
        <section className="settings-section">
          <div className="settings-section-header">
            <h2>Connected upstreams</h2>
            <button className="btn-primary" onClick={() => setShowAdd(true)}>
              + Add upstream
            </button>
          </div>
          {(config.upstreams ?? []).length === 0 ? (
            <div className="welcome">
              <h3>Welcome to Aeolus</h3>
              <p>
                No upstreams yet. An <strong>upstream</strong> is an MCP server
                Aeolus will proxy for your client (Claude Code, Cursor,
                Copilot, etc.).
              </p>
              <p>Two ways to add your first one:</p>
              <div className="welcome-actions">
                <button className="btn-primary" onClick={() => setSettingsTab('catalog')}>
                  Browse catalog →
                </button>
                <button className="btn-secondary" onClick={() => setShowAdd(true)}>
                  + Add custom upstream
                </button>
              </div>
              <p className="welcome-hint">
                Your <code>aeolus.yaml</code> lives on disk and reloads on every change.
                Edit it by hand or use the dashboard — both work.
              </p>
            </div>
          ) : (
            <div className="upstream-grid">
              {config.upstreams.map((u) => (
                <UpstreamCard
                  key={u.name}
                  upstream={u}
                  allowList={allowList}
                  denyList={denyList}
                  onEdit={() => setEditing(u)}
                  onRemove={() => removeUpstream(u)}
                  removing={removing === u.name}
                />
              ))}
            </div>
          )}
        </section>
      )}

      {settingsTab === 'catalog' && (
        <section className="settings-section">
          <div className="settings-section-header">
            <h2>Catalog</h2>
            <span className="settings-help">
              {catalog.length} servers from the MCP registry
              {catalogLoading && <span className="catalog-loading"> · loading more…</span>}
            </span>
          </div>
          <input
            type="search"
            className="search"
            placeholder="Search catalog by name, description, or id..."
            value={catalogSearch}
            onChange={(e) => setCatalogSearch(e.target.value)}
          />
          <CatalogFilterBar filters={catalogFilters} onChange={setCatalogFilters} />
          <div className="catalog-grid">
            {visibleCatalog.map((e) => (
              <CatalogCard
                key={e.id}
                entry={e}
                failedProbe={knownBad.has(e.id)}
                onAdd={() => {
                  setPrefill(e)
                  setShowAdd(true)
                }}
              />
            ))}
          </div>
          {hiddenCount > 0 && (
            <div className="settings-help">
              {hiddenCount} more match{hiddenCount === 1 ? 'es' : ''} — refine the search to narrow down.
            </div>
          )}
          {filteredCatalog.length === 0 && (
            <div className="empty">No catalog entries match your search.</div>
          )}
        </section>
      )}

      {(showAdd || editing) && config && (
        <AddUpstream
          config={config}
          catalog={catalog}
          editing={editing ?? undefined}
          prefill={prefill ?? undefined}
          onClose={() => {
            setShowAdd(false)
            setEditing(null)
            setPrefill(null)
            setKnownBad(loadKnownBad())
          }}
          onSaved={(next) => {
            setConfig(next)
            setShowAdd(false)
            setEditing(null)
            setPrefill(null)
            setKnownBad(loadKnownBad())
          }}
        />
      )}
    </div>
  )
}

function UpstreamCard({
  upstream,
  allowList,
  denyList,
  onEdit,
  onRemove,
  removing,
}: {
  upstream: Upstream
  allowList: string[]
  denyList: string[]
  onEdit: () => void
  onRemove: () => void
  removing: boolean
}) {
  const [tab, setTab] = useState<'setup' | 'tools'>('setup')

  const prefix = `${upstream.name}.`
  const wildcard = `${upstream.name}.*`
  const ownAllow = allowList.filter((r) => r === wildcard || r.startsWith(prefix))
  const ownDeny = denyList.filter((r) => r === wildcard || r.startsWith(prefix))
  const ruleCount = ownAllow.length + ownDeny.length

  const transport = upstream.transport === 'http' ? 'http' : 'stdio'
  const argsLine = (upstream.args ?? []).join(' ')
  const envCount = (upstream.env ?? []).length
  const headerCount = Object.keys(upstream.headers ?? {}).length

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">{upstream.name}</span>
        <span className="badge">{transport}</span>
      </div>
      <div className="card-subtabs">
        <button
          className={`subtab ${tab === 'setup' ? 'subtab-active' : ''}`}
          onClick={() => setTab('setup')}
        >
          Setup
        </button>
        <button
          className={`subtab ${tab === 'tools' ? 'subtab-active' : ''}`}
          onClick={() => setTab('tools')}
        >
          Tools{ruleCount > 0 ? ` (${ruleCount})` : ''}
        </button>
      </div>
      <div className="card-body">
        {tab === 'setup' && (
          <>
            {transport === 'stdio' ? (
              <>
                <Row label="command" value={upstream.command ?? '—'} mono />
                {argsLine && <Row label="args" value={argsLine} mono />}
                {envCount > 0 && (
                  <Row label="env" value={`${envCount} variable${envCount === 1 ? '' : 's'}`} />
                )}
              </>
            ) : (
              <>
                <Row label="url" value={upstream.url ?? '—'} mono />
                {headerCount > 0 && (
                  <Row label="headers" value={`${headerCount} header${headerCount === 1 ? '' : 's'}`} />
                )}
              </>
            )}
          </>
        )}
        {tab === 'tools' && (
          <>
            {ruleCount === 0 ? (
              <div className="card-empty">All tools allowed (no rules).</div>
            ) : (
              <div className="card-rules">
                {ownAllow.length > 0 && (
                  <RuleList label="Allow" rules={stripPrefix(ownAllow, prefix)} tone="allow" />
                )}
                {ownDeny.length > 0 && (
                  <RuleList label="Deny" rules={stripPrefix(ownDeny, prefix)} tone="deny" />
                )}
              </div>
            )}
          </>
        )}
      </div>
      <div className="card-footer card-footer-spaced">
        <button className="btn-secondary" onClick={onEdit} disabled={removing}>
          Edit
        </button>
        <button className="btn-danger" onClick={onRemove} disabled={removing}>
          {removing ? 'Removing…' : 'Remove'}
        </button>
      </div>
    </div>
  )
}

function stripPrefix(rules: string[], prefix: string): string[] {
  return rules.map((r) => (r.startsWith(prefix) ? r.slice(prefix.length) : r))
}

function CatalogFilterBar({
  filters,
  onChange,
}: {
  filters: CatalogFilters
  onChange: (f: CatalogFilters) => void
}) {
  return (
    <div className="catalog-filters">
      <FilterGroup label="Transport">
        <FilterOption active={filters.transport === 'all'} onClick={() => onChange({ ...filters, transport: 'all' })}>All</FilterOption>
        <FilterOption active={filters.transport === 'stdio'} onClick={() => onChange({ ...filters, transport: 'stdio' })}>stdio</FilterOption>
        <FilterOption active={filters.transport === 'http'} onClick={() => onChange({ ...filters, transport: 'http' })}>http</FilterOption>
      </FilterGroup>
      <FilterGroup label="Auth">
        <FilterOption active={filters.auth === 'all'} onClick={() => onChange({ ...filters, auth: 'all' })}>All</FilterOption>
        <FilterOption active={filters.auth === 'none'} onClick={() => onChange({ ...filters, auth: 'none' })}>No auth</FilterOption>
        <FilterOption active={filters.auth === 'required'} onClick={() => onChange({ ...filters, auth: 'required' })}>Auth required</FilterOption>
      </FilterGroup>
    </div>
  )
}

function FilterGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="filter-group">
      <span className="filter-label">{label}</span>
      <div className="segmented filter-segmented">{children}</div>
    </div>
  )
}

function FilterOption({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" className={`segment ${active ? 'segment-active' : ''}`} onClick={onClick}>
      {children}
    </button>
  )
}

function CatalogCard({
  entry,
  failedProbe,
  onAdd,
}: {
  entry: CatalogEntry
  failedProbe: boolean
  onAdd: () => void
}) {
  const needsAuth =
    (entry.env && Object.keys(entry.env).length > 0) ||
    (entry.headers && Object.keys(entry.headers).length > 0)
  const transport = entry.transport === 'http' ? 'http' : 'stdio'
  return (
    <div className={`card ${failedProbe ? 'card-dim' : ''}`}>
      <div className="card-header">
        <span className="card-title">{entry.name}</span>
        <span className="badge">{transport}</span>
      </div>
      <div className="card-body">
        <p className="card-description">{entry.description}</p>
        {entry.notes && <p className="card-notes">{entry.notes}</p>}
        {(entry.repository || entry.website) && (
          <div className="card-links">
            {entry.repository && (
              <a href={entry.repository} target="_blank" rel="noopener noreferrer">Repo ↗</a>
            )}
            {entry.website && (
              <a href={entry.website} target="_blank" rel="noopener noreferrer">Docs ↗</a>
            )}
          </div>
        )}
      </div>
      <div className="card-footer card-footer-spaced">
        <span className="card-footer-slot">
          {needsAuth && <span className="badge badge-warn">needs auth</span>}
          {failedProbe && (
            <span
              className="badge badge-danger"
              title="A previous probe failed on this machine. Click Add to try again — a successful probe will clear this."
            >
              probe failed
            </span>
          )}
        </span>
        <button className="btn-primary" onClick={onAdd}>Add</button>
      </div>
    </div>
  )
}

function RuleList({ label, rules, tone }: { label: string; rules: string[]; tone: 'allow' | 'deny' }) {
  return (
    <div className="rule-list">
      <div className={`rule-list-label rule-${tone}`}>{label}</div>
      <div className="rule-list-items">
        {rules.map((r, i) => (
          <code key={i} className="rule">{r}</code>
        ))}
      </div>
    </div>
  )
}

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="row">
      <span className="row-label">{label}</span>
      <span className={`row-value ${mono ? 'mono' : ''}`}>{value}</span>
    </div>
  )
}
