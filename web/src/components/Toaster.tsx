import { CircleAlert, CircleCheck } from 'lucide-react'
import { useCallback, useRef, useState, type ReactNode } from 'react'
import { ToastContext, type ToastTone } from '../lib/toast'
import { cx } from '../lib/ui'

interface Item {
  id: number
  message: string
  tone: ToastTone
}

export function Toaster({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([])
  const seq = useRef(0)

  const push = useCallback((message: string, tone: ToastTone = 'ok') => {
    const id = ++seq.current
    setItems((list) => [...list.slice(-2), { id, message, tone }])
    window.setTimeout(() => setItems((list) => list.filter((i) => i.id !== id)), tone === 'err' ? 5000 : 2800)
  }, [])

  return (
    <ToastContext value={push}>
      {children}
      <div className="pointer-events-none fixed inset-x-0 bottom-5 z-[60] flex flex-col items-center gap-2 px-4" aria-live="polite">
        {items.map((t) => (
          <div
            key={t.id}
            className="anim-rise pointer-events-auto flex max-w-md items-center gap-2 rounded-lg border border-line-strong bg-surface px-3.5 py-2 text-[13px] shadow-(--shadow)"
          >
            {t.tone === 'ok' ? <CircleCheck className="size-4 text-ok" /> : <CircleAlert className="size-4 text-err" />}
            <span className={cx(t.tone === 'err' && 'text-err')}>{t.message}</span>
          </div>
        ))}
      </div>
    </ToastContext>
  )
}
