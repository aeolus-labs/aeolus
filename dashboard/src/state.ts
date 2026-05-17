import { createContext, createElement, useContext, useEffect, useRef, useState, type ReactNode } from 'react'

// DashboardState mirrors the Go-side struct served at
// /api/dashboard/state. Small UI preferences only — anything
// user-managed lives in aeolus.yaml.
export type DashboardState = {
  tour_dismissed?: boolean
  sidebar_collapsed?: boolean
}

type Ctx = {
  // null while the initial fetch is in flight. Components can treat
  // null as "still loading, hold off on first-run behaviors."
  state: DashboardState | null
  update: (partial: DashboardState) => void
}

const DashboardStateContext = createContext<Ctx>({
  state: null,
  update: () => {},
})

// DashboardStateProvider fetches /api/dashboard/state on mount and
// exposes it via context. Writes go through `update`, which optimistic-
// merges into local state and PUTs the full new state to the server.
// Failures don't roll back — best-effort persistence.
export function DashboardStateProvider(props: { children: ReactNode }) {
  const [state, setState] = useState<DashboardState | null>(null)
  // Hold a ref to the latest state so update() can read it without
  // closure-capturing a stale value.
  const stateRef = useRef<DashboardState | null>(null)
  stateRef.current = state

  useEffect(() => {
    let cancelled = false
    fetch('/api/dashboard/state')
      .then((r) => (r.ok ? r.json() : {}))
      .then((d) => {
        if (!cancelled) setState(d as DashboardState)
      })
      .catch(() => {
        // Persistence not enabled or network failure — proceed with
        // an empty state so the UI doesn't get stuck loading.
        if (!cancelled) setState({})
      })
    return () => {
      cancelled = true
    }
  }, [])

  function update(partial: DashboardState) {
    const next: DashboardState = { ...(stateRef.current ?? {}), ...partial }
    setState(next)
    stateRef.current = next
    // Fire-and-forget. We've already optimistic-updated; if the PUT
    // fails the UI still reflects the user's intent and they'll see
    // it on next page load too (it just won't survive a hard refresh
    // until persistence is back).
    fetch('/api/dashboard/state', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(next),
    }).catch(() => {
      /* ignore */
    })
  }

  return createElement(
    DashboardStateContext.Provider,
    { value: { state, update } },
    props.children,
  )
}

export function useDashboardState(): Ctx {
  return useContext(DashboardStateContext)
}
