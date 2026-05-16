import { useMemo, useState, type ReactNode } from 'react'
import type { AeolusConfig, CatalogEntry, Upstream } from './types'

type Source = 'catalog' | 'custom'

type ProbedTool = { name: string; description?: string }

type Props = {
  config: AeolusConfig
  catalog: CatalogEntry[]
  editing?: Upstream // when present, modal opens in edit mode pre-filled
  onClose: () => void
  onSaved: (next: AeolusConfig) => void
}

export default function AddUpstream({ config, catalog, editing, onClose, onSaved }: Props) {
  const isEdit = !!editing

  const [source, setSource] = useState<Source>('catalog')
  const [step, setStep] = useState<'pick' | 'probe' | 'tools'>(isEdit ? 'probe' : 'pick')

  const [name, setName] = useState(editing?.name ?? '')
  const [command, setCommand] = useState(editing?.command ?? '')
  const [args, setArgs] = useState(editing?.args?.join('\n') ?? '')
  const [envEntries, setEnvEntries] = useState<Array<{ key: string; value: string }>>(parseEnv(editing?.env))
  const [catalogSearch, setCatalogSearch] = useState('')

  const [probedTools, setProbedTools] = useState<ProbedTool[] | null>(null)
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set())

  const [probing, setProbing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const filteredCatalog = useMemo(() => {
    const q = catalogSearch.toLowerCase().trim()
    if (!q) return catalog.slice(0, 40)
    return catalog
      .filter((e) =>
        e.name.toLowerCase().includes(q) ||
        e.description.toLowerCase().includes(q) ||
        e.id.toLowerCase().includes(q)
      )
      .slice(0, 40)
  }, [catalog, catalogSearch])

  function applyCatalog(entry: CatalogEntry) {
    const slug = lastSegment(entry.id) || entry.name
    setName(slug)
    setCommand(entry.command)
    setArgs(entry.args.join('\n'))
    setEnvEntries(
      Object.entries(entry.env ?? {}).map(([key, value]) => ({ key, value: value.startsWith('{{') ? '' : value }))
    )
    setStep('probe')
  }

  async function probe() {
    setError(null)
    setProbing(true)
    setProbedTools(null)
    try {
      const env: Record<string, string> = {}
      for (const e of envEntries) if (e.key) env[e.key] = e.value
      const r = await fetch('/api/probe', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          command,
          args: argsLines(args),
          env,
        }),
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
      const newUpstream: Upstream = {
        name,
        command,
        args: argsLines(args),
      }
      const envSlice = envEntries
        .filter((e) => e.key)
        .map((e) => `${e.key}=${e.value}`)
      if (envSlice.length > 0) newUpstream.env = envSlice

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
            onPick={applyCatalog}
          />
        )}

        {(step === 'probe' || step === 'tools') && (
          <FormStep
            name={name}
            onName={setName}
            command={command}
            onCommand={setCommand}
            args={args}
            onArgs={setArgs}
            envEntries={envEntries}
            onEnvEntries={setEnvEntries}
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
              <button className="btn-primary" onClick={probe} disabled={probing || !command}>
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

function FormStep(props: {
  name: string
  onName: (s: string) => void
  command: string
  onCommand: (s: string) => void
  args: string
  onArgs: (s: string) => void
  envEntries: Array<{ key: string; value: string }>
  onEnvEntries: (e: Array<{ key: string; value: string }>) => void
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
    </div>
  )
}

function EnvEditor({
  entries,
  onChange,
}: {
  entries: Array<{ key: string; value: string }>
  onChange: (e: Array<{ key: string; value: string }>) => void
}) {
  return (
    <div className="env-editor">
      {entries.map((e, i) => (
        <div key={i} className="env-row">
          <input
            className="text-input mono env-key"
            value={e.key}
            placeholder="KEY"
            onChange={(ev) => {
              const next = [...entries]
              next[i] = { ...e, key: ev.target.value }
              onChange(next)
            }}
          />
          <input
            className="text-input mono env-value"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={e.value}
            placeholder="value"
            onChange={(ev) => {
              const next = [...entries]
              next[i] = { ...e, value: ev.target.value }
              onChange(next)
            }}
          />
          <button
            className="btn-icon"
            onClick={() => onChange(entries.filter((_, j) => j !== i))}
            aria-label="Remove"
          >
            ×
          </button>
        </div>
      ))}
      <button
        className="btn-secondary"
        onClick={() => onChange([...entries, { key: '', value: '' }])}
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

function parseEnv(env?: string[]): Array<{ key: string; value: string }> {
  if (!env) return []
  return env.map((s) => {
    const i = s.indexOf('=')
    if (i < 0) return { key: s, value: '' }
    return { key: s.slice(0, i), value: s.slice(i + 1) }
  })
}

function errMsg(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}
