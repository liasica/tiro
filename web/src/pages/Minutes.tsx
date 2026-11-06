import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, FileText, Plus, RotateCw } from 'lucide-react'
import { lazy, Suspense, useState, type FormEvent, type MouseEvent } from 'react'
import { useSearchParams } from 'react-router'
import { Avatar, Button, ErrorLine, Input, Modal, PageHeader, Segmented, Spinner, StatusBadge } from '../components/ui'
import type { MinuteRecord, RecordStatus, Scope, Stats } from '../lib/api'
import { api } from '../lib/api'
import { formatDuration, formatTime } from '../lib/format'
import { useToast } from '../lib/toast'
import { cx, generateLabel } from '../lib/ui'

// 详情抽屉含 Markdown 渲染，按需加载
const MinuteDrawer = lazy(() => import('./MinuteDrawer').then((m) => ({ default: m.MinuteDrawer })))

const PAGE_SIZE = 20

const statusOptions: { value: RecordStatus | ''; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'pending', label: '排队中' },
  { value: 'processing', label: '生成中' },
  { value: 'done', label: '已生成' },
  { value: 'failed', label: '失败' },
  { value: 'skipped', label: '已跳过' },
]

const isStatus = (v: string | null): v is RecordStatus => statusOptions.some((o) => o.value !== '' && o.value === v)

// 纪要记录页：统计、筛选、列表、详情抽屉
export function Minutes({ admin }: { admin: boolean }) {
  const [params, setParams] = useSearchParams()
  const rawStatus = params.get('status')
  const status: RecordStatus | '' = isStatus(rawStatus) ? rawStatus : ''
  const scope: Scope = admin && params.get('scope') === 'all' ? 'all' : 'mine'
  const page = Math.max(1, Number(params.get('page')) || 1)
  const [openId, setOpenId] = useState<number | null>(null)
  const [adding, setAdding] = useState(false)

  const update = (patch: Record<string, string>) => {
    const next = new URLSearchParams(params)
    for (const [k, v] of Object.entries(patch)) {
      if (v === '' || (k === 'page' && v === '1') || (k === 'scope' && v === 'mine')) next.delete(k)
      else next.set(k, v)
    }
    setParams(next, { replace: true })
  }

  const stats = useQuery({
    queryKey: ['stats', scope],
    queryFn: () => api.stats(scope),
    refetchInterval: (q) => ((q.state.data?.pending ?? 0) > 0 ? 5000 : 30000),
  })

  const busy = (stats.data?.pending ?? 0) > 0
  const list = useQuery({
    queryKey: ['minutes', { scope, status, page }],
    queryFn: () => api.minutes({ page, page_size: PAGE_SIZE, status, scope }),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (busy || q.state.data?.items.some((r) => r.status === 'pending' || r.status === 'processing') ? 5000 : 30000),
  })

  const total = list.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <div>
      <PageHeader
        title="纪要"
        actions={
          <>
            {admin && (
              <Segmented<Scope>
                value={scope}
                onChange={(v) => update({ scope: v, page: '1' })}
                options={[
                  { value: 'mine', label: '我的' },
                  { value: 'all', label: '全部成员' },
                ]}
              />
            )}
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus className="size-4" />
              添加妙记
            </Button>
          </>
        }
      />

      <StatsStrip stats={stats.data} />

      <div className="mt-8 mb-3 flex flex-wrap items-center justify-between gap-3">
        <Segmented<RecordStatus | ''> value={status} onChange={(v) => update({ status: v, page: '1' })} options={statusOptions} className="max-w-full overflow-x-auto" />
        {list.isFetching && !list.isPending && <Spinner className="size-3.5" />}
      </div>

      {list.isError && <ErrorLine error={list.error} onRetry={() => list.refetch()} />}

      <div className="overflow-hidden rounded-xl border border-line bg-surface/70">
        <table className="w-full table-fixed border-collapse text-sm">
          <thead>
            <tr className="border-b border-line text-left text-xs text-faint">
              <th className="px-4 py-2.5 font-normal">标题</th>
              <th className="hidden w-40 px-4 py-2.5 font-normal md:table-cell">妙记时间</th>
              <th className="hidden w-28 px-4 py-2.5 font-normal lg:table-cell">时长</th>
              <th className="w-24 px-4 py-2.5 font-normal sm:w-44">状态</th>
              <th className="hidden w-20 px-4 py-2.5 font-normal sm:table-cell">纪要</th>
              <th className="w-14 px-2 py-2.5 sm:w-28 sm:px-4">
                <span className="sr-only">操作</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.isPending && (
              <tr>
                <td colSpan={6} className="py-16 text-center">
                  <Spinner className="mx-auto" />
                </td>
              </tr>
            )}
            {list.data?.items.map((r) => (
              <Row key={r.id} record={r} showOwner={scope === 'all'} onOpen={() => setOpenId(r.id)} />
            ))}
            {list.data && list.data.items.length === 0 && (
              <tr>
                <td colSpan={6} className="py-16 text-center text-sm text-muted">
                  {status ? '没有符合条件的记录' : '暂无记录'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {total > 0 && (
        <div className="mt-4 flex items-center justify-between text-[13px] text-muted">
          <span className="tabular-nums">共 {total} 条</span>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="ghost" disabled={page <= 1} onClick={() => update({ page: String(page - 1) })} aria-label="上一页">
              <ChevronLeft className="size-4" />
            </Button>
            <span className="tabular-nums">
              {page} / {pages}
            </span>
            <Button size="sm" variant="ghost" disabled={page >= pages} onClick={() => update({ page: String(page + 1) })} aria-label="下一页">
              <ChevronRight className="size-4" />
            </Button>
          </div>
        </div>
      )}

      {openId !== null && (
        <Suspense fallback={null}>
          <MinuteDrawer id={openId} onClose={() => setOpenId(null)} />
        </Suspense>
      )}
      {adding && <AddMinute onClose={() => setAdding(false)} onAdded={(r) => setOpenId(r.id)} />}
    </div>
  )
}

function StatsStrip({ stats }: { stats?: Stats }) {
  const cells: { label: string; value?: number; tone?: string }[] = [
    { label: '总数', value: stats?.total },
    { label: '已生成', value: stats?.done, tone: 'text-ok' },
    { label: '失败', value: stats?.failed, tone: stats?.failed ? 'text-err' : undefined },
    { label: '待处理', value: stats?.pending, tone: stats?.pending ? 'text-warn' : undefined },
    { label: '已跳过', value: stats?.skipped },
  ]
  return (
    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3 lg:grid-cols-6">
      {cells.map((c) => (
        <div key={c.label} className="bg-surface px-5 py-4">
          <div className="text-xs text-faint">{c.label}</div>
          <div className={cx('mt-1 text-[28px] leading-tight font-light tracking-tight tabular-nums', c.tone ?? 'text-text')}>{c.value ?? '–'}</div>
        </div>
      ))}
      <div className="flex flex-col justify-center bg-surface px-5 py-4">
        <div className="text-xs text-faint">最近检查</div>
        <div className="mt-1.5 text-sm text-muted tabular-nums">{stats ? formatTime(stats.last_sync_at) : '–'}</div>
      </div>
    </div>
  )
}

function Row({ record: r, showOwner, onOpen }: { record: MinuteRecord; showOwner: boolean; onOpen: () => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const gen = useMutation({
    mutationFn: () => api.generate(r.id),
    onSuccess: () => {
      toast('已加入生成队列')
      qc.invalidateQueries({ queryKey: ['minutes'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
    onError: (e) => toast(e.message, 'err'),
  })
  const running = r.status === 'pending' || r.status === 'processing'
  const stop = (e: MouseEvent) => e.stopPropagation()

  return (
    <tr
      onClick={onOpen}
      className="group cursor-pointer border-b border-line transition-colors last:border-0 hover:bg-raised/60"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter') onOpen()
      }}
    >
      <td className="px-4 py-3">
        <div className="flex min-w-0 items-center gap-2.5">
          {showOwner && <Avatar src={r.owner.avatar_url} name={r.owner.name} size={22} />}
          <div className="min-w-0">
            <div className={cx('truncate', r.title ? 'text-text' : 'text-faint')}>{r.title || r.minute_token}</div>
            <div className="truncate text-xs text-faint tabular-nums">
              {showOwner && <span className="mr-3">{r.owner.name}</span>}
              <span className="md:hidden">
                {formatTime(r.minute_created_at)}
                <span className="ml-3">{formatDuration(r.duration_seconds)}</span>
              </span>
            </div>
          </div>
        </div>
      </td>
      <td className="hidden px-4 py-3 text-muted tabular-nums md:table-cell">{formatTime(r.minute_created_at)}</td>
      <td className="hidden px-4 py-3 text-muted tabular-nums lg:table-cell">{formatDuration(r.duration_seconds)}</td>
      <td className="px-4 py-3">
        <StatusBadge status={r.status} />
        {r.reason && (r.status === 'failed' || r.status === 'skipped') && (
          <div className="mt-0.5 hidden truncate text-xs text-faint sm:block" title={r.reason}>
            {r.reason}
          </div>
        )}
      </td>
      <td className="hidden px-4 py-3 sm:table-cell">
        {r.doc_url ? (
          <a href={r.doc_url} target="_blank" rel="noreferrer" onClick={stop} className="inline-flex items-center gap-1 text-accent hover:text-accent-strong">
            <FileText className="size-3.5" />
            打开
          </a>
        ) : (
          <span className="text-faint">—</span>
        )}
      </td>
      <td className="px-2 py-3 text-right sm:px-4" onClick={stop}>
        <Button size="sm" variant="ghost" disabled={running} loading={gen.isPending} onClick={() => gen.mutate()} title={generateLabel(r.status)}>
          {!gen.isPending && <RotateCw className="size-3.5" />}
          <span className="hidden sm:inline">{generateLabel(r.status)}</span>
        </Button>
      </td>
    </tr>
  )
}

function AddMinute({ onClose, onAdded }: { onClose: () => void; onAdded: (r: MinuteRecord) => void }) {
  const [url, setUrl] = useState('')
  const qc = useQueryClient()
  const toast = useToast()
  const add = useMutation({
    mutationFn: () => api.addMinute(url.trim()),
    onSuccess: (r) => {
      toast('已添加，加入生成队列')
      qc.invalidateQueries({ queryKey: ['minutes'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
      onClose()
      onAdded(r)
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (url.trim()) add.mutate()
  }

  return (
    <Modal title="添加妙记" onClose={onClose}>
      <form onSubmit={onSubmit} className="space-y-3">
        <Input autoFocus value={url} onChange={(e) => setUrl(e.target.value)} placeholder="妙记链接或 token" spellCheck={false} />
        <p className="text-xs text-faint">支持妙记链接或 token，添加后立即排队生成纪要。</p>
        {add.isError && <p className="text-[13px] text-err">{add.error.message}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" type="submit" loading={add.isPending} disabled={!url.trim()}>
            添加
          </Button>
        </div>
      </form>
    </Modal>
  )
}
