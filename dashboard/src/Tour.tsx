import { useEffect, useLayoutEffect, useState, type ReactNode } from 'react'

export type TourStep = {
  // CSS selector or `data-tour` value to spotlight. When null, the
  // tooltip floats centered with no spotlight.
  target: string | null
  title: string
  body: ReactNode
  // Where to place the tooltip relative to the target. Defaults to
  // "bottom" if there's room, otherwise "top".
  placement?: 'top' | 'bottom' | 'left' | 'right'
}

type Props = {
  steps: TourStep[]
  // Whether the tour is currently active.
  open: boolean
  // Called when the user dismisses or finishes the tour. Outside-click
  // does NOT trigger this — only the Skip / Finish buttons do — so a
  // misclick doesn't lose the tour.
  onClose: () => void
}

// Tour renders a fully-modal dimmed overlay with a "spotlight" cutout
// around the current step's target element, plus a positioned tooltip
// that explains what to look at. The tour is purely informational:
// clicks on the page underneath are blocked, so the user reads each
// step and advances with Next / Back / Skip. Once they Skip or Finish,
// they can interact with the page normally.
export default function Tour(props: Props) {
  const [stepIdx, setStepIdx] = useState(0)
  const [rect, setRect] = useState<DOMRect | null>(null)

  const step: TourStep | undefined = props.open ? props.steps[stepIdx] : undefined

  // Reset to step 0 whenever the tour re-opens.
  useEffect(() => {
    if (props.open) setStepIdx(0)
  }, [props.open])

  // Track the target rect. Critical that the first update is
  // *synchronous* inside useLayoutEffect so React re-renders with the
  // new rect before the browser paints — otherwise the spotlight
  // flashes at the previous step's position for one frame on
  // Next/Back. After the initial placement, an rAF loop keeps the
  // spotlight in sync with any layout shifts (e.g., upstreams
  // animating in).
  useLayoutEffect(() => {
    if (!step || step.target === null) {
      setRect(null)
      return
    }

    const targetSel = step.target
    const initial = resolveTarget(targetSel)
    setRect(initial ? initial.getBoundingClientRect() : null)

    let raf = 0
    function tick() {
      const el = resolveTarget(targetSel)
      if (el) {
        const r = el.getBoundingClientRect()
        setRect((prev) => {
          if (
            prev &&
            prev.left === r.left &&
            prev.top === r.top &&
            prev.width === r.width &&
            prev.height === r.height
          ) {
            return prev
          }
          return r
        })
      } else {
        setRect(null)
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [step])

  // Escape to skip the whole tour.
  useEffect(() => {
    if (!props.open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') props.onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [props.open, props.onClose])

  if (!step) return null

  const isFirst = stepIdx === 0
  const isLast = stepIdx >= props.steps.length - 1
  const tooltip = positionTooltip(rect, step.placement)

  return (
    <>
      {/* Full-screen shade that also blocks every underlying click —
          the tour is read-only while open, so the user can't get into
          a confused half-state. Click-on-shade does NOTHING; only the
          explicit Skip button closes the tour. */}
      <div className="tour-shade" />
      {rect && (
        <div
          className="tour-spotlight"
          style={{
            left: rect.left - 8,
            top: rect.top - 8,
            width: rect.width + 16,
            height: rect.height + 16,
          }}
        />
      )}
      <div className="tour-tooltip" style={tooltip}>
        <div className="tour-tooltip-step">
          Step {stepIdx + 1} of {props.steps.length}
        </div>
        <h3 className="tour-tooltip-title">{step.title}</h3>
        <div className="tour-tooltip-body">{step.body}</div>
        <div className="tour-tooltip-footer">
          <button className="tour-skip" onClick={props.onClose}>
            Skip tour
          </button>
          <div className="tour-tooltip-actions">
            <button
              className="btn-secondary"
              onClick={() => setStepIdx(stepIdx - 1)}
              disabled={isFirst}
            >
              Back
            </button>
            <button
              className="btn-primary"
              onClick={() => {
                if (isLast) props.onClose()
                else setStepIdx(stepIdx + 1)
              }}
            >
              {isLast ? 'Finish' : 'Next →'}
            </button>
          </div>
        </div>
      </div>
    </>
  )
}

function resolveTarget(sel: string): Element | null {
  try {
    const el = document.querySelector(sel)
    if (el) return el
  } catch {
    /* fall through */
  }
  return document.querySelector(`[data-tour="${cssEscape(sel)}"]`)
}

function cssEscape(s: string): string {
  return s.replace(/"/g, '\\"')
}

function positionTooltip(
  rect: DOMRect | null,
  placement: TourStep['placement'],
): React.CSSProperties {
  const margin = 14
  const tooltipMaxWidth = 380

  if (!rect) {
    return {
      left: '50%',
      top: '50%',
      transform: 'translate(-50%, -50%)',
      maxWidth: tooltipMaxWidth,
    }
  }

  const vw = window.innerWidth
  const vh = window.innerHeight
  const place: TourStep['placement'] =
    placement ?? (rect.bottom + 220 < vh ? 'bottom' : 'top')

  let left = rect.left
  let top = rect.bottom + margin
  if (place === 'top') {
    top = rect.top - margin - 220
  } else if (place === 'left') {
    left = rect.left - tooltipMaxWidth - margin
    top = rect.top
  } else if (place === 'right') {
    left = rect.right + margin
    top = rect.top
  }

  left = Math.max(12, Math.min(left, vw - tooltipMaxWidth - 12))
  top = Math.max(12, Math.min(top, vh - 240))

  return { left, top, maxWidth: tooltipMaxWidth }
}
