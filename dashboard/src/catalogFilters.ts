import type { CatalogEntry } from './types'

export type CatalogFilters = {
  transport: 'all' | 'stdio' | 'http'
  auth: 'all' | 'none' | 'required'
  source: 'all' | 'official' | 'community'
}

const OFFICIAL_NAMESPACE = 'io.github.modelcontextprotocol/'

export function applyCatalogFilters(
  catalog: CatalogEntry[],
  search: string,
  filters: CatalogFilters,
): CatalogEntry[] {
  const q = search.toLowerCase().trim()
  return catalog.filter((e) => {
    if (q) {
      const hit =
        e.name.toLowerCase().includes(q) ||
        e.description.toLowerCase().includes(q) ||
        e.id.toLowerCase().includes(q)
      if (!hit) return false
    }
    if (filters.transport !== 'all') {
      const t = e.transport === 'http' ? 'http' : 'stdio'
      if (t !== filters.transport) return false
    }
    if (filters.auth !== 'all') {
      const needsAuth =
        (e.env && Object.keys(e.env).length > 0) ||
        (e.headers && Object.keys(e.headers).length > 0)
      if (filters.auth === 'none' && needsAuth) return false
      if (filters.auth === 'required' && !needsAuth) return false
    }
    if (filters.source !== 'all') {
      const isOfficial = e.id.startsWith(OFFICIAL_NAMESPACE)
      if (filters.source === 'official' && !isOfficial) return false
      if (filters.source === 'community' && isOfficial) return false
    }
    return true
  })
}
