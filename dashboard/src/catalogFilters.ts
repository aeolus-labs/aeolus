import type { CatalogEntry } from './types'

// Filters on top of the MCP Registry's published metadata. We currently
// only filter on dimensions the registry actually exposes:
// - transport: derived from packages (stdio) vs remotes (http)
// - auth: derived from declared environmentVariables / headers
// We deliberately don't have an "official" filter because the registry
// has no "verified publisher" signal — every entry is community-published.

export type CatalogFilters = {
  transport: 'all' | 'stdio' | 'http'
  auth: 'all' | 'none' | 'required'
}

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
    return true
  })
}
