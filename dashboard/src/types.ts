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
