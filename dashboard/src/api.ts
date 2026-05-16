import type { AeolusConfig, CatalogEntry } from './types'

async function getJSON<T>(path: string): Promise<T> {
  const r = await fetch(path)
  if (!r.ok) throw new Error(`${path}: ${r.status} ${r.statusText}`)
  return r.json() as Promise<T>
}

export const api = {
  config: () => getJSON<AeolusConfig>('/api/config'),
  catalog: () => getJSON<CatalogEntry[]>('/api/catalog'),
}
