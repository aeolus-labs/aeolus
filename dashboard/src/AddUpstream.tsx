import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { AeolusConfig, CatalogEntry, Upstream } from './types'
import { applyCatalogFilters, type CatalogFilters } from './catalogFilters'
import { clearKnownBad, markKnownBad } from './knownBad'

type Source = 'catalog' | 'custom'

type ProbedTool = { name: string; description?: string }

type Props = {
  config: AeolusConfig
  catalog: CatalogEntry[]
  editing?: Upstream // edit mode: pre-fill from existing upstream
  prefill?: CatalogEntry // open with this catalog entry pre-applied
  onClose: () => void
  onSaved: (next: AeolusConfig) => void
}

export default function AddUpstream({ config, catalog, editing, prefill, onClose, onSaved }: Props) {
  const isEdit = !!editing

  const [source, setSource] = useState<Source>(prefill ? 'custom' : 'catalog')
  const [step, setStep] = useState<'pick' | 'form'>(
    isEdit || prefill ? 'form' : 'pick'
  )
  const [modalTab, setModalTab] = useState<'setup' | 'tools'>('setup')

  const [name, setName] = useState(editing?.name ?? '')
  const [transport, setTransport] = useState<'stdio' | 'http'>(
    (editing?.transport === 'http' ? 'http' : 'stdio')
  )
  const [command, setCommand] = useState(editing?.command ?? '')
  const [args, setArgs] = useState(editing?.args?.join('\n') ?? '')
  const [envEntries, setEnvEntries] = useState<EnvRow[]>(parseEnv(editing?.env))
  const [url, setUrl] = useState(editing?.url ?? '')
  const [headerEntries, setHeaderEntries] = useState<EnvRow[]>(parseHeaders(editing?.headers))
  // Workspace membership: which workspaces include this upstream. Edits
  // here are persisted along with the rest of the upstream's config on
  // save, so the user picks everything in one place.
  const workspacesList = config.workspaces ?? []
  const initialMemberships = new Set<string>(
    workspacesList
      .filter((p) =>
        editing
          ? (p.include ?? []).includes(editing.name)
          : false,
      )
      .map((p) => p.name),
  )
  const [workspaceMemberships, setWorkspaceMemberships] = useState<Set<string>>(initialMemberships)
  const [catalogSearch, setCatalogSearch] = useState('')
  const [catalogFilters, setCatalogFilters] = useState<CatalogFilters>({
    transport: 'all',
    auth: 'all',
  })

  const [probedTools, setProbedTools] = useState<ProbedTool[] | null>(null)
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set())

  const [probing, setProbing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pendingAutoProbe, setPendingAutoProbe] = useState(false)

  // Live conflict check — every other upstream is considered "taken"
  // except the one we're editing. Reads as true when there's a real
  // collision so we can light up the inline warning and the suggest
  // button.
  const nameConflict = (() => {
    const trimmed = name.trim()
    if (!trimmed) return false
    return config.upstreams.some(
      (u) => u.name === trimmed && u.name !== editing?.name
    )
  })()

  const filteredCatalog = useMemo(
    () => applyCatalogFilters(catalog, catalogSearch, catalogFilters),
    [catalog, catalogSearch, catalogFilters]
  )

  // If we opened with a catalog prefill, apply it once on mount and
  // auto-probe whenever the entry doesn't require any auth — saves a click
  // for "just want to try this server" entries (filesystem, memory, etc.).
  useEffect(() => {
    if (prefill) {
      applyCatalog(prefill)
      const needsAuth =
        (prefill.env && Object.keys(prefill.env).length > 0) ||
        (prefill.headers && Object.keys(prefill.headers).length > 0)
      if (!needsAuth) {
        setPendingAutoProbe(true)
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Fire the auto-probe on the next render — by then state setters from
  // applyCatalog have flushed so probe() reads the right values.
  useEffect(() => {
    if (pendingAutoProbe && !probing && !probedTools) {
      setPendingAutoProbe(false)
      probe()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingAutoProbe])

  function applyCatalog(entry: CatalogEntry) {
    const slug = lastSegment(entry.id) || entry.name
    setName(uniqueUpstreamName(slug, config.upstreams.map((u) => u.name)))
    const t = entry.transport === 'http' ? 'http' : 'stdio'
    setTransport(t)
    if (t === 'http') {
      setUrl(entry.url ?? '')
      setHeaderEntries(
        Object.entries(entry.headers ?? {}).map(([key, value]) => ({
          key,
          value: value.startsWith('{{') ? '' : value,
          secure: false,
        }))
      )
      setCommand('')
      setArgs('')
      setEnvEntries([])
    } else {
      setCommand(entry.command ?? '')
      setArgs((entry.args ?? []).join('\n'))
      setEnvEntries(
        Object.entries(entry.env ?? {}).map(([key, value]) => ({
          key,
          value: value.startsWith('{{') ? '' : value,
          secure: false,
        }))
      )
      setUrl('')
      setHeaderEntries([])
    }
    setStep('form')
    setModalTab('setup')
  }

  async function probe() {
    setError(null)
    setProbing(true)
    setProbedTools(null)
    try {
      const body: Record<string, unknown> = { transport }
      if (transport === 'stdio') {
        body.command = command
        body.args = argsLines(args)
        const env: Record<string, string> = {}
        for (const e of envEntries) if (e.key) env[e.key] = e.value
        body.env = env
      } else {
        body.url = url
        const headers: Record<string, string> = {}
        for (const e of headerEntries) if (e.key) headers[e.key] = e.value
        body.headers = headers
      }
      const r = await fetch('/api/probe', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!r.ok) throw new Error(await r.text())
      const data = (await r.json()) as { tools: ProbedTool[] }
      setProbedTools(data.tools)
      // Pre-select tools to match the upstream's current allow/deny
      // state. For brand-new upstreams (no rules yet reference this
      // name) we fall back to "select all" — the user is just adding
      // a server and probably wants everything.
      const allow = config.tools?.allow ?? []
      const deny = config.tools?.deny ?? []
      const prefix = `${name}.`
      const hasRulesForThis =
        allow.some((p) => p === `${name}.*` || p.startsWith(prefix)) ||
        deny.some((p) => p === `${name}.*` || p.startsWith(prefix))
      if (hasRulesForThis) {
        setSelectedTools(
          new Set(
            data.tools
              .filter((t) => isToolAllowed(`${name}.${t.name}`, allow, deny))
              .map((t) => t.name),
          ),
        )
      } else {
        setSelectedTools(new Set(data.tools.map((t) => t.name)))
      }
      setModalTab('tools')
      if (prefill) clearKnownBad(prefill.id)
    } catch (err: unknown) {
      setError(errMsg(err))
      // Only mark the catalog entry as known-bad if the user actually
      // supplied every required env/header it declared. If they left
      // a required field empty, the probe failure is a user-side
      // missing-input, not a catalog defect.
      if (prefill && !hasMissingRequiredAuth(prefill, envEntries, headerEntries)) {
        markKnownBad(prefill.id)
      }
    } finally {
      setProbing(false)
    }
  }

  async function save() {
    if (!name) {
      setError('Name is required')
      return
    }
    const dupName =
      !isEdit && config.upstreams.some((u) => u.name === name) ||
      isEdit && name !== editing?.name && config.upstreams.some((u) => u.name === name)
    if (dupName) {
      setError(`An upstream named "${name}" already exists`)
      return
    }
    setSaving(true)
    setError(null)
    try {
      const newUpstream: Upstream = { name, transport }
      if (transport === 'stdio') {
        newUpstream.command = command
        newUpstream.args = argsLines(args)
        const envSlice = await processSecretRows(envEntries, `${name}`)
        if (envSlice.length > 0) {
          newUpstream.env = envSlice.map(([k, v]) => `${k}=${v}`)
        }
      } else {
        if (!url) throw new Error('URL is required for http transport')
        newUpstream.url = url
        const headerKVs = await processSecretRows(headerEntries, `${name}.headers`)
        if (headerKVs.length > 0) {
          const headersMap: Record<string, string> = {}
          for (const [k, v] of headerKVs) headersMap[k] = v
          newUpstream.headers = headersMap
        }
      }

      const nextUpstreams = isEdit
        ? config.upstreams.map((u) => (u.name === editing!.name ? newUpstream : u))
        : [...config.upstreams, newUpstream]

      // If the user re-probed in this session we replace the upstream's
      // allow rules from the new selection; otherwise we leave them
      // untouched so a Workspaces-only edit doesn't accidentally wipe
      // previously-curated tool rules.
      const oldName = editing?.name ?? name
      const reprobed = probedTools !== null
      const allowSansSelf = reprobed
        ? stripUpstreamRules(config.tools.allow, oldName)
        : (config.tools.allow ?? [])
      const otherUpstreams = nextUpstreams.filter((u) => u.name !== name)

      // Apply workspace memberships from the modal: rewrite each workspace's
      // include list to add or remove this upstream (using the new
      // name) based on the user's checkbox state.
      const oldUpstreamName = editing?.name ?? name
      const nextWorkspaces = workspacesList.map((p) => {
        const cur = p.include ?? []
        // Strip any prior reference to this upstream (covers both
        // rename and toggle-off).
        const without = cur.filter((n) => n !== oldUpstreamName && n !== name)
        const include = workspaceMemberships.has(p.name) ? [...without, name] : without
        return { ...p, include }
      })

      const next: AeolusConfig = {
        ...config,
        upstreams: nextUpstreams,
        tools: {
          allow: reprobed
            ? addAllowRules(allowSansSelf, name, selectedTools, probedTools ?? [], otherUpstreams)
            : allowSansSelf,
          deny: config.tools.deny ?? [],
        },
        workspaces: nextWorkspaces,
      }

      const r = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(next),
      })
      if (!r.ok) throw new Error(await r.text())
      const saved = (await r.json()) as AeolusConfig
      onSaved(saved)
    } catch (err: unknown) {
      setError(errMsg(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-shade" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>{isEdit ? `Edit upstream: ${editing!.name}` : 'Add upstream'}</h2>
          <button className="modal-close" onClick={onClose} aria-label="Close">×</button>
        </header>

        {!isEdit && !prefill && (
          <div className="modal-tabs">
            <button
              className={`tab ${source === 'catalog' ? 'tab-active' : ''}`}
              onClick={() => { setSource('catalog'); setStep('pick') }}
            >
              From catalog
            </button>
            <button
              className={`tab ${source === 'custom' ? 'tab-active' : ''}`}
              onClick={() => { setSource('custom'); setStep('form') }}
            >
              Custom
            </button>
          </div>
        )}
        {prefill && !isEdit && (
          <div className="modal-source-line">
            <span className="modal-source-label">From catalog:</span>{' '}
            <span className="mono">{prefill.id}</span>
          </div>
        )}

        {error && (
          <div className="modal-error">
            <span className="modal-error-text">{error}</span>
            <button
              className="modal-error-dismiss"
              onClick={() => setError(null)}
              aria-label="Dismiss error"
            >
              ×
            </button>
          </div>
        )}

        {source === 'catalog' && step === 'pick' && (
          <CatalogStep
            catalog={filteredCatalog}
            search={catalogSearch}
            onSearch={setCatalogSearch}
            filters={catalogFilters}
            onFilters={setCatalogFilters}
            onPick={applyCatalog}
          />
        )}

        {step === 'form' && (
          <>
            <div className="modal-tabs modal-inner-tabs">
              <button
                className={`tab ${modalTab === 'setup' ? 'tab-active' : ''}`}
                onClick={() => setModalTab('setup')}
              >
                Setup
              </button>
              <button
                className={`tab ${modalTab === 'tools' ? 'tab-active' : ''}`}
                onClick={() => {
                  setModalTab('tools')
                  // In edit mode, clicking Tools probes automatically the
                  // first time so the user sees a fresh tool list from the
                  // running upstream instead of having to click probe
                  // manually. For new upstreams without prefill, the user
                  // still drives the probe explicitly from Setup.
                  if (isEdit && !probedTools && !probing) {
                    void probe()
                  }
                }}
                disabled={!isEdit && !probedTools}
                title={!isEdit && !probedTools ? 'Probe first to discover tools' : ''}
              >
                Tools{probedTools ? ` (${selectedTools.size}/${probedTools.length})` : ''}
              </button>
            </div>

            {modalTab === 'setup' && (
              <FormStep
                name={name}
                onName={setName}
                nameConflict={nameConflict}
                onSuggestUniqueName={() =>
                  setName(uniqueUpstreamName(name, config.upstreams.map((u) => u.name)))
                }
                transport={transport}
                onTransport={setTransport}
                command={command}
                onCommand={setCommand}
                args={args}
                onArgs={setArgs}
                envEntries={envEntries}
                onEnvEntries={setEnvEntries}
                url={url}
                onUrl={setUrl}
                headerEntries={headerEntries}
                onHeaderEntries={setHeaderEntries}
                workspaces={workspacesList}
                memberships={workspaceMemberships}
                onToggleMembership={(workspaceName, next) => {
                  setWorkspaceMemberships((prev) => {
                    const out = new Set(prev)
                    if (next) out.add(workspaceName)
                    else out.delete(workspaceName)
                    return out
                  })
                }}
              />
            )}

            {modalTab === 'tools' && !probedTools && probing && (
              <div className="modal-body modal-body-empty">
                Probing tools…
              </div>
            )}
            {modalTab === 'tools' && !probedTools && !probing && (
              <div className="modal-body modal-body-empty">
                No tools loaded yet. Go to Setup and click Probe.
              </div>
            )}
            {modalTab === 'tools' && probedTools && (
              <ToolPicker
                tools={probedTools}
                selected={selectedTools}
                onToggle={(name) => {
                  setSelectedTools((prev) => {
                    const next = new Set(prev)
                    next.has(name) ? next.delete(name) : next.add(name)
                    return next
                  })
                }}
                onSelectAll={() => setSelectedTools(new Set(probedTools.map((t) => t.name)))}
                onSelectNone={() => setSelectedTools(new Set())}
              />
            )}

            <footer className="modal-footer">
              <button className="btn-secondary" onClick={onClose} disabled={saving}>Cancel</button>
              <button
                className={probedTools ? 'btn-secondary' : 'btn-primary'}
                onClick={probe}
                disabled={probing || (transport === 'stdio' ? !command : !url)}
              >
                {probing ? 'Probing…' : probedTools ? 'Re-probe' : 'Probe tools'}
              </button>
              {(probedTools || isEdit) && (
                <button className="btn-primary" onClick={save} disabled={saving}>
                  {saving
                    ? 'Saving…'
                    : probedTools
                      ? `Save (${selectedTools.size} tool${selectedTools.size === 1 ? '' : 's'})`
                      : 'Save'}
                </button>
              )}
            </footer>
          </>
        )}
      </div>
    </div>
  )
}

const CATALOG_PICK_PAGE_SIZE = 40

function CatalogStep(props: {
  catalog: CatalogEntry[]
  search: string
  onSearch: (s: string) => void
  filters: CatalogFilters
  onFilters: (f: CatalogFilters) => void
  onPick: (e: CatalogEntry) => void
}) {
  const [page, setPage] = useState(1)
  const visible = props.catalog.slice(0, page * CATALOG_PICK_PAGE_SIZE)
  const hasMore = visible.length < props.catalog.length

  // Reset to first page when the filters / search change.
  useEffect(() => {
    setPage(1)
  }, [props.search, props.filters.transport, props.filters.auth])

  // IntersectionObserver scoped to the .catalog-list scrolling container,
  // so the sentinel fires when scrolling *inside* the modal list (not
  // when the whole page scrolls).
  const listRef = useRef<HTMLDivElement | null>(null)
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!hasMore) return
    const list = listRef.current
    const sentinel = sentinelRef.current
    if (!list || !sentinel) return
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setPage((p) => p + 1)
          }
        }
      },
      { root: list, rootMargin: '120px' },
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [hasMore, visible.length])

  return (
    <div className="modal-body">
      <input
        type="search"
        className="search"
        placeholder="Search catalog..."
        value={props.search}
        onChange={(e) => props.onSearch(e.target.value)}
        autoFocus
      />
      <ModalCatalogFilterBar filters={props.filters} onChange={props.onFilters} />
      <div className="catalog-list" ref={listRef}>
        {visible.map((e) => (
          <button key={e.id} className="catalog-pick" onClick={() => props.onPick(e)}>
            <div className="catalog-pick-name">{e.name}</div>
            <div className="catalog-pick-desc">{e.description}</div>
            <div className="catalog-pick-id mono">{e.id}</div>
          </button>
        ))}
        {hasMore && (
          <div ref={sentinelRef} className="catalog-sentinel">
            Loading more…
          </div>
        )}
        {!hasMore && visible.length > 0 && (
          <div className="catalog-sentinel catalog-sentinel-done">
            {visible.length} entries
          </div>
        )}
      </div>
    </div>
  )
}

function ModalCatalogFilterBar({
  filters,
  onChange,
}: {
  filters: CatalogFilters
  onChange: (f: CatalogFilters) => void
}) {
  return (
    <div className="catalog-filters">
      <div className="segmented filter-segmented">
        <button type="button" className={`segment ${filters.transport === 'all' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, transport: 'all' })}>All</button>
        <button type="button" className={`segment ${filters.transport === 'stdio' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, transport: 'stdio' })}>stdio</button>
        <button type="button" className={`segment ${filters.transport === 'http' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, transport: 'http' })}>http</button>
      </div>
      <div className="segmented filter-segmented">
        <button type="button" className={`segment ${filters.auth === 'all' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, auth: 'all' })}>Any auth</button>
        <button type="button" className={`segment ${filters.auth === 'none' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, auth: 'none' })}>No auth</button>
        <button type="button" className={`segment ${filters.auth === 'required' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, auth: 'required' })}>Auth req.</button>
      </div>
    </div>
  )
}

function FormStep(props: {
  name: string
  onName: (s: string) => void
  nameConflict: boolean
  onSuggestUniqueName: () => void
  transport: 'stdio' | 'http'
  onTransport: (t: 'stdio' | 'http') => void
  command: string
  onCommand: (s: string) => void
  args: string
  onArgs: (s: string) => void
  envEntries: EnvRow[]
  onEnvEntries: (e: EnvRow[]) => void
  url: string
  onUrl: (s: string) => void
  headerEntries: EnvRow[]
  onHeaderEntries: (e: EnvRow[]) => void
  workspaces: { name: string }[]
  memberships: Set<string>
  onToggleMembership: (name: string, next: boolean) => void
}) {
  return (
    <div className="modal-body">
      <Field label="Name" hint="Tools will be exposed as name.tool_name">
        <input
          className={`text-input ${props.nameConflict ? 'text-input-error' : ''}`}
          value={props.name}
          onChange={(e) => props.onName(e.target.value)}
          placeholder="e.g. filesystem"
          aria-invalid={props.nameConflict}
        />
        {props.nameConflict && (
          <div className="field-warning">
            An upstream named &ldquo;{props.name}&rdquo; already exists.{' '}
            <button
              type="button"
              className="field-warning-link"
              onClick={props.onSuggestUniqueName}
            >
              Use a unique name
            </button>
          </div>
        )}
      </Field>
      <Field label="Transport">
        <div className="segmented">
          <button
            type="button"
            className={`segment ${props.transport === 'stdio' ? 'segment-active' : ''}`}
            onClick={() => props.onTransport('stdio')}
          >
            Subprocess (stdio)
          </button>
          <button
            type="button"
            className={`segment ${props.transport === 'http' ? 'segment-active' : ''}`}
            onClick={() => props.onTransport('http')}
          >
            HTTP endpoint
          </button>
        </div>
      </Field>
      {props.transport === 'stdio' ? (
        <>
          <Field label="Command">
            <input
              className="text-input mono"
              value={props.command}
              onChange={(e) => props.onCommand(e.target.value)}
              placeholder="npx"
            />
          </Field>
          <Field label="Args" hint="One per line">
            <textarea
              className="text-input mono"
              rows={4}
              value={props.args}
              onChange={(e) => props.onArgs(e.target.value)}
              placeholder={`-y\n@modelcontextprotocol/server-filesystem\n/tmp`}
            />
          </Field>
          <Field label="Environment variables">
            <EnvEditor entries={props.envEntries} onChange={props.onEnvEntries} />
          </Field>
        </>
      ) : (
        <>
          <Field label="URL">
            <input
              className="text-input mono"
              value={props.url}
              onChange={(e) => props.onUrl(e.target.value)}
              placeholder="https://mcp.example.com/mcp"
            />
          </Field>
          <Field label="HTTP headers" hint="Authorization, X-Api-Key, etc.">
            <EnvEditor entries={props.headerEntries} onChange={props.onHeaderEntries} />
          </Field>
        </>
      )}
      {props.workspaces.length > 0 && (
        <Field
          label="Workspaces"
          hint="Scoped: only visible in the checked workspaces. Unchecked everywhere: global (visible when no workspace is active)."
        >
          <WorkspacePickerButton
            workspaces={props.workspaces}
            memberships={props.memberships}
            onToggleMembership={props.onToggleMembership}
          />
        </Field>
      )}
    </div>
  )
}

function EnvEditor({
  entries,
  onChange,
}: {
  entries: EnvRow[]
  onChange: (e: EnvRow[]) => void
}) {
  function update(i: number, patch: Partial<EnvRow>) {
    const next = [...entries]
    next[i] = { ...entries[i], ...patch }
    onChange(next)
  }

  return (
    <div className="env-editor">
      {entries.map((e, i) => {
        const isKeychainRef = e.value.startsWith('keychain:')
        const isMasked = e.value === '***'
        return (
          <div key={i} className="env-row">
            <input
              className="text-input mono env-key"
              value={e.key}
              placeholder="KEY"
              onChange={(ev) => update(i, { key: ev.target.value })}
            />
            <input
              className="text-input mono env-value"
              type={e.secure && !isKeychainRef ? 'password' : 'text'}
              autoComplete="off"
              spellCheck={false}
              value={e.value}
              placeholder={e.secure ? 'secret value' : 'value'}
              onChange={(ev) => update(i, { value: ev.target.value })}
            />
            <button
              className={`btn-icon ${e.secure ? 'btn-icon-active' : ''}`}
              onClick={() => {
                if (!e.secure && isMasked) return // disabled: must retype first
                const turningOff = e.secure
                update(i, {
                  secure: !e.secure,
                  // Clear keychain refs when turning off so the user retypes a plain value.
                  value: turningOff && isKeychainRef ? '' : e.value,
                })
              }}
              disabled={!e.secure && isMasked}
              title={
                e.secure
                  ? 'Secured in OS keychain — click to switch to plaintext'
                  : isMasked
                    ? 'Clear the value and re-type before securing'
                    : 'Click to store value in OS keychain'
              }
            >
              {e.secure ? '🔒' : '🔓'}
            </button>
            <button
              className="btn-icon"
              onClick={() => onChange(entries.filter((_, j) => j !== i))}
              aria-label="Remove"
            >
              ×
            </button>
          </div>
        )
      })}
      <button
        className="btn-secondary"
        onClick={() => onChange([...entries, { key: '', value: '', secure: false }])}
      >
        + Add variable
      </button>
    </div>
  )
}

function ToolPicker({
  tools,
  selected,
  onToggle,
  onSelectAll,
  onSelectNone,
}: {
  tools: ProbedTool[]
  selected: Set<string>
  onToggle: (name: string) => void
  onSelectAll: () => void
  onSelectNone: () => void
}) {
  return (
    <div className="modal-body">
      <div className="tools-header">
        <span>
          <strong>{selected.size}</strong> of {tools.length} selected
        </span>
        <span className="tools-actions">
          <button className="link" onClick={onSelectAll}>Select all</button>
          <button className="link" onClick={onSelectNone}>None</button>
        </span>
      </div>
      <div className="tool-list">
        {tools.map((t) => (
          <label key={t.name} className="tool-row">
            <input
              type="checkbox"
              checked={selected.has(t.name)}
              onChange={() => onToggle(t.name)}
            />
            <div>
              <div className="tool-row-name mono">{t.name}</div>
              {t.description && <div className="tool-row-desc">{t.description}</div>}
            </div>
          </label>
        ))}
      </div>
    </div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label className="field-label">
        {label}
        {hint && <span className="field-hint"> · {hint}</span>}
      </label>
      {children}
    </div>
  )
}

function WorkspacePickerButton(props: {
  workspaces: { name: string }[]
  memberships: Set<string>
  onToggleMembership: (name: string, next: boolean) => void
}) {
  const [open, setOpen] = useState(false)
  const checkedNames = props.workspaces
    .map((p) => p.name)
    .filter((n) => props.memberships.has(n))

  return (
    <>
      <button
        type="button"
        className="workspace-picker-button"
        onClick={() => setOpen(true)}
      >
        <span className="workspace-picker-button-label">
          {checkedNames.length === 0
            ? 'Global only — not in any workspace'
            : `In ${checkedNames.length} workspace${checkedNames.length === 1 ? '' : 's'}`}
        </span>
        {checkedNames.length > 0 && (
          <span className="workspace-picker-button-chips">
            {checkedNames.slice(0, 3).map((n) => (
              <span key={n} className="workspace-picker-button-chip mono" title={n}>
                {n}
              </span>
            ))}
            {checkedNames.length > 3 && (
              <span className="workspace-picker-button-chip muted">
                +{checkedNames.length - 3}
              </span>
            )}
          </span>
        )}
        <span className="workspace-picker-button-arrow">▾</span>
      </button>
      {open && (
        <WorkspacePickerModal
          workspaces={props.workspaces}
          memberships={props.memberships}
          onToggleMembership={props.onToggleMembership}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

function WorkspacePickerModal(props: {
  workspaces: { name: string }[]
  memberships: Set<string>
  onToggleMembership: (name: string, next: boolean) => void
  onClose: () => void
}) {
  const [query, setQuery] = useState('')
  const q = query.trim().toLowerCase()
  const visible = q
    ? props.workspaces.filter((p) => p.name.toLowerCase().includes(q))
    : props.workspaces
  const checkedCount = props.workspaces.filter((p) =>
    props.memberships.has(p.name),
  ).length

  return (
    <div className="modal-shade modal-shade-nested" onClick={props.onClose}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>Workspaces · {checkedCount} of {props.workspaces.length} selected</h2>
          <button className="modal-close" onClick={props.onClose} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          <input
            type="search"
            className="text-input"
            placeholder="Filter workspaces…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            autoFocus
          />
          <div className="workspace-picker-list">
            {visible.map((p) => {
              const checked = props.memberships.has(p.name)
              return (
                <label
                  key={p.name}
                  className={`workspace-picker-row ${checked ? 'workspace-picker-row-on' : ''}`}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={(e) => props.onToggleMembership(p.name, e.target.checked)}
                  />
                  <span className="mono workspace-picker-row-name" title={p.name}>
                    {p.name}
                  </span>
                </label>
              )
            })}
            {visible.length === 0 && (
              <div className="add-existing-empty">
                No workspaces match &ldquo;{query}&rdquo;.
              </div>
            )}
          </div>
        </div>
        <footer className="modal-footer">
          <button className="btn-primary" onClick={props.onClose}>
            Done
          </button>
        </footer>
      </div>
    </div>
  )
}

function argsLines(s: string): string[] {
  return s
    .split('\n')
    .map((x) => x.trim())
    .filter((x) => x.length > 0)
}

function lastSegment(id: string): string {
  const parts = id.split('/')
  return parts[parts.length - 1] || ''
}

// uniqueUpstreamName returns base unchanged if it's free, otherwise
// appends `-2`, `-3`, ... until it finds a name no existing upstream
// claims. Used both when prefilling from the catalog (so a second
// "github" entry lands as "github-2" instead of failing later at save)
// and as a one-click escape from the "name already exists" warning.
function uniqueUpstreamName(base: string, existing: string[]): string {
  const taken = new Set(existing)
  const seed = (base || 'upstream').trim() || 'upstream'
  if (!taken.has(seed)) return seed
  let i = 2
  while (taken.has(`${seed}-${i}`)) i++
  return `${seed}-${i}`
}

// isToolAllowed mirrors the backend ToolFilter semantics so the
// probe-result checkboxes can match what the engine actually allows.
// Deny wins. Empty allow means everything not denied is allowed.
// Otherwise allow is a whitelist. Patterns support trailing "*".
function isToolAllowed(exposedName: string, allow: string[], deny: string[]): boolean {
  for (const p of deny) if (globMatch(p, exposedName)) return false
  if (allow.length === 0) return true
  for (const p of allow) if (globMatch(p, exposedName)) return true
  return false
}

function globMatch(pattern: string, name: string): boolean {
  if (pattern === name) return true
  if (pattern.endsWith('*')) return name.startsWith(pattern.slice(0, -1))
  return false
}

function addAllowRules(
  existing: string[] | undefined,
  upstreamName: string,
  selected: Set<string>,
  all: ProbedTool[],
  otherUpstreams: Upstream[]
): string[] {
  const current = [...(existing ?? [])]

  // If user selected every tool, no constraints needed for this upstream.
  // Don't touch other upstreams either.
  if (selected.size === all.length) return current

  // Adding any allow rule activates strict whitelist mode globally. Preserve
  // "no constraints" for other upstreams by adding a `<name>.*` wildcard if
  // they don't already have a matching rule.
  for (const u of otherUpstreams) {
    if (u.name === upstreamName) continue
    const prefix = `${u.name}.`
    const hasRule = current.some((r) => r === `${u.name}.*` || r.startsWith(prefix))
    if (!hasRule) current.push(`${u.name}.*`)
  }

  const additions = Array.from(selected).map((n) => `${upstreamName}.${n}`)
  return [...current, ...additions]
}

// stripUpstreamRules removes any allow patterns that apply to a specific
// upstream — used when editing so the new tool selection cleanly replaces
// the old set.
function stripUpstreamRules(rules: string[] | undefined, upstreamName: string): string[] {
  if (!rules) return []
  const prefix = `${upstreamName}.`
  return rules.filter((r) => r !== `${upstreamName}.*` && !r.startsWith(prefix))
}

// EnvRow is the in-modal representation of one env var. `secure` is true
// when the value is (or will be on save) backed by the OS keychain.
type EnvRow = { key: string; value: string; secure: boolean }

// hasMissingRequiredAuth returns true when the catalog entry declared one
// or more required env / header keys but the user left at least one of
// them empty. Used to avoid marking a catalog entry as "known bad" when
// the probe failure is actually a user-side missing input.
function hasMissingRequiredAuth(
  entry: CatalogEntry,
  envRows: EnvRow[],
  headerRows: EnvRow[],
): boolean {
  const requiredEnv = new Set(Object.keys(entry.env ?? {}))
  const requiredHeaders = new Set(Object.keys(entry.headers ?? {}))
  if (requiredEnv.size === 0 && requiredHeaders.size === 0) return false
  for (const r of envRows) {
    if (requiredEnv.has(r.key) && !r.value) return true
  }
  for (const r of headerRows) {
    if (requiredHeaders.has(r.key) && !r.value) return true
  }
  return false
}

function parseEnv(env?: string[]): EnvRow[] {
  if (!env) return []
  return env.map((s) => {
    const i = s.indexOf('=')
    const key = i < 0 ? s : s.slice(0, i)
    const value = i < 0 ? '' : s.slice(i + 1)
    return { key, value, secure: value.startsWith('keychain:') }
  })
}

function parseHeaders(headers?: Record<string, string>): EnvRow[] {
  if (!headers) return []
  return Object.entries(headers).map(([key, value]) => ({
    key,
    value,
    secure: value.startsWith('keychain:'),
  }))
}

// processSecretRows POSTs secure values to /api/secrets and returns the
// resulting [key, value] pairs ready to be written to config. Plain rows
// are passed through unchanged. The namespace argument is used to build
// the keychain entry name (e.g. "<upstream>" for env, "<upstream>.headers"
// for HTTP headers) so the two never collide.
async function processSecretRows(rows: EnvRow[], namespace: string): Promise<Array<[string, string]>> {
  const out: Array<[string, string]> = []
  for (const e of rows) {
    if (!e.key) continue
    if (e.secure && !e.value.startsWith('keychain:')) {
      if (!e.value) {
        throw new Error(`Secure value for ${e.key} is empty`)
      }
      if (e.value === '***') {
        throw new Error(`Re-enter the value for ${e.key} before enabling secure storage`)
      }
      const secretName = `${namespace}.${e.key}`
      const r = await fetch(`/api/secrets/${encodeURIComponent(secretName)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ value: e.value }),
      })
      if (!r.ok) throw new Error(`Save ${e.key} to keychain failed: ${await r.text()}`)
      out.push([e.key, `keychain:${secretName}`])
    } else {
      out.push([e.key, e.value])
    }
  }
  return out
}

function errMsg(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}
