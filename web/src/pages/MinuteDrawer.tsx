import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, FileText, RotateCw, X } from 'lucide-react'
import type { ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Avatar, Button, Drawer, LinkButton, ErrorLine, Spinner, StatusBadge } from '../components/ui'
import { api, type LlmCall } from '../lib/api'
import { formatCount, formatDuration, formatMillis, formatTime } from '../lib/format'
import { useToast } from '../lib/toast'
import { cx, generateLabel } from '../lib/ui'

// 纪要详情抽屉
export function MinuteDrawer({ id, admin, onClose }: { id: number; admin: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({
    queryKey: ['minute', id],
    queryFn: () => api.minute(id),
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === 'pending' || s === 'processing' ? 5000 : false
    },
  })
  const gen = useMutation({
    mutationFn: () => api.generate(id),
    onSuccess: () => {
      toast('已加入生成队列')
      qc.invalidateQueries({ queryKey: ['minute', id] })
      qc.invalidateQueries({ queryKey: ['minutes'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
    onError: (e) => toast(e.message, 'err'),
  })

  const r = q.data
  const running = r?.status === 'pending' || r?.status === 'processing'

  return (
    <Drawer label="纪要详情" onClose={onClose}>
      <div className="flex items-start justify-between gap-4 border-b border-line px-6 pt-5 pb-4">
        <div className="min-w-0">
          <h2 className={cx('text-[17px] leading-snug font-semibold', !r?.title && 'text-muted')}>{r ? r.title || '未命名妙记' : '纪要详情'}</h2>
          {r && <p className="mt-1 truncate text-xs text-faint">{r.minute_token}</p>}
        </div>
        <button type="button" onClick={onClose} className="shrink-0 rounded-md p-1.5 text-faint hover:bg-raised hover:text-text" aria-label="关闭">
          <X className="size-4" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto">
        {q.isPending && (
          <div className="grid h-40 place-items-center">
            <Spinner />
          </div>
        )}
        {q.isError && (
          <div className="p-6">
            <ErrorLine error={q.error} onRetry={() => q.refetch()} />
          </div>
        )}
        {r && (
          <div className="px-6 py-5">
            <div className="flex flex-wrap gap-2">
              {r.doc_url && (
                <LinkButton variant="primary" href={r.doc_url}>
                  <FileText className="size-4" />
                  打开飞书文档
                </LinkButton>
              )}
              <LinkButton href={r.minute_url}>
                <ExternalLink className="size-4" />
                打开妙记
              </LinkButton>
              {admin && (
                <Button onClick={() => gen.mutate()} loading={gen.isPending} disabled={running}>
                  {!gen.isPending && <RotateCw className="size-4" />}
                  {generateLabel(r.status)}
                </Button>
              )}
            </div>

            <dl className="mt-6 grid grid-cols-1 gap-x-8 gap-y-3 text-[13px] sm:grid-cols-2">
              <Meta label="状态">
                <StatusBadge status={r.status} />
              </Meta>
              <Meta label="所有者">
                <span className="flex items-center gap-2">
                  <Avatar src={r.owner.avatar_url} name={r.owner.name} size={18} />
                  {r.owner.name}
                </span>
              </Meta>
              <Meta label="妙记时间">{formatTime(r.minute_created_at)}</Meta>
              <Meta label="时长">{formatDuration(r.duration_seconds)}</Meta>
              <Meta label="入库时间">{formatTime(r.created_at)}</Meta>
              <Meta label="完成时间">{formatTime(r.finished_at)}</Meta>
              <Meta label="生成次数">{r.attempts}</Meta>
            </dl>

            {r.reason && (
              <div
                className={cx(
                  'mt-5 rounded-lg border px-3.5 py-2.5 text-[13px]',
                  r.status === 'failed' ? 'border-err/30 bg-err/8 text-err' : 'border-line bg-raised/60 text-muted',
                )}
              >
                {r.reason}
              </div>
            )}

            {r.llm_calls.length > 0 && <Usage calls={r.llm_calls} />}

            <section className="mt-8">
              <SectionTitle>纪要预览</SectionTitle>
              {r.summary_markdown ? (
                <article className="md">
                  <Markdown remarkPlugins={[remarkGfm]} components={{ a: (p) => <a {...p} target="_blank" rel="noreferrer" /> }}>
                    {r.summary_markdown}
                  </Markdown>
                </article>
              ) : (
                <p className="py-6 text-center text-[13px] text-faint">{running ? '纪要生成中' : '纪要尚未生成'}</p>
              )}
            </section>
          </div>
        )}
      </div>
    </Drawer>
  )
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="mb-3 flex items-center gap-3 text-xs text-faint">
      {children}
      <span className="h-px flex-1 bg-line" />
    </h3>
  )
}

type TokenKey = 'prompt_tokens' | 'cached_tokens' | 'completion_tokens' | 'reasoning_tokens' | 'total_tokens' | 'duration_ms'

// 历次模型调用的用量，多于一次时末行给出合计
function Usage({ calls }: { calls: LlmCall[] }) {
  const sum = (key: TokenKey) => calls.reduce((n, c) => n + c[key], 0)
  return (
    <section className="mt-8">
      <SectionTitle>模型消耗</SectionTitle>
      <div className="overflow-x-auto rounded-lg border border-line">
        <table className="w-full min-w-[560px] border-collapse text-[13px] tabular-nums">
          <thead>
            <tr className="border-b border-line text-left text-xs text-faint">
              <th className="px-3 py-2 font-normal">时间</th>
              <th className="px-3 py-2 font-normal">模型</th>
              <th className="px-3 py-2 text-right font-normal">输入</th>
              <th className="px-3 py-2 text-right font-normal">输出</th>
              <th className="px-3 py-2 text-right font-normal">合计</th>
              <th className="px-3 py-2 text-right font-normal">耗时</th>
            </tr>
          </thead>
          <tbody>
            {calls.map((c) => (
              <tr key={c.id} className="border-b border-line align-top last:border-0">
                <td className="px-3 py-2 whitespace-nowrap text-muted">{formatTime(c.created_at)}</td>
                <td className="px-3 py-2">
                  <div className="break-all">{c.model || '—'}</div>
                  {c.error && <div className="mt-0.5 text-xs text-err">{c.error}</div>}
                </td>
                <TokenCell value={c.prompt_tokens} part={c.cached_tokens} partLabel="缓存命中" />
                <TokenCell value={c.completion_tokens} part={c.reasoning_tokens} partLabel="推理" />
                <td className="px-3 py-2 text-right">{formatCount(c.total_tokens)}</td>
                <td className="px-3 py-2 text-right whitespace-nowrap text-muted">{formatMillis(c.duration_ms)}</td>
              </tr>
            ))}
          </tbody>
          {calls.length > 1 && (
            <tfoot>
              <tr className="border-t border-line bg-raised/60 align-top font-medium">
                <td className="px-3 py-2" colSpan={2}>
                  合计 {calls.length} 次调用
                </td>
                <TokenCell value={sum('prompt_tokens')} part={sum('cached_tokens')} partLabel="缓存命中" />
                <TokenCell value={sum('completion_tokens')} part={sum('reasoning_tokens')} partLabel="推理" />
                <td className="px-3 py-2 text-right">{formatCount(sum('total_tokens'))}</td>
                <td className="px-3 py-2 text-right whitespace-nowrap">{formatMillis(sum('duration_ms'))}</td>
              </tr>
            </tfoot>
          )}
        </table>
      </div>
    </section>
  )
}

function TokenCell({ value, part, partLabel }: { value: number; part: number; partLabel: string }) {
  return (
    <td className="px-3 py-2 text-right">
      <div>{formatCount(value)}</div>
      {part > 0 && (
        <div className="text-xs font-normal whitespace-nowrap text-faint">
          {partLabel} {formatCount(part)}
        </div>
      )}
    </td>
  )
}

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center gap-4">
      <dt className="w-16 shrink-0 text-faint">{label}</dt>
      <dd className="min-w-0 text-text tabular-nums">{children}</dd>
    </div>
  )
}
