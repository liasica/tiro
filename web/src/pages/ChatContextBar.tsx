import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query'
import { CalendarRange, Files, X } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { Button, ErrorLine, Field, Input, Modal, Segmented, Spinner, StatusBadge } from '../components/ui'
import type { ChatContext, ContextMinute, MinuteRecord, Scope } from '../lib/api'
import { api } from '../lib/api'
import { MAX_CONTEXT_MINUTES, rangeLabel, rangePresets } from '../lib/chat'
import { formatTime } from '../lib/format'
import { cx } from '../lib/ui'

const PICKER_PAGE_SIZE = 20

// 输入框上方的上下文选择：范围、时间段、手动选择的妙记
export function ContextBar({
  admin,
  context,
  minutes,
  onChange,
}: {
  admin: boolean
  context: ChatContext
  minutes: ContextMinute[]
  onChange: (context: ChatContext, minutes?: ContextMinute[]) => void
}) {
  const [dialog, setDialog] = useState<'range' | 'minutes' | null>(null)
  const hasRange = context.start_date !== '' || context.end_date !== ''
  const count = context.minute_ids.length

  // 已选妙记按 minute_ids 的顺序还原，缺少标题的条目只保留编号
  const selected = context.minute_ids.map((id) => minutes.find((m) => m.id === id) ?? { id, title: '', minute_created_at: null })

  return (
    <div className="flex flex-wrap items-center gap-2">
      {admin && (
        <Segmented<Scope>
          value={context.scope}
          onChange={(scope) => onChange({ ...context, scope })}
          options={[
            { value: 'mine', label: '我的' },
            { value: 'all', label: '全部成员' },
          ]}
          className="h-7"
        />
      )}

      {hasRange ? (
        <Chip onOpen={() => setDialog('range')} onClear={() => onChange({ ...context, start_date: '', end_date: '' })} clearLabel="清除时间段">
          <CalendarRange className="size-3.5" />
          <span className="tabular-nums">{rangeLabel(context.start_date, context.end_date)}</span>
        </Chip>
      ) : (
        <Button size="sm" variant="ghost" onClick={() => setDialog('range')}>
          <CalendarRange className="size-3.5" />
          时间段
        </Button>
      )}

      {count > 0 ? (
        <Chip onOpen={() => setDialog('minutes')} onClear={() => onChange({ ...context, minute_ids: [] }, [])} clearLabel="清除已选妙记">
          <Files className="size-3.5" />
          已选 {count} 条妙记
        </Chip>
      ) : (
        <Button size="sm" variant="ghost" onClick={() => setDialog('minutes')}>
          <Files className="size-3.5" />
          选择妙记
        </Button>
      )}

      {dialog === 'range' && (
        <RangeModal
          start={context.start_date}
          end={context.end_date}
          onClose={() => setDialog(null)}
          onApply={(start_date, end_date) => {
            onChange({ ...context, start_date, end_date })
            setDialog(null)
          }}
        />
      )}
      {dialog === 'minutes' && (
        <MinutePicker
          scope={context.scope}
          initial={selected}
          onClose={() => setDialog(null)}
          onApply={(list) => {
            onChange({ ...context, minute_ids: list.map((m) => m.id) }, list)
            setDialog(null)
          }}
        />
      )}
    </div>
  )
}

function Chip({ children, onOpen, onClear, clearLabel }: { children: ReactNode; onOpen: () => void; onClear: () => void; clearLabel: string }) {
  return (
    <span className="inline-flex h-7 max-w-full items-center rounded-lg border border-accent/30 bg-accent-soft text-[13px] text-accent">
      <button type="button" onClick={onOpen} className="inline-flex h-full min-w-0 items-center gap-1.5 rounded-l-lg pr-1 pl-2.5 whitespace-nowrap hover:text-accent-strong">
        {children}
      </button>
      <button type="button" onClick={onClear} className="grid h-full place-items-center rounded-r-lg pr-2 pl-1 hover:text-accent-strong" aria-label={clearLabel} title={clearLabel}>
        <X className="size-3.5" />
      </button>
    </span>
  )
}

function RangeModal({ start, end, onApply, onClose }: { start: string; end: string; onApply: (start: string, end: string) => void; onClose: () => void }) {
  const [from, setFrom] = useState(start)
  const [to, setTo] = useState(end)
  const [presets] = useState(rangePresets)
  const invalid = from !== '' && to !== '' && from > to

  return (
    <Modal
      title="时间段"
      onClose={onClose}
      footer={
        <>
          {(start !== '' || end !== '') && (
            <Button variant="ghost" className="mr-auto" onClick={() => onApply('', '')}>
              清除
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" disabled={invalid} onClick={() => onApply(from, to)}>
            确定
          </Button>
        </>
      }
    >
      <div className="flex flex-wrap gap-1.5">
        {presets.map((p) => (
          <Button key={p.label} size="sm" variant={p.start === from && p.end === to ? 'primary' : 'secondary'} onClick={() => onApply(p.start, p.end)}>
            {p.label}
          </Button>
        ))}
      </div>
      <div className="mt-4 grid grid-cols-2 gap-3">
        <Field label="开始日期" htmlFor="range-start">
          <Input id="range-start" type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className="min-w-0" />
        </Field>
        <Field label="结束日期" htmlFor="range-end">
          <Input id="range-end" type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className="min-w-0" />
        </Field>
      </div>
      <p className={cx('mt-2 text-xs', invalid ? 'text-err' : 'text-faint')}>{invalid ? '开始日期不能晚于结束日期' : '包含开始与结束当天'}</p>
    </Modal>
  )
}

function MinutePicker({
  scope,
  initial,
  onApply,
  onClose,
}: {
  scope: Scope
  initial: ContextMinute[]
  onApply: (list: ContextMinute[]) => void
  onClose: () => void
}) {
  const [selected, setSelected] = useState<ContextMinute[]>(initial)
  const [text, setText] = useState('')
  const [q, setQ] = useState('')

  // 输入停顿后再按标题搜索
  useEffect(() => {
    const t = window.setTimeout(() => setQ(text.trim()), 300)
    return () => window.clearTimeout(t)
  }, [text])

  const list = useInfiniteQuery({
    queryKey: ['minute-picker', scope, q],
    queryFn: ({ pageParam }) => api.minutes({ page: pageParam, page_size: PICKER_PAGE_SIZE, status: '', scope, q }),
    initialPageParam: 1,
    getNextPageParam: (last, all) => (all.length * PICKER_PAGE_SIZE < last.total ? all.length + 1 : undefined),
    placeholderData: keepPreviousData,
  })

  // 翻页期间有新妙记入库时，相邻两页可能出现同一条，按 id 去重
  const rows: MinuteRecord[] = []
  const seen = new Set<number>()
  for (const page of list.data?.pages ?? []) {
    for (const r of page.items) {
      if (!seen.has(r.id)) {
        seen.add(r.id)
        rows.push(r)
      }
    }
  }

  const chosen = new Set(selected.map((m) => m.id))
  const full = selected.length >= MAX_CONTEXT_MINUTES

  const toggle = (r: MinuteRecord) =>
    setSelected((s) => {
      if (s.some((m) => m.id === r.id)) return s.filter((m) => m.id !== r.id)
      if (s.length >= MAX_CONTEXT_MINUTES) return s
      return [...s, { id: r.id, title: r.title, minute_created_at: r.minute_created_at }]
    })

  return (
    <Modal
      title="选择妙记"
      onClose={onClose}
      footer={
        <>
          <span className="mr-auto self-center text-xs text-faint tabular-nums">
            已选 {selected.length} / {MAX_CONTEXT_MINUTES}
          </span>
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" onClick={() => onApply(selected)}>
            确定
          </Button>
        </>
      }
    >
      <Input autoFocus value={text} onChange={(e) => setText(e.target.value)} placeholder="搜索妙记标题" spellCheck={false} />

      {selected.length > 0 && (
        <div className="mt-3 flex max-h-[76px] flex-wrap gap-1.5 overflow-y-auto">
          {selected.map((m) => (
            <span key={m.id} className="inline-flex h-6 max-w-full items-center gap-1 rounded-md border border-line-strong bg-raised pr-1 pl-2 text-xs">
              <span className="truncate">{m.title || '未命名妙记'}</span>
              <button
                type="button"
                onClick={() => setSelected((s) => s.filter((x) => x.id !== m.id))}
                className="shrink-0 rounded p-0.5 text-faint hover:text-text"
                aria-label={`移除 ${m.title || '未命名妙记'}`}
              >
                <X className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}

      <div className="mt-3 max-h-[min(46dvh,360px)] overflow-y-auto rounded-lg border border-line">
        {list.isPending && (
          <div className="grid h-28 place-items-center">
            <Spinner />
          </div>
        )}
        {list.isError && (
          <div className="p-3">
            <ErrorLine error={list.error} onRetry={() => list.refetch()} />
          </div>
        )}
        {list.data && rows.length === 0 && <p className="py-10 text-center text-[13px] text-muted">{q ? '没有符合条件的妙记' : '暂无妙记'}</p>}
        {rows.map((r) => {
          const checked = chosen.has(r.id)
          const disabled = !checked && full
          return (
            <label
              key={r.id}
              className={cx(
                'flex items-start gap-3 border-b border-line px-3 py-2.5 last:border-0',
                disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer hover:bg-raised/60',
              )}
            >
              <input type="checkbox" checked={checked} disabled={disabled} onChange={() => toggle(r)} className="mt-1 size-3.5 shrink-0 accent-accent" />
              <span className="min-w-0 flex-1">
                <span className={cx('block truncate text-sm', !r.title && 'text-faint')}>{r.title || r.minute_token}</span>
                <span className="block truncate text-xs text-faint tabular-nums">
                  {formatTime(r.minute_created_at)}
                  {scope === 'all' && <span className="ml-3">{r.owner.name}</span>}
                </span>
              </span>
              <StatusBadge status={r.status} />
            </label>
          )
        })}
        {list.hasNextPage && (
          <div className="border-t border-line p-1.5">
            <Button size="sm" variant="ghost" className="w-full" loading={list.isFetchingNextPage} onClick={() => list.fetchNextPage()}>
              加载更多
            </Button>
          </div>
        )}
      </div>
    </Modal>
  )
}
