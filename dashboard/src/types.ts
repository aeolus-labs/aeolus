export type ToolCallStatus = 'ok' | 'error' | 'transport_error'

export type ToolCallEvent = {
  time: string
  upstream: string
  tool: string
  latency_ms: number
  status: ToolCallStatus
}

export type Stats = {
  upstreams: string[]
  total: number
  errors: number
}

export type Upstream = {
  name: string
  command?: string
  args?: string[]
  env?: string[]
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

export type AeolusConfig = {
  upstreams: Upstream[]
  tools: Tools
  log: Log
  dashboard: Dashboard
}

export type CatalogEntry = {
  id: string
  name: string
  description: string
  command: string
  args: string[]
  env?: Record<string, string>
  notes?: string
}
