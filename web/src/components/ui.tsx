import { Loader2, X } from 'lucide-react'
import { useEffect, useState, type AnchorHTMLAttributes, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import type { RecordStatus } from '../lib/api'
import { buttonClass, cx, inputClass, statusMeta, type ButtonVariant } from '../lib/ui'

// 文字标识：Tiro 标志加衬线斜体 tiro
export function Wordmark({ size = 'md' }: { size?: 'md' | 'lg' }) {
  const lg = size === 'lg'
  return (
    <span className={cx('inline-flex items-center select-none', lg ? 'gap-1.5' : 'gap-1')} aria-label="tiro">
      <img src="/logo.svg" alt="" className={lg ? 'size-[56px]' : 'size-[34px]'} draggable={false} />
      <span className={cx('font-serif italic leading-none tracking-tight text-text', lg ? 'text-[44px]' : 'text-[26px]')}>tiro</span>
    </span>
  )
}

export function Button({
  variant = 'secondary',
  size = 'md',
  loading,
  className,
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: 'sm' | 'md'; loading?: boolean }) {
  return (
    <button
      type="button"
      disabled={disabled || loading}
      className={buttonClass(variant, size, className)}
      {...rest}
    >
      {loading && <Loader2 className="size-3.5 animate-spin" />}
      {children}
    </button>
  )
}

// 外链按钮：样式同 Button，新窗口打开
export function LinkButton({
  variant = 'secondary',
  size = 'md',
  className,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: ButtonVariant; size?: 'sm' | 'md' }) {
  return <a target="_blank" rel="noreferrer" className={buttonClass(variant, size, className)} {...rest} />
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cx(inputClass, 'h-9', className)} {...rest} />
}

export function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={cx(inputClass, 'block py-2.5 leading-relaxed', className)} {...rest} />
}

export function Field({ label, hint, children, htmlFor }: { label: string; hint?: ReactNode; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="block text-[13px] font-medium text-muted">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs leading-relaxed text-faint">{hint}</p>}
    </div>
  )
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cx('size-4 animate-spin text-faint', className)} />
}

export function Avatar({ src, name, size = 24 }: { src?: string; name: string; size?: number }) {
  const [failed, setFailed] = useState(false)
  const style = { width: size, height: size }
  if (!src || failed) {
    return (
      <span
        style={{ ...style, fontSize: Math.round(size * 0.44) }}
        className="inline-grid shrink-0 place-items-center rounded-full bg-accent-soft font-medium text-accent"
      >
        {name.slice(0, 1) || '?'}
      </span>
    )
  }
  return (
    <img
      src={src}
      alt=""
      style={style}
      className="shrink-0 rounded-full object-cover ring-1 ring-line"
      onError={() => setFailed(true)}
      referrerPolicy="no-referrer"
    />
  )
}

export function StatusBadge({ status }: { status: RecordStatus }) {
  const m = statusMeta[status]
  return (
    <span className={cx('inline-flex items-center gap-1.5 text-[13px] whitespace-nowrap', m.tone)}>
      <span className={cx('size-1.5 rounded-full bg-current', m.live && 'pulse-dot')} />
      {m.label}
    </span>
  )
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  className,
}: {
  value: T
  options: { value: T; label: string }[]
  onChange: (v: T) => void
  className?: string
}) {
  return (
    <div className={cx('inline-flex h-9 rounded-lg border border-line bg-surface p-0.5', className)} role="tablist">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="tab"
          aria-selected={value === o.value}
          onClick={() => onChange(o.value)}
          className={cx(
            'h-full rounded-md px-3 text-sm whitespace-nowrap transition-colors duration-150',
            value === o.value ? 'bg-raised text-text shadow-[inset_0_0_0_1px_var(--line-strong)]' : 'text-muted hover:text-text',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function Switch({ checked, onChange, disabled, label }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors duration-150 disabled:opacity-45',
        checked ? 'border-accent bg-accent' : 'border-line-strong bg-raised',
      )}
    >
      <span
        className={cx(
          'size-3.5 rounded-full shadow transition-transform duration-150',
          checked ? 'translate-x-[18px] bg-on-accent' : 'translate-x-[2px] bg-muted',
        )}
      />
    </button>
  )
}

function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', onKey)
      document.body.style.overflow = prev
    }
  }, [onClose])
}

export function Modal({ title, onClose, children, footer }: { title: string; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  useEscape(onClose)
  return createPortal(
    <div className="fixed inset-0 z-50 grid place-items-center p-4">
      <div className="anim-fade absolute inset-0 bg-black/50 backdrop-blur-[2px]" onClick={onClose} />
      <div role="dialog" aria-modal="true" aria-label={title} className="anim-rise relative w-full max-w-md rounded-xl border border-line-strong bg-surface shadow-(--shadow)">
        <div className="flex items-center justify-between px-5 pt-4">
          <h2 className="text-[15px] font-semibold">{title}</h2>
          <button type="button" onClick={onClose} className="rounded-md p-1 text-faint hover:bg-raised hover:text-text" aria-label="关闭">
            <X className="size-4" />
          </button>
        </div>
        <div className="px-5 py-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-line px-5 py-3">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

export function Drawer({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  useEscape(onClose)
  return createPortal(
    <div className="fixed inset-0 z-40 flex justify-end">
      <div className="anim-fade absolute inset-0 bg-black/40" onClick={onClose} />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={label}
        className="anim-drawer relative flex h-full w-full max-w-[680px] flex-col border-l border-line-strong bg-bg shadow-(--shadow)"
      >
        {children}
      </aside>
    </div>,
    document.body,
  )
}

export function ErrorLine({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const msg = error instanceof Error ? error.message : String(error)
  return (
    <div className="flex items-center justify-between gap-3 rounded-lg border border-err/30 bg-err/8 px-3.5 py-2.5 text-[13px] text-err">
      <span>{msg}</span>
      {onRetry && (
        <Button size="sm" variant="ghost" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  )
}

export function PageHeader({ title, actions }: { title: string; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <h1 className="text-[22px] font-semibold tracking-tight">{title}</h1>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}
