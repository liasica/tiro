import type { RecordStatus } from './api'

// 样式工具与状态元数据

export function cx(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(' ')
}

export const inputClass =
  'w-full rounded-lg border border-line-strong bg-surface px-3 text-sm text-text placeholder:text-faint transition-[border-color,box-shadow] duration-150 outline-none focus:border-accent focus:shadow-[0_0_0_3px_var(--accent-soft)] disabled:opacity-60'

export const statusMeta: Record<RecordStatus, { label: string; tone: string; live?: boolean }> = {
  pending: { label: '排队中', tone: 'text-warn' },
  processing: { label: '生成中', tone: 'text-accent', live: true },
  done: { label: '已生成', tone: 'text-ok' },
  failed: { label: '失败', tone: 'text-err' },
  skipped: { label: '已跳过', tone: 'text-faint' },
}

// 生成按钮文案随状态变化
export function generateLabel(status: RecordStatus) {
  return status === 'failed' ? '重试' : status === 'skipped' ? '生成' : '重新生成'
}

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

export function buttonClass(variant: ButtonVariant, size: 'sm' | 'md', className?: string) {
  return cx(
    'inline-flex shrink-0 items-center justify-center gap-1.5 rounded-lg font-medium whitespace-nowrap transition-[background-color,border-color,color,box-shadow] duration-150 disabled:cursor-not-allowed disabled:opacity-45',
    size === 'sm' ? 'h-7 px-2.5 text-[13px]' : 'h-9 px-3.5 text-sm',
    variant === 'primary' && 'bg-accent text-on-accent shadow-[0_0_0_1px_var(--accent),0_6px_20px_-8px_var(--glow)] hover:bg-accent-strong',
    variant === 'secondary' && 'border border-line-strong bg-surface text-text hover:border-accent/50 hover:bg-raised',
    variant === 'ghost' && 'text-muted hover:bg-raised hover:text-text',
    variant === 'danger' && 'border border-line-strong text-err hover:border-err/50 hover:bg-err/10',
    className,
  )
}
