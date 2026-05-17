import { useEffect, useState, type ReactNode } from 'react'

export type ConfirmRequest = {
  title: string
  body: ReactNode
  // Confirm button copy. Defaults to "Confirm".
  confirmLabel?: string
  // Cancel button copy. Defaults to "Cancel".
  cancelLabel?: string
  // Confirm button style. "danger" → red, "primary" → accent.
  tone?: 'danger' | 'primary'
}

// useConfirm returns a tuple [confirm, dialog] where dialog is the
// React element you render once at the root of your component, and
// confirm(req) is an async function that resolves to true/false.
//
// Use this anywhere the codebase previously called the native
// window.confirm() — it gives a consistent, styled modal that matches
// the rest of the dashboard.
export function useConfirm(): [
  (req: ConfirmRequest) => Promise<boolean>,
  ReactNode,
] {
  const [pending, setPending] = useState<{
    req: ConfirmRequest
    resolve: (ok: boolean) => void
  } | null>(null)

  function confirm(req: ConfirmRequest): Promise<boolean> {
    return new Promise((resolve) => {
      setPending({ req, resolve })
    })
  }

  function close(ok: boolean) {
    if (pending) {
      pending.resolve(ok)
      setPending(null)
    }
  }

  // Escape cancels, Enter confirms.
  useEffect(() => {
    if (!pending) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') close(false)
      if (e.key === 'Enter') close(true)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pending])

  const dialog = pending ? (
    <div className="modal-shade" onClick={() => close(false)}>
      <div className="modal modal-narrow" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h2>{pending.req.title}</h2>
          <button className="modal-close" onClick={() => close(false)} aria-label="Close">×</button>
        </header>
        <div className="modal-body">
          <div className="confirm-body">{pending.req.body}</div>
        </div>
        <footer className="modal-footer">
          <button className="btn-secondary" onClick={() => close(false)}>
            {pending.req.cancelLabel ?? 'Cancel'}
          </button>
          <button
            className={pending.req.tone === 'danger' ? 'btn-danger' : 'btn-primary'}
            onClick={() => close(true)}
            autoFocus
          >
            {pending.req.confirmLabel ?? 'Confirm'}
          </button>
        </footer>
      </div>
    </div>
  ) : null

  return [confirm, dialog]
}
