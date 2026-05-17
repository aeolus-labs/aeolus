import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api'
import type { AeolusConfig, CatalogEntry, Workspace, Upstream } from './types'
import AddUpstream from './AddUpstream'
import { applyCatalogFilters, type CatalogFilters } from './catalogFilters'
import Dropdown, { DropdownAction } from './Dropdown'
import Tour, { type TourStep } from './Tour'
import { useDashboardState } from './state'
import { loadKnownBad } from './knownBad'

const CATALOG_PAGE_SIZE = 60

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
  const [reconnecting, setReconnecting] = useState<string | null>(null)
  const [toggling, setToggling] = useState<string | null>(null)
  const [removingFromScope, setRemovingFromScope] = useState<string | null>(null)
  const [settingsTab, setSettingsTab] = useState<'upstreams' | 'catalog'>('upstreams')
  const [knownBad, setKnownBad] = useState<Set<string>>(() => loadKnownBad())
  // currentWorkspace = "" → "All upstreams" view (no workspace filter).
  // Any other value → the dashboard scopes the Servers tab to that
  // workspace and "+ Add" auto-includes new entries into it.
  const [currentWorkspace, setCurrentWorkspace] = useState<string>('')
  const [showNewWorkspace, setShowNewWorkspace] = useState(false)
  const [showSnippet, setShowSnippet] = useState(false)
  const [showAddExisting, setShowAddExisting] = useState(false)
  const [showGlobalSnippet, setShowGlobalSnippet] = useState(false)
  // Map of upstream name → failure message for any upstream the
  // engine couldn't initialize (or whose tools failed to refresh).
  // Polled every 5s and refreshed after each mutation.
  const [failures, setFailures] = useState<Record<string, string>>({})

  useEffect(() => {
    api.config().then(setConfig).catch((err) => setError(err.message))
  }, [])

  // Refresh the failure map. Called on mount, on a 5s poll, and after
  // any mutation that might fix or surface new failures (toggle,
  // reconnect, save).
  async function refreshFailures() {
    try {
      const r = await fetch('/api/upstreams/failures')
      if (!r.ok) return
      const data = (await r.json()) as { failures?: { name: string; error: string }[] }
      const map: Record<string, string> = {}
      for (const f of data.failures ?? []) map[f.name] = f.error
      setFailures(map)
    } catch {
      /* ignore — keep last known map */
    }
  }

  useEffect(() => {
    refreshFailures()
    const t = setInterval(refreshFailures, 5000)
    return () => clearInterval(t)
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

  const [catalogPage, setCatalogPage] = useState(1)
  const visibleCatalog = filteredCatalog.slice(0, catalogPage * CATALOG_PAGE_SIZE)
  const hiddenCount = filteredCatalog.length - visibleCatalog.length

  // Reset back to the first page whenever filters or search change so the
  // user sees the top of the new result set.
  useEffect(() => {
    setCatalogPage(1)
  }, [catalogSearch, catalogFilters.transport, catalogFilters.auth])

  // Sentinel at the bottom of the catalog grid — when it scrolls into
  // view we bump the page count. Pure infinite scroll, no Load More
  // button needed.
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (hiddenCount <= 0) return
    const el = sentinelRef.current
    if (!el) return
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setCatalogPage((p) => p + 1)
          }
        }
      },
      { rootMargin: '200px' }, // start loading slightly before the user reaches the bottom
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hiddenCount, visibleCatalog.length])

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

  async function setUpstreamEnabled(u: Upstream, enabled: boolean) {
    if (!config) return
    setError(null)
    // Optimistic: flip the toggle visually right away, then roll back on
    // error. The PUT + reload takes ~1s and the user needs instant
    // feedback that their click registered.
    const before = config
    const next: AeolusConfig = {
      ...config,
      upstreams: config.upstreams.map((x) =>
        x.name === u.name ? { ...x, enabled } : x
      ),
    }
    setConfig(next)
    setToggling(u.name)
    try {
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
      setConfig(before) // roll back the optimistic flip
    } finally {
      setToggling(null)
    }
  }

  async function reconnectUpstream(u: Upstream) {
    setError(null)
    setReconnecting(u.name)
    try {
      const r = await fetch(`/api/upstreams/${encodeURIComponent(u.name)}/reconnect`, {
        method: 'POST',
      })
      if (!r.ok) throw new Error(await r.text())
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setReconnecting(null)
      // Immediate refresh so the broken badge / banner reflects the
      // new state without waiting for the next poll tick.
      void refreshFailures()
    }
  }

  // ---- Workspace mutations ----

  async function saveConfig(next: AeolusConfig): Promise<AeolusConfig | null> {
    try {
      const r = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(next),
      })
      if (!r.ok) throw new Error(await r.text())
      const saved = (await r.json()) as AeolusConfig
      setConfig(saved)
      // A config save triggers a daemon reload, which may surface
      // new failures (bad command, expired token) or clear old ones.
      void refreshFailures()
      return saved
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
      return null
    }
  }

  async function createWorkspace(name: string, cwdMatch: string[]): Promise<boolean> {
    if (!config) return false
    const workspaces = config.workspaces ?? []
    if (workspaces.some((p) => p.name === name)) {
      setError(`A workspace named "${name}" already exists`)
      return false
    }
    const next: AeolusConfig = {
      ...config,
      workspaces: [...workspaces, { name, include: [], cwd_match: cwdMatch }],
    }
    const saved = await saveConfig(next)
    if (saved) {
      setCurrentWorkspace(name)
      setShowNewWorkspace(false)
      return true
    }
    return false
  }

  async function deleteWorkspace(name: string) {
    if (!config) return
    if (!confirm(`Delete workspace "${name}"? Upstreams stay; only the workspace entry is removed.`)) return
    const next: AeolusConfig = {
      ...config,
      workspaces: (config.workspaces ?? []).filter((p) => p.name !== name),
    }
    const saved = await saveConfig(next)
    if (saved && currentWorkspace === name) {
      setCurrentWorkspace('')
    }
  }

  async function setWorkspaceMembership(workspaceName: string, upstreamName: string, include: boolean) {
    if (!config) return
    const workspaces = config.workspaces ?? []
    const next: AeolusConfig = {
      ...config,
      workspaces: workspaces.map((p) => {
        if (p.name !== workspaceName) return p
        const cur = p.include ?? []
        const isIn = cur.includes(upstreamName)
        if (include && !isIn) return { ...p, include: [...cur, upstreamName] }
        if (!include && isIn) return { ...p, include: cur.filter((n) => n !== upstreamName) }
        return p
      }),
    }
    await saveConfig(next)
  }

  // updateWorkspace lets the Edit Workspace modal change both the name and
  // cwd_match in one save. Returns true on success so the modal can
  // close itself.
  async function updateWorkspace(
    oldName: string,
    newName: string,
    cwdMatch: string[],
  ): Promise<boolean> {
    if (!config) return false
    const trimmed = newName.trim()
    if (trimmed === '') {
      setError('Workspace name cannot be empty')
      return false
    }
    if (trimmed !== oldName && (config.workspaces ?? []).some((p) => p.name === trimmed)) {
      setError(`A workspace named "${trimmed}" already exists`)
      return false
    }
    const next: AeolusConfig = {
      ...config,
      workspaces: (config.workspaces ?? []).map((p) =>
        p.name === oldName ? { ...p, name: trimmed, cwd_match: cwdMatch } : p,
      ),
    }
    const saved = await saveConfig(next)
    if (!saved) return false
    if (currentWorkspace === oldName) setCurrentWorkspace(trimmed)
    return true
  }

  if (!config) {
    if (error) {
      return (
        <div className="settings-error">
          <div className="settings-error-row">
            <span>Failed to load settings: {error}</span>
            <button className="btn-secondary" onClick={() => location.reload()}>Retry</button>
          </div>
        </div>
      )
    }
    return <div className="settings-loading">Loading…</div>
  }

  const tools = config.tools ?? {}
  const allowList = tools.allow ?? []
  const denyList = tools.deny ?? []
  const workspaces = config.workspaces ?? []
  const activeWorkspace: Workspace | null = currentWorkspace
    ? workspaces.find((p) => p.name === currentWorkspace) ?? null
    : null
  const visibleUpstreams = activeWorkspace
    ? config.upstreams.filter((u) => (activeWorkspace.include ?? []).includes(u.name))
    : config.upstreams

  // For each upstream, which workspaces list it in their include set —
  // shown as small badges on the card so the user can spot servers
  // shared across projects.
  function workspaceMembershipsFor(name: string): string[] {
    return workspaces
      .filter((p) => (p.include ?? []).includes(name))
      .map((p) => p.name)
  }

  return (
    <div className="settings">
      {error && (
        <div className="page-error">
          <span className="page-error-text">{error}</span>
          <button
            className="page-error-dismiss"
            onClick={() => setError(null)}
            aria-label="Dismiss error"
          >
            ×
          </button>
        </div>
      )}

      <WorkspaceBar
        workspaces={workspaces}
        current={currentWorkspace}
        onSelect={setCurrentWorkspace}
        onNew={() => setShowNewWorkspace(true)}
        onDelete={(name) => deleteWorkspace(name)}
        onUpdateWorkspace={(oldName, newName, cwd) => updateWorkspace(oldName, newName, cwd)}
        onShowSnippet={() => {
          // Pick the right snippet modal based on the current view.
          if (activeWorkspace) setShowSnippet(true)
          else setShowGlobalSnippet(true)
        }}
      />

      {showSnippet && activeWorkspace && (
        <ClientConfigModal
          workspace={activeWorkspace}
          onClose={() => setShowSnippet(false)}
        />
      )}

      {showGlobalSnippet && (
        <ClientConfigModal
          workspace={null}
          onClose={() => setShowGlobalSnippet(false)}
        />
      )}

      <TourController />

      {Object.keys(failures).length > 0 && (
        <FailureBanner failures={failures} onReconnect={(name) => {
          const u = config.upstreams.find((x) => x.name === name)
          if (u) reconnectUpstream(u)
        }} />
      )}

      <nav className="subtabs">
        <button
          className={`subtab ${settingsTab === 'upstreams' ? 'subtab-active' : ''}`}
          onClick={() => setSettingsTab('upstreams')}
          title={activeWorkspace ? activeWorkspace.name : ''}
        >
          {activeWorkspace ? (
            <span className="workspace-name-truncate subtab-workspace-name">{activeWorkspace.name}</span>
          ) : (
            `Upstreams (${config.upstreams.length})`
          )}
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
            <h2 title={activeWorkspace ? activeWorkspace.name : ''}>
              {activeWorkspace ? (
                <span className="workspace-name-truncate">{activeWorkspace.name}</span>
              ) : (
                'Connected upstreams'
              )}
            </h2>
            <div className="settings-section-actions">
              {activeWorkspace && (
                <button className="btn-secondary" onClick={() => setShowAddExisting(true)}>
                  + Add existing
                </button>
              )}
              <button
                className="btn-primary"
                onClick={() => setShowAdd(true)}
                data-tour="add-upstream"
              >
                + Add upstream
              </button>
            </div>
          </div>
          {visibleUpstreams.length === 0 ? (
            activeWorkspace ? (
              <div className="welcome">
                <h3>No servers in {activeWorkspace.name} yet</h3>
                <p>Two ways to populate it:</p>
                <div className="welcome-actions">
                  <button className="btn-primary" onClick={() => setShowAddExisting(true)}>
                    + Add existing servers
                  </button>
                  <button className="btn-secondary" onClick={() => setShowAdd(true)}>
                    + Add new upstream
                  </button>
                </div>
                <p className="welcome-hint">
                  "Add existing" pulls servers you've already configured into this
                  workspace. "Add new" creates a brand-new server, auto-included here.
                </p>
              </div>
            ) : (
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
            )
          ) : (
            <div className="upstream-grid">
              {visibleUpstreams.map((u) => (
                <UpstreamCard
                  key={u.name}
                  upstream={u}
                  allowList={allowList}
                  denyList={denyList}
                  inWorkspaces={workspaceMembershipsFor(u.name)}
                  scopedToWorkspace={activeWorkspace?.name ?? null}
                  failure={failures[u.name]}
                  onEdit={() => setEditing(u)}
                  onRemove={() => removeUpstream(u)}
                  onRemoveFromWorkspace={async () => {
                    if (!activeWorkspace) return
                    if (removingFromScope) return // another removal in flight
                    setRemovingFromScope(u.name)
                    try {
                      await setWorkspaceMembership(activeWorkspace.name, u.name, false)
                    } finally {
                      setRemovingFromScope(null)
                    }
                  }}
                  onToggleEnabled={(next) => setUpstreamEnabled(u, next)}
                  onReconnect={() => reconnectUpstream(u)}
                  removing={removing === u.name}
                  reconnecting={reconnecting === u.name}
                  toggling={toggling === u.name}
                  removingFromScope={removingFromScope === u.name}
                  anyScopeRemovalInFlight={removingFromScope !== null}
                />
              ))}
            </div>
          )}
        </section>
      )}

      {settingsTab === 'catalog' && (
        <section className="settings-section catalog-section">
          <div className="settings-section-header">
            <h2>
              Catalog
              <span className="settings-help">
                {' '}· {catalog.length} servers
                {catalogLoading && <span className="catalog-loading"> · loading more…</span>}
              </span>
            </h2>
            <button className="btn-primary" onClick={() => setShowAdd(true)}>
              + Add upstream
            </button>
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
            {hiddenCount > 0 && (
              <div ref={sentinelRef} className="catalog-sentinel">
                Loading more…
              </div>
            )}
          </div>
          <div className="catalog-footer">
            {filteredCatalog.length === 0 ? (
              <span>No catalog entries match your search.</span>
            ) : hiddenCount > 0 ? (
              <span>
                Showing {visibleCatalog.length} of {filteredCatalog.length} — scroll for more.
              </span>
            ) : (
              <span>
                {filteredCatalog.length} match{filteredCatalog.length === 1 ? '' : 'es'}
              </span>
            )}
          </div>
        </section>
      )}

      {showNewWorkspace && (
        <NewWorkspaceModal
          existing={workspaces.map((p) => p.name)}
          onClose={() => setShowNewWorkspace(false)}
          onCreate={(name, cwdMatch) => createWorkspace(name, cwdMatch)}
        />
      )}

      {showAddExisting && activeWorkspace && (
        <AddExistingMembersModal
          workspace={activeWorkspace}
          allUpstreams={config.upstreams}
          onClose={() => setShowAddExisting(false)}
          onAdd={async (names) => {
            if (!config) return
            const workspacesNext = (config.workspaces ?? []).map((p) => {
              if (p.name !== activeWorkspace.name) return p
              const cur = new Set(p.include ?? [])
              for (const n of names) cur.add(n)
              return { ...p, include: Array.from(cur) }
            })
            const saved = await saveConfig({ ...config, workspaces: workspacesNext })
            if (saved) setShowAddExisting(false)
          }}
        />
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
          onSaved={async (next) => {
            // If a workspace is selected, auto-include the newly-saved
            // upstream(s) in it so the user doesn't have to do a
            // second click to add them to the workspace.
            let finalConfig = next
            if (activeWorkspace) {
              const prevNames = new Set((config?.upstreams ?? []).map((u) => u.name))
              const newNames = next.upstreams
                .map((u) => u.name)
                .filter((n) => !prevNames.has(n))
              if (newNames.length > 0) {
                const includes = new Set(activeWorkspace.include ?? [])
                newNames.forEach((n) => includes.add(n))
                const withWorkspace: AeolusConfig = {
                  ...next,
                  workspaces: (next.workspaces ?? []).map((p) =>
                    p.name === activeWorkspace.name
                      ? { ...p, include: Array.from(includes) }
                      : p,
                  ),
                }
                const saved = await saveConfig(withWorkspace)
                if (saved) finalConfig = saved
              }
            }
            setConfig(finalConfig)
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
  inWorkspaces,
  scopedToWorkspace,
  failure,
  onEdit,
  onRemove,
  onRemoveFromWorkspace,
  onToggleEnabled,
  onReconnect,
  removing,
  reconnecting,
  toggling,
  removingFromScope,
  anyScopeRemovalInFlight,
}: {
  upstream: Upstream
  allowList: string[]
  denyList: string[]
  inWorkspaces: string[]
  scopedToWorkspace: string | null
  failure?: string
  onEdit: () => void
  onRemove: () => void
  onRemoveFromWorkspace: () => void
  onToggleEnabled: (next: boolean) => void
  onReconnect: () => void
  removing: boolean
  reconnecting: boolean
  toggling: boolean
  removingFromScope: boolean
  anyScopeRemovalInFlight: boolean
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
  const enabled = upstream.enabled !== false
  const busy = removing || reconnecting || toggling || removingFromScope

  return (
    <div className={`card ${enabled ? '' : 'card-disabled'}`}>
      <div className="card-header">
        <span className="card-title">{upstream.name}</span>
        <span className="badge">{transport}</span>
        {failure && (
          <span
            className="badge badge-danger broken-pill"
            title={failure}
          >
            broken
          </span>
        )}
        {inWorkspaces.length > 0 && (
          <span
            className="scope-pill"
            title={`In ${inWorkspaces.length} workspace${inWorkspaces.length === 1 ? '' : 's'}: ${inWorkspaces.join(', ')}. Click Edit to manage.`}
          >
            scoped
          </span>
        )}
        <div className="enable-control">
          {toggling ? (
            <span className="enable-state enable-state-saving">
              <Spinner />
              {enabled ? 'Enabling…' : 'Disabling…'}
            </span>
          ) : (
            <span className={`enable-state ${enabled ? 'enable-state-on' : 'enable-state-off'}`}>
              {enabled ? 'Enabled' : 'Disabled'}
            </span>
          )}
          <ToggleSwitch
            checked={enabled}
            onChange={onToggleEnabled}
            disabled={busy}
            label={enabled ? 'Disable upstream' : 'Enable upstream'}
          />
        </div>
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
            {failure && (
              <div className="card-failure">
                <div className="card-failure-label">Last error</div>
                <code className="card-failure-msg">{failure}</code>
              </div>
            )}
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
        <button
          className="btn-secondary"
          onClick={onReconnect}
          disabled={busy || !enabled}
          title={!enabled ? 'Enable the upstream first.' : 'Restart this upstream in place.'}
        >
          {reconnecting ? 'Reconnecting…' : 'Reconnect'}
        </button>
        <div className="card-footer-right">
          <button className="btn-secondary" onClick={onEdit} disabled={busy}>
            Edit
          </button>
          {scopedToWorkspace ? (
            <button
              className="btn-danger"
              onClick={onRemoveFromWorkspace}
              disabled={busy || anyScopeRemovalInFlight}
              title={`Remove from workspace "${scopedToWorkspace}". The upstream stays configured globally.`}
            >
              {removingFromScope ? (
                <>
                  <Spinner /> Removing…
                </>
              ) : (
                'Remove from scope'
              )}
            </button>
          ) : (
            <button className="btn-danger" onClick={onRemove} disabled={busy}>
              {removing ? 'Removing…' : 'Remove'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function ToggleSwitch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      className={`toggle-switch ${checked ? 'toggle-switch-on' : ''}`}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span className="toggle-switch-knob" />
    </button>
  )
}

function Spinner() {
  return (
    <svg
      className="spinner"
      viewBox="0 0 16 16"
      width="12"
      height="12"
      aria-hidden="true"
    >
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="2" opacity="0.25" />
      <path
        d="M 8 2 A 6 6 0 0 1 14 8"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
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

// FailureBanner is shown at the top of the Servers tab whenever the
// engine reports one or more upstreams that couldn't be initialized
// or refreshed. Click an entry's Reconnect button to retry that
// specific upstream without touching the others.
function FailureBanner({
  failures,
  onReconnect,
}: {
  failures: Record<string, string>
  onReconnect: (name: string) => void
}) {
  const names = Object.keys(failures)
  return (
    <div className="failure-banner">
      <div className="failure-banner-head">
        <span className="failure-banner-icon" aria-hidden="true">!</span>
        <strong>
          {names.length} upstream{names.length === 1 ? '' : 's'} not running
        </strong>
      </div>
      <ul className="failure-banner-list">
        {names.map((n) => (
          <li key={n}>
            <code className="mono">{n}</code>: {failures[n]}
            <button
              className="failure-banner-retry"
              onClick={() => onReconnect(n)}
              title={`Retry connecting to ${n}`}
            >
              Reconnect
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ---- Workspace UI helpers ----

function WorkspaceBar(props: {
  workspaces: Workspace[]
  current: string
  onSelect: (name: string) => void
  onNew: () => void
  onDelete: (name: string) => void
  onUpdateWorkspace: (oldName: string, newName: string, cwd: string[]) => Promise<boolean>
  onShowSnippet: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [editingAutoDetect, setEditingAutoDetect] = useState(false)
  const activeWorkspace = props.workspaces.find((p) => p.name === props.current)
  return (
    <div className="workspace-bar" data-tour="workspace-bar">
      <label className="workspace-bar-label">Workspace</label>
      <WorkspaceSelect
        value={props.current}
        workspaces={props.workspaces}
        onSelect={props.onSelect}
        onNew={props.onNew}
      />

      <button
        className="btn-secondary"
        onClick={props.onShowSnippet}
        title={
          activeWorkspace
            ? `Show the client config snippet for "${activeWorkspace.name}"`
            : 'Show how to point Claude / Cursor / Zed / Copilot at Aeolus'
        }
        data-tour="connect-client"
      >
        Connect a client →
      </button>

      {activeWorkspace && (
        <>
          <AutoDetectChip
            workspace={activeWorkspace}
            onEdit={() => setEditingAutoDetect(true)}
          />
          <button
            className="btn-secondary"
            onClick={() => setEditing(true)}
            title="Rename or delete this workspace"
          >
            Edit workspace
          </button>
        </>
      )}

      {editing && activeWorkspace && (
        <EditWorkspaceModal
          workspace={activeWorkspace}
          existingNames={props.workspaces.map((p) => p.name)}
          onClose={() => setEditing(false)}
          onSave={async (newName) => {
            const cwd = activeWorkspace.cwd_match ?? []
            const ok = await props.onUpdateWorkspace(activeWorkspace.name, newName, cwd)
            if (ok) setEditing(false)
          }}
          onDelete={() => {
            setEditing(false)
            props.onDelete(activeWorkspace.name)
          }}
        />
      )}

      {editingAutoDetect && activeWorkspace && (
        <AutoDetectModal
          workspace={activeWorkspace}
          onClose={() => setEditingAutoDetect(false)}
          onSave={async (cwd) => {
            const ok = await props.onUpdateWorkspace(activeWorkspace.name, activeWorkspace.name, cwd)
            if (ok) setEditingAutoDetect(false)
          }}
        />
      )}
    </div>
  )
}

function WorkspaceSelect(props: {
  value: string
  workspaces: Workspace[]
  onSelect: (name: string) => void
  onNew: () => void
}) {
  const options = [
    { value: '', label: 'All upstreams' },
    ...props.workspaces.map((p) => ({ value: p.name, label: p.name })),
  ]
  return (
    <Dropdown
      value={props.value}
      options={options}
      onChange={props.onSelect}
      width={240}
      footer={
        <DropdownAction
          label="+ New workspace…"
          accent
          onClick={props.onNew}
        />
      }
    />
  )
}

function AutoDetectChip(props: { workspace: Workspace; onEdit: () => void }) {
  const patterns = props.workspace.cwd_match ?? []
  if (patterns.length === 0) {
    return (
      <button
        type="button"
        className="auto-detect-chip auto-detect-chip-empty"
        onClick={props.onEdit}
        title="Set folder patterns so this workspace applies automatically by cwd — no per-project config edits needed."
      >
        + Set auto-detect path
      </button>
    )
  }
  const first = patterns[0]
  const rest = patterns.length - 1
  return (
    <button
      type="button"
      className="auto-detect-chip"
      onClick={props.onEdit}
      title={`Workspace applies automatically when running in:\n${patterns.join('\n')}`}
    >
      <span className="auto-detect-icon">↳</span>
      <span className="mono">{first}</span>
      {rest > 0 && <span className="muted">+{rest} more</span>}
    </button>
  )
}

// Guided product tour for first-run. The "dismissed" flag is
// persisted on the server (~/.local/share/aeolus/dashboard_state.json)
// via DashboardStateProvider, so it survives browser restarts and
// switching browsers — better than localStorage which dies on
// clear-site-data or cross-browser use.

function TourController() {
  // The "dismissed" flag lives in the server-side dashboard state, so
  // it survives cross-browser visits / cleared site data. The Help
  // button writes a sessionStorage "tourPending" flag *and* fires an
  // event; we consume both so the open-from-another-tab path works
  // before TourController re-mounts.
  const { state: dashState, update: updateDashState } = useDashboardState()
  const [open, setOpen] = useState(false)

  // Auto-open on first visit once state has loaded and dismiss is
  // false. Won't re-fire after dismiss because dashState.tour_dismissed
  // flips to true.
  useEffect(() => {
    if (dashState === null) return
    if (sessionStorage.getItem('aeolus.tourPending') === '1') {
      sessionStorage.removeItem('aeolus.tourPending')
      setOpen(true)
      return
    }
    if (!dashState.tour_dismissed && !open) {
      setOpen(true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dashState])

  useEffect(() => {
    function handler() {
      sessionStorage.removeItem('aeolus.tourPending')
      setOpen(true)
    }
    window.addEventListener('aeolus:open-tour', handler)
    return () => window.removeEventListener('aeolus:open-tour', handler)
  }, [])

  function close() {
    setOpen(false)
    updateDashState({ tour_dismissed: true })
  }

  // Purely informational steps. The user can't act on the page
  // underneath while the tour is up — they read each step, advance
  // with Next/Back, then Finish or Skip to actually do things.
  const steps: TourStep[] = [
    {
      target: 'add-upstream',
      title: 'Add your first MCP server',
      body: (
        <>
          <p>
            An <em>upstream</em> is any MCP server you want Aeolus to expose
            — filesystem, github, your own database, anything that speaks
            MCP.
          </p>
          <p>
            The highlighted <strong>+ Add upstream</strong> button opens a
            modal where you can paste a server config or pick one from the
            Catalog (2,600+ entries). Aeolus probes it to discover its tools.
          </p>
        </>
      ),
      placement: 'bottom',
    },
    {
      target: 'connect-client',
      title: 'Connect a client',
      body: (
        <>
          <p>
            Once you have a server, point your MCP client at Aeolus. The
            highlighted <strong>Connect a client →</strong> button shows the
            exact snippet for Claude / Cursor / VS Code Copilot / Zed and
            where to paste it.
          </p>
          <p>
            One config line and every server you've configured becomes
            available in the client.
          </p>
        </>
      ),
      placement: 'bottom',
    },
    {
      target: 'workspace-bar',
      title: 'Optionally: scope by project',
      body: (
        <>
          <p>
            Workspaces let you expose different servers per project —
            <code className="mono"> github + db-prod</code> in one repo,{' '}
            <code className="mono">github + db-staging</code> in another.
            Aeolus picks the right one automatically based on the directory
            the client is running from.
          </p>
          <p>
            The highlighted dropdown is where you create and switch
            workspaces. Skip if you only need one shared set.
          </p>
        </>
      ),
      placement: 'bottom',
    },
    {
      target: 'live-tab',
      title: 'Watch tool calls live',
      body: (
        <>
          <p>
            The highlighted <strong>Live</strong> tab streams every tool
            call the moment your MCP client makes one. You'll see the
            upstream, tool, latency, status, and which client made the call.
          </p>
          <p>
            Click any row to expand it and inspect the JSON arguments and
            response — useful for debugging "why did Claude call that tool"
            or "what did that server return."
          </p>
        </>
      ),
      placement: 'right',
    },
    {
      target: null,
      title: "You're set",
      body: (
        <>
          <p>
            That's the core loop. Add servers, connect a client, optionally
            scope by project, watch calls in the Live tab.
          </p>
          <p>
            Use the <strong>Help</strong> button at the bottom of the left
            sidebar to re-open this tour anytime.
          </p>
        </>
      ),
    },
  ]

  return <Tour steps={steps} open={open} onClose={close} />
}

function CwdExamples() {
  return (
    <div className="field-hint cwd-examples">
      <p>
        Aeolus uses this to pick the workspace automatically based on the directory
        your MCP client is running from. No <code>--workspace</code> flag needed in
        any client config.
      </p>
      <table className="cwd-examples-table">
        <thead>
          <tr>
            <th>Pattern</th>
            <th>Matches</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td><code>~/code/acme-backend</code></td>
            <td>Only when running in exactly <code>~/code/acme-backend</code>.</td>
          </tr>
          <tr>
            <td><code>~/code/acme-backend/**</code></td>
            <td>
              That directory <em>and any subdirectory</em>. e.g. opening Claude in{' '}
              <code>~/code/acme-backend/services/api</code> still picks this workspace.
            </td>
          </tr>
          <tr>
            <td><code>~/work/**</code></td>
            <td>Every project under <code>~/work</code>.</td>
          </tr>
          <tr>
            <td>
              <code>~/code/foo</code><br />
              <code>~/code/bar/**</code>
            </td>
            <td>One pattern per line — match if <em>any</em> hits.</td>
          </tr>
        </tbody>
      </table>
      <p className="muted">
        Leave blank if you'd rather wire the workspace in explicitly via the
        snippet under "Connect a client".
      </p>
    </div>
  )
}

function NewWorkspaceModal(props: {
  existing: string[]
  onClose: () => void
  onCreate: (name: string, cwdMatch: string[]) => Promise<boolean>
}) {
  const [name, setName] = useState('')
  const [cwd, setCwd] = useState('')
  const [saving, setSaving] = useState(false)
  const trimmed = name.trim()
  const conflict = props.existing.includes(trimmed)
  const valid = trimmed.length > 0 && !conflict

  async function submit() {
    if (!valid || saving) return
    setSaving(true)
    try {
      await props.onCreate(trimmed, cwd.trim() ? [cwd.trim()] : [])
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-shade" onClick={saving ? undefined : props.onClose}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>New workspace</h2>
          <button
            className="modal-close"
            onClick={props.onClose}
            disabled={saving}
            aria-label="Close"
          >
            ×
          </button>
        </header>
        <div className="modal-body">
          <label className="field">
            <span className="field-label">Name</span>
            <input
              className={`text-input ${conflict ? 'text-input-error' : ''}`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. project-a"
              autoFocus
              disabled={saving}
            />
            {conflict && (
              <span className="field-warning">A workspace named &ldquo;{trimmed}&rdquo; already exists.</span>
            )}
          </label>
          <label className="field">
            <span className="field-label">Auto-detect by project folder (optional)</span>
            <input
              className="text-input"
              value={cwd}
              onChange={(e) => setCwd(e.target.value)}
              placeholder="~/code/acme-backend/**"
              disabled={saving}
            />
            <CwdExamples />
          </label>
        </div>
        <footer className="modal-footer">
          <button className="btn-secondary" onClick={props.onClose} disabled={saving}>Cancel</button>
          <button
            className="btn-primary"
            disabled={!valid || saving}
            onClick={submit}
          >
            {saving ? (
              <>
                <Spinner /> Creating…
              </>
            ) : (
              'Create workspace'
            )}
          </button>
        </footer>
      </div>
    </div>
  )
}

function EditWorkspaceModal(props: {
  workspace: Workspace
  existingNames: string[]
  onClose: () => void
  onSave: (newName: string) => Promise<void> | void
  onDelete: () => void
}) {
  const [name, setName] = useState(props.workspace.name)
  const [saving, setSaving] = useState(false)

  const trimmed = name.trim()
  const conflict =
    trimmed !== '' &&
    trimmed !== props.workspace.name &&
    props.existingNames.includes(trimmed)
  const valid = trimmed !== '' && !conflict

  async function submit() {
    if (!valid || saving) return
    setSaving(true)
    try {
      await props.onSave(trimmed)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-shade" onClick={saving ? undefined : props.onClose}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>
            Edit workspace:{' '}
            <span className="workspace-name-truncate">{props.workspace.name}</span>
          </h2>
          <button className="modal-close" onClick={props.onClose} disabled={saving} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          <label className="field">
            <span className="field-label">Name</span>
            <input
              className={`text-input ${conflict ? 'text-input-error' : ''}`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. project-a"
              disabled={saving}
            />
            {conflict && (
              <span className="field-warning">
                A workspace named &ldquo;{trimmed}&rdquo; already exists.
              </span>
            )}
            <span className="field-hint">
              Renaming updates this workspace's entry in <code>aeolus.yaml</code>.{' '}
              If you've already wired up client config files with an explicit
              {' '}<code>--workspace</code> flag, update those to the new name too.
              {' '}Auto-detect paths live in their own dialog — click the chip
              next to the workspace selector.
            </span>
          </label>
        </div>
        <footer className="modal-footer">
          <button className="btn-danger" onClick={props.onDelete} disabled={saving}>Delete workspace</button>
          <div style={{ flex: 1 }} />
          <button className="btn-secondary" onClick={props.onClose} disabled={saving}>Cancel</button>
          <button
            className="btn-primary"
            disabled={!valid || saving}
            onClick={submit}
          >
            {saving ? (
              <>
                <Spinner /> Saving…
              </>
            ) : (
              'Save'
            )}
          </button>
        </footer>
      </div>
    </div>
  )
}

function AutoDetectModal(props: {
  workspace: Workspace
  onClose: () => void
  onSave: (cwd: string[]) => Promise<void> | void
}) {
  const [cwd, setCwd] = useState((props.workspace.cwd_match ?? []).join('\n'))
  const [saving, setSaving] = useState(false)

  async function submit() {
    if (saving) return
    setSaving(true)
    try {
      await props.onSave(
        cwd
          .split('\n')
          .map((s) => s.trim())
          .filter(Boolean),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-shade" onClick={saving ? undefined : props.onClose}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>
            Auto-detect for{' '}
            <span className="workspace-name-truncate">{props.workspace.name}</span>
          </h2>
          <button className="modal-close" onClick={props.onClose} disabled={saving} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          <label className="field">
            <span className="field-label">Project folders</span>
            <textarea
              className="text-input"
              rows={4}
              value={cwd}
              onChange={(e) => setCwd(e.target.value)}
              placeholder={'~/code/acme-backend/**\n~/work/acme/**'}
              autoFocus
              disabled={saving}
            />
            <CwdExamples />
          </label>
        </div>
        <footer className="modal-footer">
          <button className="btn-secondary" onClick={props.onClose} disabled={saving}>Cancel</button>
          <button className="btn-primary" disabled={saving} onClick={submit}>
            {saving ? (
              <>
                <Spinner /> Saving…
              </>
            ) : (
              'Save'
            )}
          </button>
        </footer>
      </div>
    </div>
  )
}

// Snippet generators. workspaceName == null produces the global form
// (no --workspace arg) — used in the getting-started flow before the
// user has created any workspace.
function buildArgs(workspaceName: string | null): string[] {
  return workspaceName ? ['mcp', '--workspace', workspaceName] : ['mcp']
}

function claudeSnippet(workspaceName: string | null): string {
  return JSON.stringify(
    {
      mcpServers: {
        aeolus: { command: 'aeolus', args: buildArgs(workspaceName) },
      },
    },
    null,
    2,
  )
}

function vscodeSnippet(workspaceName: string | null): string {
  return JSON.stringify(
    {
      servers: {
        aeolus: { type: 'stdio', command: 'aeolus', args: buildArgs(workspaceName) },
      },
    },
    null,
    2,
  )
}

function zedSnippet(workspaceName: string | null): string {
  return JSON.stringify(
    {
      context_servers: {
        aeolus: { command: { path: 'aeolus', args: buildArgs(workspaceName) } },
      },
    },
    null,
    2,
  )
}

type ClientChoice = 'claude' | 'cursor' | 'vscode' | 'zed'

const clientChoices: {
  id: ClientChoice
  label: string
  // Path varies for global vs project-scoped configs. For Claude we
  // recommend the user-global path in global mode; project path
  // otherwise (so per-project --workspace lands in the right file).
  scopedPath: string
  globalPath: string
  sample: (w: string | null) => string
}[] = [
  {
    id: 'claude',
    label: 'Claude (Code / Desktop)',
    scopedPath: '<project-root>/.claude/settings.local.json',
    globalPath: '~/.claude.json',
    sample: claudeSnippet,
  },
  {
    id: 'cursor',
    label: 'Cursor',
    scopedPath: '<project-root>/.cursor/mcp.json',
    globalPath: '~/.cursor/mcp.json',
    sample: claudeSnippet,
  },
  {
    id: 'vscode',
    label: 'GitHub Copilot (VS Code)',
    scopedPath: '<project-root>/.vscode/mcp.json',
    globalPath: '<VS Code user settings.json>',
    sample: vscodeSnippet,
  },
  {
    id: 'zed',
    label: 'Zed',
    scopedPath: '<project-root>/.zed/settings.json',
    globalPath: '~/.config/zed/settings.json',
    sample: zedSnippet,
  },
]

function AddExistingMembersModal(props: {
  workspace: Workspace
  allUpstreams: Upstream[]
  onClose: () => void
  onAdd: (names: string[]) => Promise<void>
}) {
  const included = new Set(props.workspace.include ?? [])
  // Only upstreams that aren't already in this workspace.
  const candidates = props.allUpstreams.filter((u) => !included.has(u.name))
  const [picked, setPicked] = useState<Set<string>>(() => new Set())
  const [saving, setSaving] = useState(false)
  const [query, setQuery] = useState('')

  const q = query.trim().toLowerCase()
  const visible = q
    ? candidates.filter(
        (u) =>
          u.name.toLowerCase().includes(q) ||
          (u.transport ?? 'stdio').toLowerCase().includes(q) ||
          (u.command ?? '').toLowerCase().includes(q) ||
          (u.url ?? '').toLowerCase().includes(q),
      )
    : candidates

  function toggle(name: string) {
    setPicked((prev) => {
      const out = new Set(prev)
      if (out.has(name)) out.delete(name)
      else out.add(name)
      return out
    })
  }

  return (
    <div className="modal-shade" onClick={props.onClose}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>Add servers to "{props.workspace.name}"</h2>
          <button className="modal-close" onClick={props.onClose} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          {candidates.length === 0 ? (
            <div className="muted">
              Every configured upstream is already in this workspace.
            </div>
          ) : (
            <>
              <p className="muted" style={{ marginTop: 0 }}>
                Check the existing upstreams you want this workspace to expose.
                You can also create a brand-new one with <em>+ Add upstream</em>.
              </p>
              <input
                type="search"
                className="text-input"
                placeholder="Search by name, command, or URL…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                autoFocus
              />
              <div className="add-existing-list">
                {visible.map((u) => {
                  const checked = picked.has(u.name)
                  return (
                    <label key={u.name} className={`add-existing-row ${checked ? 'add-existing-row-on' : ''}`}>
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggle(u.name)}
                      />
                      <span className="mono">{u.name}</span>
                      <span className="muted add-existing-transport">
                        {u.transport === 'http' ? 'http' : 'stdio'}
                      </span>
                    </label>
                  )
                })}
                {visible.length === 0 && (
                  <div className="add-existing-empty">
                    No upstreams match &ldquo;{query}&rdquo;.
                  </div>
                )}
              </div>
            </>
          )}
        </div>
        <footer className="modal-footer">
          <button className="btn-secondary" onClick={props.onClose} disabled={saving}>Cancel</button>
          <button
            className="btn-primary"
            disabled={picked.size === 0 || saving}
            onClick={async () => {
              setSaving(true)
              await props.onAdd(Array.from(picked))
              setSaving(false)
            }}
          >
            {saving
              ? 'Adding…'
              : picked.size === 0
                ? 'Add'
                : `Add ${picked.size} server${picked.size === 1 ? '' : 's'}`}
          </button>
        </footer>
      </div>
    </div>
  )
}

function ClientConfigModal(props: {
  // null = global mode (no workspace scoping) — used by GettingStarted
  // before the user has any workspaces.
  workspace: Workspace | null
  onClose: () => void
}) {
  const [choice, setChoice] = useState<ClientChoice>('claude')
  const [copied, setCopied] = useState(false)
  const active = clientChoices.find((c) => c.id === choice)!
  const workspaceName = props.workspace?.name ?? null
  const snippet = active.sample(workspaceName)
  const patterns = props.workspace?.cwd_match ?? []
  const hasAutoDetect = patterns.length > 0
  const isGlobal = props.workspace === null

  async function copy() {
    try {
      await navigator.clipboard.writeText(snippet)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* ignore — user can select + copy manually */
    }
  }

  return (
    <div className="modal-shade" onClick={props.onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>
            {isGlobal
              ? 'Connect a client'
              : `Connect a client to "${props.workspace!.name}"`}
          </h2>
          <button className="modal-close" onClick={props.onClose} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          {isGlobal ? (
            <div className="callout">
              Paste this into your client's user-global config. Aeolus will expose
              every upstream that isn't scoped to a workspace. Add workspaces
              later if you want per-project tool sets.
            </div>
          ) : hasAutoDetect ? (
            <div className="callout callout-success">
              <strong>Auto-detect is on for this workspace.</strong>{' '}
              <span>
                It applies automatically whenever an MCP client launches from
                inside:
              </span>
              <ul className="callout-list">
                {patterns.map((p) => (
                  <li key={p}><code className="mono">{p}</code></li>
                ))}
              </ul>
              <span>
                If your global Claude / Cursor / Zed config already has{' '}
                <code className="mono">aeolus mcp</code> (without{' '}
                <code className="mono">--workspace</code>), you're done — no
                per-project config file needed. The snippet below is only for
                cases where you want to wire this workspace in explicitly.
              </span>
            </div>
          ) : (
            <div className="callout">
              <strong>Tip:</strong> set <em>Auto-detect by project folder</em>{' '}
              on this workspace and Aeolus will pick it automatically based on
              where the client launches from — no per-project config edits
              needed. <em>Edit workspace</em> → "Auto-detect by project folder".
            </div>
          )}

          <div className="field">
            <span className="field-label">Client</span>
            <div className="segmented">
              {clientChoices.map((c) => (
                <button
                  key={c.id}
                  type="button"
                  className={`segment ${choice === c.id ? 'segment-active' : ''}`}
                  onClick={() => setChoice(c.id)}
                >
                  {c.label}
                </button>
              ))}
            </div>
          </div>

          {choice === 'claude' && !isGlobal && (
            <div className="callout callout-warn">
              <strong>Claude Desktop caveat:</strong> Claude Desktop is launched
              from the macOS dock, not a terminal, so its working directory is
              wherever the .app was opened from (usually <code className="mono">/</code>) — cwd
              auto-detect won't reliably match. For Desktop, either accept the
              global unscoped set or pass <code className="mono">--workspace</code>{' '}
              explicitly via the snippet below. <strong>Claude Code (CLI)</strong>,
              Cursor, VS Code, and Zed all spawn from the project directory, so
              auto-detect works.
            </div>
          )}

          <div className="field">
            <span className="field-label">File path</span>
            <code className="mono client-snippet-path">
              {isGlobal ? active.globalPath : active.scopedPath}
            </code>
          </div>

          <div className="field">
            <span className="field-label">Snippet</span>
            <pre className="client-snippet">{snippet}</pre>
          </div>
        </div>
        <footer className="modal-footer">
          <button className="btn-secondary" onClick={props.onClose}>Close</button>
          <button className="btn-primary" onClick={copy}>
            {copied ? '✓ Copied' : 'Copy to clipboard'}
          </button>
        </footer>
      </div>
    </div>
  )
}
