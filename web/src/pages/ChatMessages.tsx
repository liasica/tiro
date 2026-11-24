import { ArrowUpRight, AudioLines, BookOpen, ChevronRight, ClipboardList, FileText, ListChecks, Milestone, Scale, Search, TriangleAlert } from 'lucide-react'
import { createContext, memo, useContext, useState, type AnchorHTMLAttributes } from 'react'
import Markdown, { type Components, type ExtraProps } from 'react-markdown'
import remarkCjkFriendly from 'remark-cjk-friendly'
import remarkGfm from 'remark-gfm'
import { Spinner } from '../components/ui'
import type { ChatMessage, ChatResult, ChatSource, ChatStep, ChatStepKind } from '../lib/api'
import { citeTarget, linkCites, parseCiteHref } from '../lib/chat'
import { formatCount, formatDuration, formatMillis, formatTime, formatTokens } from '../lib/format'
import { cx } from '../lib/ui'

// 当前回答的引用来源，供 Markdown 里的角标查找
const SourcesContext = createContext<ChatSource[]>([])

function MdLink({ href, children, node: _node, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & ExtraProps) {
  const sources = useContext(SourcesContext)
  const cite = href ? parseCiteHref(href) : null
  if (!cite) {
    return (
      <a {...rest} href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    )
  }
  const src = sources.find((s) => s.index === cite.index)
  if (!src) return <span className="cite">{cite.index}</span>
  const title = [src.title || '未命名妙记', cite.time].filter(Boolean).join(' · ')
  return (
    <a href={citeTarget(src, cite.time)} target="_blank" rel="noreferrer" className="cite" title={title} aria-label={`引用 ${cite.index}：${title}`}>
      {cite.index}
    </a>
  )
}

const mdComponents: Components = { a: MdLink }

function Answer({ text, sources }: { text: string; sources: ChatSource[] }) {
  return (
    <SourcesContext value={sources}>
      <article className="md">
        <Markdown remarkPlugins={[remarkGfm, remarkCjkFriendly]} components={mdComponents}>
          {text}
        </Markdown>
      </article>
    </SourcesContext>
  )
}

export const UserBubble = memo(function UserBubble({ text }: { text: string }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-2xl rounded-br-md border border-accent/20 bg-accent-soft px-3.5 py-2 text-sm leading-relaxed break-words whitespace-pre-wrap">
        {text}
      </div>
    </div>
  )
})

const stepIcons: Record<ChatStepKind, typeof Search> = {
  search: Search,
  read: FileText,
  transcript: AudioLines,
}

function StepList({ steps, pending }: { steps: ChatStep[]; pending?: boolean }) {
  return (
    <ul className="space-y-1">
      {steps.map((s, i) => {
        const Icon = stepIcons[s.kind] ?? Search
        return (
          <li key={i} className="flex items-start gap-2 text-xs leading-relaxed text-faint">
            <Icon className="mt-[3px] size-3.5 shrink-0" />
            <span className="min-w-0 break-words">{s.text}</span>
          </li>
        )
      })}
      {pending && (
        <li>
          <Spinner className="size-3.5" />
        </li>
      )}
    </ul>
  )
}

// 已完成的检索过程折叠成一行，点击展开
function StepsFold({ steps }: { steps: ChatStep[] }) {
  const [open, setOpen] = useState(false)
  if (steps.length === 0) return null
  return (
    <div className="mb-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 py-0.5 text-xs text-faint transition-colors hover:text-muted"
      >
        <ChevronRight className={cx('size-3.5 transition-transform duration-150', open && 'rotate-90')} />
        检索过程 · {steps.length} 步
      </button>
      {open && (
        <div className="mt-1.5 ml-1 border-l border-line pl-3">
          <StepList steps={steps} />
        </div>
      )}
    </div>
  )
}

function ErrorBox({ text }: { text: string }) {
  return <div className="mt-3 rounded-lg border border-err/30 bg-err/8 px-3.5 py-2.5 text-[13px] break-words text-err">{text}</div>
}

function Sources({ sources, showOwner }: { sources: ChatSource[]; showOwner: boolean }) {
  const sorted = [...sources].sort((a, b) => a.index - b.index)
  const link = 'text-accent hover:text-accent-strong'
  return (
    <section className="mt-5">
      <h4 className="mb-2.5 flex items-center gap-3 text-xs text-faint">
        引用来源
        <span className="h-px flex-1 bg-line" />
      </h4>
      <ol className="space-y-3">
        {sorted.map((s) => {
          const meta = [formatTime(s.minute_created_at), s.duration_seconds > 0 ? formatDuration(s.duration_seconds) : '', showOwner ? s.owner : ''].filter(
            (v) => v && v !== '—',
          )
          return (
            <li key={s.index} className="flex gap-2.5 text-[13px]">
              <span className="cite mt-0.5 shrink-0 self-start" aria-hidden>
                {s.index}
              </span>
              <div className="min-w-0 flex-1">
                <div className={cx('break-words', s.title ? 'text-text' : 'text-faint')}>{s.title || '未命名妙记'}</div>
                {meta.length > 0 && <div className="text-xs text-faint tabular-nums">{meta.join(' · ')}</div>}
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                  {s.doc_url && (
                    <a href={s.doc_url} target="_blank" rel="noreferrer" className={link}>
                      会议纪要
                    </a>
                  )}
                  <a href={s.minute_url} target="_blank" rel="noreferrer" className={link}>
                    妙记
                  </a>
                  {s.moments.map((m, i) => (
                    <a
                      key={`${m.label}-${i}`}
                      href={m.url}
                      target="_blank"
                      rel="noreferrer"
                      className="rounded border border-line px-1 text-muted tabular-nums transition-colors hover:border-accent/50 hover:text-accent"
                    >
                      {m.label}
                    </a>
                  ))}
                </div>
              </div>
            </li>
          )
        })}
      </ol>
    </section>
  )
}

const resultIcons: Record<string, typeof FileText> = {
  todo: ListChecks,
  summary: ClipboardList,
  decision: Scale,
  progress: Milestone,
  risk: TriangleAlert,
  brief: BookOpen,
}

// 已保存的结果文档：类型、主题与跳转
function ResultDoc({ result }: { result: ChatResult }) {
  const Icon = resultIcons[result.kind] ?? FileText
  const label = result.label || '飞书文档'
  const prefix = `${label}：`
  const subject = result.title.startsWith(prefix) ? result.title.slice(prefix.length) : result.title
  return (
    <a
      href={result.url}
      target="_blank"
      rel="noreferrer"
      className="group mt-5 flex max-w-md items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2.5 transition-colors hover:border-accent/40 hover:bg-accent-soft"
    >
      <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent-soft text-accent">
        <Icon className="size-[18px]" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[11px] text-faint">{label} · 飞书文档</span>
        <span className="block truncate text-[13px] font-medium text-text">{subject || label}</span>
      </span>
      <ArrowUpRight className="size-4 shrink-0 text-faint transition-colors group-hover:text-accent" />
    </a>
  )
}

function MetaLine({ message: m }: { message: ChatMessage }) {
  const parts: { text: string; title?: string }[] = []
  if (m.model) parts.push({ text: m.model })
  if (m.total_tokens > 0) {
    parts.push({ text: `${formatTokens(m.total_tokens)} Token`, title: `输入 ${formatCount(m.prompt_tokens)} · 输出 ${formatCount(m.completion_tokens)}` })
  }
  if (m.duration_ms > 0) parts.push({ text: formatMillis(m.duration_ms) })
  const state = m.result?.state === 'pending' || m.result?.state === 'failed' ? m.result.state : null
  if (parts.length === 0 && !state) return null
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px] text-faint/80 tabular-nums">
      {parts.map((p, i) => (
        <span key={i} title={p.title}>
          {i > 0 && <span className="mr-1.5">·</span>}
          {p.text}
        </span>
      ))}
      {state && (
        <span className={cx('inline-flex min-w-0 items-center gap-1', state === 'failed' && 'text-err/70')}>
          {parts.length > 0 && <span className="mr-0.5 text-faint/80">·</span>}
          {state === 'pending' ? (
            <>
              正在整理成文档
              <Spinner className="size-3" />
            </>
          ) : (
            <span className="break-all">
              文档保存失败{m.result.error ? `：${m.result.error}` : ''}
              {m.result.retrying ? '，稍后自动重试' : ''}
            </span>
          )}
        </span>
      )}
    </div>
  )
}

// 已保存的助手消息，流式输出期间不随其他消息重新渲染
export const AssistantMessage = memo(function AssistantMessage({ message: m, showOwner }: { message: ChatMessage; showOwner: boolean }) {
  return (
    <div>
      <StepsFold steps={m.steps ?? []} />
      {m.content && <Answer text={linkCites(m.content)} sources={m.sources ?? []} />}
      {m.error && <ErrorBox text={m.error} />}
      {(m.sources?.length ?? 0) > 0 && <Sources sources={m.sources} showOwner={showOwner} />}
      {m.result?.state === 'done' && m.result.url && <ResultDoc result={m.result} />}
      <MetaLine message={m} />
    </div>
  )
})

// 流式生成中或被中断的临时回答，text 已去掉原始引用标记
export function LiveAnswer({ steps, text, running, error, stopped }: { steps: ChatStep[]; text: string; running: boolean; error: string; stopped: boolean }) {
  return (
    <div>
      {running ? (
        (steps.length > 0 || !text) && (
          <div className="mb-3">
            <StepList steps={steps} pending={!text} />
          </div>
        )
      ) : (
        <StepsFold steps={steps} />
      )}
      {text && <Answer text={text} sources={[]} />}
      {error && <ErrorBox text={error} />}
      {stopped && <p className="mt-3 text-xs text-faint">已停止</p>}
    </div>
  )
}
