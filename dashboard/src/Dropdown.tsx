import { useEffect, useRef, useState, type ReactNode } from 'react'

export type DropdownOption = {
  value: string
  label: string
}

type Props = {
  value: string
  options: DropdownOption[]
  onChange: (value: string) => void
  // When provided, rendered below a divider at the bottom of the menu.
  // Used for actions like "+ New workspace…" that aren't selectable values.
  footer?: ReactNode
  // Optional placeholder shown when no option matches the current value.
  placeholder?: string
  // CSS width / max-width control. Defaults to 200px wide.
  width?: number | string
  className?: string
  title?: string
}

// Dropdown is a click-to-open menu component that replaces native
// <select>. Unlike <select>, we control the rendering of the option
// popup, which lets us truncate long labels with ellipsis and show
// tooltips. Use this everywhere a native select would appear.
export default function Dropdown(props: Props) {
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!open) return
    function onDown(e: MouseEvent) {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    function onEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onEsc)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onEsc)
    }
  }, [open])

  const current = props.options.find((o) => o.value === props.value)
  const label = current?.label ?? props.placeholder ?? ''
  const style = props.width !== undefined ? { width: props.width } : undefined

  return (
    <div
      className={`dropdown ${props.className ?? ''}`}
      ref={wrapRef}
      style={style}
    >
      <button
        type="button"
        className="dropdown-button"
        onClick={() => setOpen((v) => !v)}
        title={props.title ?? label}
      >
        <span className="dropdown-current">{label}</span>
        <span className="dropdown-arrow">▾</span>
      </button>
      {open && (
        <div className="dropdown-menu">
          {props.options.map((o) => (
            <DropdownItem
              key={o.value}
              label={o.label}
              selected={props.value === o.value}
              onClick={() => {
                props.onChange(o.value)
                setOpen(false)
              }}
            />
          ))}
          {props.footer && (
            <>
              <div className="dropdown-divider" />
              <div className="dropdown-footer" onClick={() => setOpen(false)}>
                {props.footer}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

function DropdownItem(props: {
  label: string
  selected?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      className={`dropdown-item ${props.selected ? 'dropdown-item-selected' : ''}`}
      onClick={props.onClick}
      title={props.label}
    >
      <span className="dropdown-item-label">{props.label}</span>
      {props.selected && <span className="dropdown-check">✓</span>}
    </button>
  )
}

// Convenience action item for use inside `footer`.
export function DropdownAction(props: {
  label: string
  onClick: () => void
  accent?: boolean
}) {
  return (
    <button
      type="button"
      className={`dropdown-item ${props.accent ? 'dropdown-item-accent' : ''}`}
      onClick={props.onClick}
      title={props.label}
    >
      <span className="dropdown-item-label">{props.label}</span>
    </button>
  )
}
