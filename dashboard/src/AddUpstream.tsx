import { useEffect, useMemo, useState, type ReactNode } from 'react'
import type { AeolusConfig, CatalogEntry, Upstream } from './types'
import { applyCatalogFilters, type CatalogFilters } from './catalogFilters'

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
  const [step, setStep] = useState<'pick' | 'probe' | 'tools'>(
    isEdit || prefill ? 'probe' : 'pick'
  )

  const [name, setName] = useState(editing?.name ?? '')
  const [transport, setTransport] = useState<'stdio' | 'http'>(
    (editing?.transport === 'http' ? 'http' : 'stdio')
  )
  const [command, setCommand] = useState(editing?.command ?? '')
  const [args, setArgs] = useState(editing?.args?.join('\n') ?? '')
  const [envEntries, setEnvEntries] = useState<EnvRow[]>(parseEnv(editing?.env))
  const [url, setUrl] = useState(editing?.url ?? '')
  const [headerEntries, setHeaderEntries] = useState<EnvRow[]>(parseHeaders(editing?.headers))
  const [catalogSearch, setCatalogSearch] = useState('')
  const [catalogFilters, setCatalogFilters] = useState<CatalogFilters>({
    transport: 'all',
    auth: 'all',
    source: 'all',
  })

  const [probedTools, setProbedTools] = useState<ProbedTool[] | null>(null)
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set())

  const [probing, setProbing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const filteredCatalog = useMemo(
    () => applyCatalogFilters(catalog, catalogSearch, catalogFilters).slice(0, 40),
    [catalog, catalogSearch, catalogFilters]
  )

  // If we opened with a catalog prefill, apply it once on mount. Safe because
  // applyCatalog only touches state setters and is idempotent for the same entry.
  useEffect(() => {
    if (prefill) {
      applyCatalog(prefill)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function applyCatalog(entry: CatalogEntry) {
    const slug = lastSegment(entry.id) || entry.name
    setName(slug)
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
    setStep('probe')
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
      setSelectedTools(new Set(data.tools.map((t) => t.name)))
      setStep('tools')
    } catch (err: unknown) {
      setError(errMsg(err))
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

      // Strip any existing allow rules that apply to this upstream so we can
      // replace them cleanly from the new tool selection.
      const oldName = editing?.name ?? name
      const allowSansSelf = stripUpstreamRules(config.tools.allow, oldName)
      const otherUpstreams = nextUpstreams.filter((u) => u.name !== name)

      const next: AeolusConfig = {
        ...config,
        upstreams: nextUpstreams,
        tools: {
          allow: addAllowRules(allowSansSelf, name, selectedTools, probedTools ?? [], otherUpstreams),
          deny: config.tools.deny ?? [],
        },
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

        {!isEdit && (
          <div className="modal-tabs">
            <button
              className={`tab ${source === 'catalog' ? 'tab-active' : ''}`}
              onClick={() => { setSource('catalog'); setStep('pick') }}
            >
              From catalog
            </button>
            <button
              className={`tab ${source === 'custom' ? 'tab-active' : ''}`}
              onClick={() => { setSource('custom'); setStep('probe') }}
            >
              Custom
            </button>
          </div>
        )}

        {error && <div className="modal-error">{error}</div>}

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

        {(step === 'probe' || step === 'tools') && (
          <FormStep
            name={name}
            onName={setName}
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
          />
        )}

        {step === 'tools' && probedTools && (
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

        {(step === 'probe' || step === 'tools') && (
          <footer className="modal-footer">
            <button className="btn-secondary" onClick={onClose} disabled={saving}>Cancel</button>
            {step === 'probe' && (
              <button
                className="btn-primary"
                onClick={probe}
                disabled={probing || (transport === 'stdio' ? !command : !url)}
              >
                {probing ? 'Probing…' : 'Probe tools'}
              </button>
            )}
            {step === 'tools' && (
              <>
                <button className="btn-secondary" onClick={() => setStep('probe')} disabled={saving}>
                  Re-probe
                </button>
                <button className="btn-primary" onClick={save} disabled={saving}>
                  {saving ? 'Saving…' : `Save (${selectedTools.size} tools)`}
                </button>
              </>
            )}
          </footer>
        )}
      </div>
    </div>
  )
}

function CatalogStep(props: {
  catalog: CatalogEntry[]
  search: string
  onSearch: (s: string) => void
  filters: CatalogFilters
  onFilters: (f: CatalogFilters) => void
  onPick: (e: CatalogEntry) => void
}) {
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
      <div className="catalog-list">
        {props.catalog.map((e) => (
          <button key={e.id} className="catalog-pick" onClick={() => props.onPick(e)}>
            <div className="catalog-pick-name">{e.name}</div>
            <div className="catalog-pick-desc">{e.description}</div>
            <div className="catalog-pick-id mono">{e.id}</div>
          </button>
        ))}
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
      <div className="segmented filter-segmented">
        <button type="button" className={`segment ${filters.source === 'all' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, source: 'all' })}>All</button>
        <button type="button" className={`segment ${filters.source === 'official' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, source: 'official' })}>Official</button>
        <button type="button" className={`segment ${filters.source === 'community' ? 'segment-active' : ''}`} onClick={() => onChange({ ...filters, source: 'community' })}>Community</button>
      </div>
    </div>
  )
}

function FormStep(props: {
  name: string
  onName: (s: string) => void
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
}) {
  return (
    <div className="modal-body">
      <Field label="Name" hint="Tools will be exposed as name.tool_name">
        <input
          className="text-input"
          value={props.name}
          onChange={(e) => props.onName(e.target.value)}
          placeholder="e.g. filesystem"
        />
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
