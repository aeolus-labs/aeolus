export type ToolCallStatus = 'ok' | 'error' | 'transport_error'

export type ToolCallEvent = {
  time: string
  upstream: string
  tool: string
  latency_ms: number
  status: ToolCallStatus
  client?: string
  workspace?: string
  arguments?: unknown
  response?: unknown
}

export type Stats = {
  upstreams: string[]
  total: number
  errors: number
}

export type Upstream = {
  name: string
  transport?: string
  /** Missing = enabled. Explicit false = configured but not initialized. */
  enabled?: boolean
  command?: string
  args?: string[]
  env?: string[]
  url?: string
  headers?: Record<string, string>
}

export type Tools = {
  allow?: string[]
  deny?: string[]
}

export type Dashboard = {
  enabled: boolean
  addr: string
}

export type Log = {
  level: string
  format: string
}

export type Workspace = {
  name: string
  include?: string[]
  cwd_match?: string[]
  tools?: Tools
}

export type AeolusConfig = {
  upstreams: Upstream[]
  tools: Tools
  log: Log
  dashboard: Dashboard
  workspaces?: Workspace[]
}

export type CatalogEntry = {
  id: string
  name: string
  description: string
  transport?: string
  command?: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  headers?: Record<string, string>
  repository?: string
  website?: string
  notes?: string
}
