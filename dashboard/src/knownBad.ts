// Tracks catalog entries that failed probe on this machine. Persisted in
// localStorage so the warning survives reload. Survives across sessions
// until the user successfully probes the same entry again.

const KEY = 'aeolus.knownBad'

export function loadKnownBad(): Set<string> {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return new Set()
    const arr = JSON.parse(raw) as string[]
    return new Set(arr)
  } catch {
    return new Set()
  }
}

function save(set: Set<string>): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(Array.from(set)))
  } catch {
    /* quota or disabled — best-effort only */
  }
}

export function markKnownBad(id: string): void {
  const s = loadKnownBad()
  if (s.has(id)) return
  s.add(id)
  save(s)
}

export function clearKnownBad(id: string): void {
  const s = loadKnownBad()
  if (!s.has(id)) return
  s.delete(id)
  save(s)
}
