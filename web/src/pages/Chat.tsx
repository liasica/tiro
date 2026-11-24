import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUp, CircleStop, PanelLeft, Square, SquarePen, Trash2, X } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { Button, Drawer, ErrorLine, Modal, Spinner } from '../components/ui'
import type { ChatContext, ChatDetail, ChatEvent, ChatMessage, ChatModelOption, ChatStep, ChatSummary, ContextMinute } from '../lib/api'
import { ApiError, api } from '../lib/api'
import { emptyContext, stripRawCites } from '../lib/chat'
import { formatTime } from '../lib/format'
import { useToast } from '../lib/toast'
import { cx } from '../lib/ui'
import { ContextBar } from './ChatContextBar'
import { AssistantMessage, LiveAnswer, UserBubble } from './ChatMessages'

const examples = ['帮我找找本周的待办事项', '上周的会议做了哪些关键决策？', '最近一次会议讨论了什么？']

// 对话的上下文与模型，按对话分别记录；modelId 为空表示默认模型
interface Selection {
  context: ChatContext
  minutes: ContextMinute[]
  modelId: string
}

const emptySelection: Selection = { context: emptyContext(), minutes: [], modelId: '' }

type TurnStatus = 'running' | 'done' | 'stopped' | 'failed'

// 一次提问的本地状态：流式输出期间的临时消息，收到 answer 后由最终消息替换
interface Turn {
  key: number
  chatId: number | null
  question: string
  userMsg: ChatMessage | null
  steps: ChatStep[]
  text: string
  answer: ChatMessage | null
  status: TurnStatus
  error: string
  ctrl: AbortController
}

type Item =
  | { kind: 'message'; key: string; message: ChatMessage }
  | { kind: 'question'; key: string; text: string }
  | { kind: 'live'; key: string; turn: Turn }

const TAIL = Number.MAX_SAFE_INTEGER

// 合并已保存的消息与本地提问：按 id 去重排序，尚未保存的临时消息排在对应问题之后
function buildItems(messages: ChatMessage[], turns: Turn[]): Item[] {
  const saved = new Map<number, ChatMessage>()
  for (const m of messages) saved.set(m.id, m)
  for (const t of turns) {
    if (t.userMsg && !saved.has(t.userMsg.id)) saved.set(t.userMsg.id, t.userMsg)
    if (t.answer && !saved.has(t.answer.id)) saved.set(t.answer.id, t.answer)
  }

  const rows: { order: number; sub: number; item: Item }[] = []
  for (const m of saved.values()) rows.push({ order: m.id, sub: 0, item: { kind: 'message', key: `m${m.id}`, message: m } })
  for (const t of turns) {
    if (!t.userMsg) rows.push({ order: TAIL, sub: t.key * 2, item: { kind: 'question', key: `q${t.key}`, text: t.question } })
    if (t.answer) continue
    // 中断后详情里已有更晚的消息时，以详情为准
    const userId = t.userMsg?.id
    if (t.status !== 'running' && userId !== undefined && messages.some((m) => m.id > userId)) continue
    rows.push({ order: userId ?? TAIL, sub: userId === undefined ? t.key * 2 + 1 : 1, item: { kind: 'live', key: `a${t.key}`, turn: t } })
  }
  rows.sort((a, b) => (a.order !== b.order ? a.order - b.order : a.sub - b.sub))
  return rows.map((r) => r.item)
}

// 有回答还在整理成文档
function resultsPending(d?: ChatDetail) {
  return Boolean(d?.messages.some((m) => m.role === 'assistant' && m.result?.state === 'pending'))
}

function fromDetail(d: ChatDetail): Selection {
  const c = d.context ?? emptyContext()
  return {
    context: { scope: c.scope === 'all' ? 'all' : 'mine', minute_ids: c.minute_ids ?? [], start_date: c.start_date ?? '', end_date: c.end_date ?? '' },
    minutes: d.context_minutes ?? [],
    modelId: d.model_id ?? '',
  }
}

// 对话页：左侧历史对话，右侧消息与输入区
export function Chat({ admin }: { admin: boolean }) {
  const params = useParams()
  const parsed = params.id === undefined ? null : Number(params.id)
  const id = parsed !== null && Number.isInteger(parsed) && parsed > 0 ? parsed : null
  const badParam = params.id !== undefined && id === null

  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()

  const chats = useQuery({ queryKey: ['chats'], queryFn: api.chats })
  const detail = useQuery({
    queryKey: ['chat', id],
    queryFn: () => api.chat(id as number),
    enabled: id !== null,
    refetchInterval: (q) => (resultsPending(q.state.data) ? 3000 : false),
  })
  const models = useQuery({ queryKey: ['chat-models'], queryFn: api.chatModels, staleTime: 60_000 })

  const [turns, setTurns] = useState<Turn[]>([])
  const [draft, setDraftState] = useState<number | null>(null)
  const [selections, setSelections] = useState<Record<string, Selection>>({})
  const [input, setInput] = useState('')
  const [listOpen, setListOpen] = useState(false)
  const [removing, setRemoving] = useState<ChatSummary | null>(null)
  const [ending, setEnding] = useState(false)

  // 异步回调里需要读取的最新值
  const alive = useRef(true)
  const viewRef = useRef<number | null>(id)
  const draftRef = useRef<number | null>(null)
  const seq = useRef(0)

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  useEffect(() => {
    viewRef.current = id
  }, [id])

  useEffect(() => {
    if (badParam) navigate('/chat', { replace: true })
  }, [badParam, navigate])

  const setDraft = (key: number | null) => {
    draftRef.current = key
    setDraftState(key)
  }

  const viewKey = id === null ? 'new' : String(id)
  const viewTurns = id === null ? turns.filter((t) => t.key === draft) : turns.filter((t) => t.chatId === id)
  const running = viewTurns.find((t) => t.status === 'running')

  const restored = id !== null && detail.data ? fromDetail(detail.data) : null
  const sel = selections[viewKey] ?? restored ?? emptySelection
  const context: ChatContext = admin ? sel.context : { ...sel.context, scope: 'mine' }
  const options: ChatModelOption[] = models.data?.items ?? []
  const modelId = options.some((m) => m.id === sel.modelId)
    ? sel.modelId
    : options.some((m) => m.id === models.data?.default_id)
      ? (models.data?.default_id ?? '')
      : (options[0]?.id ?? '')
  // 已有对话的上下文要等详情加载后才能确定
  const ready = id === null || selections[viewKey] !== undefined || detail.data !== undefined

  const updateSel = (patch: Partial<Selection>) => setSelections((s) => ({ ...s, [viewKey]: { ...sel, ...patch } }))

  const items = buildItems(id !== null ? (detail.data?.messages ?? []) : [], viewTurns)

  // 消息区滚动：贴近底部时跟随新内容，用户往上翻后不再拉回
  const scroller = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const lastView = useRef<string | null>(null)
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    if (lastView.current !== viewKey) {
      lastView.current = viewKey
      stick.current = true
    }
    if (stick.current) el.scrollTop = el.scrollHeight
  })
  const onScroll = () => {
    const el = scroller.current
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48
  }

  const send = (raw: string) => {
    const question = raw.trim()
    if (!question || running || !ready) return

    const key = ++seq.current
    const fromNew = id === null
    const snapshot: Selection = { ...sel, context, modelId }
    const ctrl = new AbortController()
    let chatId = id
    let saved = false
    let answered = false

    setTurns((ts) => [...ts, { key, chatId, question, userMsg: null, steps: [], text: '', answer: null, status: 'running', error: '', ctrl }])
    if (fromNew) setDraft(key)
    setInput('')
    stick.current = true

    const patch = (fn: (t: Turn) => Turn) => setTurns((ts) => ts.map((t) => (t.key === key ? fn(t) : t)))

    const onEvent = (ev: ChatEvent) => {
      switch (ev.event) {
        case 'chat': {
          const newId = ev.data.id
          chatId = newId
          patch((t) => ({ ...t, chatId: newId }))
          qc.setQueryData<{ items: ChatSummary[] }>(['chats'], (old) => old && { items: [ev.data, ...old.items.filter((c) => c.id !== newId)] })
          if (fromNew) {
            const watching = draftRef.current === key
            setSelections((s) => {
              const next = { ...s, [String(newId)]: snapshot }
              if (watching) delete next.new
              return next
            })
            // 用户仍停留在这个新对话上时才换地址，不重新挂载页面
            if (watching && alive.current && viewRef.current === null) navigate(`/chat/${newId}`, { replace: true })
            if (watching) setDraft(null)
          }
          break
        }
        case 'question':
          saved = true
          patch((t) => ({ ...t, userMsg: ev.data }))
          break
        case 'step':
          patch((t) => ({ ...t, steps: [...t.steps, ev.data] }))
          break
        case 'delta':
          patch((t) => ({ ...t, text: t.text + (ev.data.text ?? '') }))
          break
        case 'reset':
          patch((t) => ({ ...t, text: '' }))
          break
        case 'answer':
          answered = true
          patch((t) => ({ ...t, answer: ev.data, status: 'done' }))
          break
      }
    }

    const fail = (e: unknown) => {
      const stopped = ctrl.signal.aborted
      // 问题还没保存时撤回这次提问，把问题放回输入框
      if (!saved) {
        setTurns((ts) => ts.filter((t) => t.key !== key))
        if (draftRef.current === key) setDraft(null)
        setInput((cur) => cur || question)
        if (!stopped) toast(e instanceof ApiError ? e.message : '连接中断', 'err')
        return
      }
      patch((t) => ({ ...t, status: stopped ? 'stopped' : 'failed', error: stopped ? '' : e instanceof ApiError ? e.message : '连接中断，回答未完成' }))
    }

    api
      .ask({ chat_id: id, question, context, model_id: modelId }, onEvent, ctrl.signal)
      .then(() => {
        if (!answered) fail(new ApiError(0, '连接中断，回答未完成'))
      })
      .catch(fail)
      .finally(() => {
        qc.invalidateQueries({ queryKey: ['chats'] })
        if (chatId !== null) qc.invalidateQueries({ queryKey: ['chat', chatId] })
      })
  }

  const stop = () => running?.ctrl.abort()

  const startNew = () => {
    setDraft(null)
    setSelections((s) => {
      const next = { ...s }
      delete next.new
      return next
    })
    setListOpen(false)
    navigate('/chat')
  }

  const remove = useMutation({
    mutationFn: (c: ChatSummary) => api.deleteChat(c.id),
    onSuccess: (_, c) => {
      for (const t of turns) if (t.chatId === c.id && t.status === 'running') t.ctrl.abort()
      setTurns((ts) => ts.filter((t) => t.chatId !== c.id))
      qc.setQueryData<{ items: ChatSummary[] }>(['chats'], (old) => old && { items: old.items.filter((x) => x.id !== c.id) })
      qc.removeQueries({ queryKey: ['chat', c.id] })
      setRemoving(null)
      toast('已删除')
      if (id === c.id) navigate('/chat', { replace: true })
    },
    onError: (e) => toast(e.message, 'err'),
  })

  const end = useMutation({
    mutationFn: (chatId: number) => api.endChat(chatId),
    onSuccess: (c) => {
      qc.setQueryData<{ items: ChatSummary[] }>(['chats'], (old) => old && { items: old.items.map((x) => (x.id === c.id ? { ...x, ...c } : x)) })
      qc.setQueryData<ChatDetail>(['chat', c.id], (old) => old && { ...old, ...c })
      qc.invalidateQueries({ queryKey: ['chats'] })
      setEnding(false)
      toast('对话已结束')
    },
    onError: (e) => toast(e.message, 'err'),
  })

  const listItem = id !== null ? chats.data?.items.find((c) => c.id === id) : undefined
  const title = id === null ? '新对话' : detail.data?.title || listItem?.title || ''
  const ended = id !== null && Boolean(detail.data?.ended || listItem?.ended)
  const canEnd = id !== null && !ended && items.length > 0
  const showOwner = context.scope === 'all'
  const empty = id === null && viewTurns.length === 0

  const list = (
    <ChatList
      items={chats.data?.items}
      loading={chats.isPending}
      error={chats.error}
      onRetry={() => chats.refetch()}
      currentId={id}
      onPick={() => setListOpen(false)}
      onRemove={setRemoving}
    />
  )

  return (
    <div className="flex min-h-0 flex-1">
      <aside className="hidden w-60 shrink-0 flex-col border-r border-line pr-4 md:flex">
        <Button onClick={startNew} className="w-full">
          <SquarePen className="size-4" />
          新对话
        </Button>
        <div className="-mr-2 mt-3 min-h-0 flex-1 overflow-y-auto pr-2">{list}</div>
      </aside>

      <section className="flex min-w-0 flex-1 flex-col md:pl-6">
        <div className="flex h-10 shrink-0 items-center gap-1 border-b border-line">
          <Button size="sm" variant="ghost" className="-ml-1.5 md:hidden" onClick={() => setListOpen(true)} aria-label="历史对话" title="历史对话">
            <PanelLeft className="size-4" />
          </Button>
          <h2 className={cx('min-w-0 flex-1 truncate text-sm font-medium', title ? 'text-text' : 'text-muted')}>{title || '未命名对话'}</h2>
          {canEnd && (
            <Button size="sm" variant="ghost" onClick={() => setEnding(true)} disabled={running !== undefined} title="结束对话" aria-label="结束对话">
              <CircleStop className="size-3.5" />
              <span className="hidden sm:inline">结束对话</span>
            </Button>
          )}
          <Button size="sm" variant="ghost" className="-mr-1.5 md:hidden" onClick={startNew} aria-label="新对话" title="新对话">
            <SquarePen className="size-4" />
          </Button>
        </div>

        <div ref={scroller} onScroll={onScroll} className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-3xl py-5">
            {id !== null && detail.isPending && viewTurns.length === 0 && (
              <div className="grid h-40 place-items-center">
                <Spinner />
              </div>
            )}
            {id !== null && detail.isError && <ErrorLine error={detail.error} onRetry={() => detail.refetch()} />}
            {empty && <Examples onPick={send} disabled={!ready} />}
            <div className="space-y-6">
              {items.map((it) => {
                if (it.kind === 'question') return <UserBubble key={it.key} text={it.text} />
                if (it.kind === 'live') {
                  const t = it.turn
                  return (
                    <LiveAnswer
                      key={it.key}
                      steps={t.steps}
                      text={stripRawCites(t.text)}
                      running={t.status === 'running'}
                      error={t.error}
                      stopped={t.status === 'stopped'}
                    />
                  )
                }
                const m = it.message
                return m.role === 'user' ? <UserBubble key={it.key} text={m.content} /> : <AssistantMessage key={it.key} message={m} showOwner={showOwner} />
              })}
            </div>
          </div>
        </div>

        <div className="mx-auto w-full max-w-3xl pt-2">
          {ended ? (
            <div className="flex items-center justify-between gap-3 rounded-xl border border-line bg-surface/70 px-4 py-2.5">
              <span className="text-sm text-muted">对话已结束</span>
              <Button size="sm" variant="primary" onClick={startNew}>
                <SquarePen className="size-3.5" />
                新对话
              </Button>
            </div>
          ) : (
            <>
              <div className="mb-2">
                <ContextBar
                  admin={admin}
                  context={context}
                  minutes={sel.minutes}
                  onChange={(c, minutes) => updateSel(minutes ? { context: c, minutes } : { context: c })}
                />
              </div>
              <Composer
                value={input}
                onChange={setInput}
                onSubmit={() => send(input)}
                onStop={stop}
                running={running !== undefined}
                disabled={!ready}
                options={options}
                modelId={modelId}
                onModel={(v) => updateSel({ modelId: v })}
              />
            </>
          )}
        </div>
      </section>

      {listOpen && (
        <Drawer label="历史对话" onClose={() => setListOpen(false)}>
          <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
            <h2 className="text-[15px] font-semibold">历史对话</h2>
            <div className="flex items-center gap-1">
              <Button size="sm" onClick={startNew}>
                <SquarePen className="size-3.5" />
                新对话
              </Button>
              <button type="button" onClick={() => setListOpen(false)} className="rounded-md p-1.5 text-faint hover:bg-raised hover:text-text" aria-label="关闭">
                <X className="size-4" />
              </button>
            </div>
          </div>
          <div className="flex-1 overflow-y-auto p-3">{list}</div>
        </Drawer>
      )}

      {ending && id !== null && (
        <Modal
          title="结束对话"
          onClose={() => setEnding(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setEnding(false)}>
                取消
              </Button>
              <Button variant="primary" loading={end.isPending} onClick={() => end.mutate(id)}>
                结束对话
              </Button>
            </>
          }
        >
          <p className="text-sm text-muted">结束后不能继续提问，新的提问会开始新的对话。</p>
        </Modal>
      )}

      {removing && (
        <Modal
          title="删除对话"
          onClose={() => setRemoving(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setRemoving(null)}>
                取消
              </Button>
              <Button variant="danger" loading={remove.isPending} onClick={() => remove.mutate(removing)}>
                删除
              </Button>
            </>
          }
        >
          <p className="text-sm text-muted">
            删除「<span className="text-text">{removing.title || '未命名对话'}</span>」及其全部消息，删除后无法恢复。
          </p>
        </Modal>
      )}
    </div>
  )
}

function ChatList({
  items,
  loading,
  error,
  onRetry,
  currentId,
  onPick,
  onRemove,
}: {
  items?: ChatSummary[]
  loading: boolean
  error: Error | null
  onRetry: () => void
  currentId: number | null
  onPick: () => void
  onRemove: (c: ChatSummary) => void
}) {
  if (loading) return <Spinner className="mx-auto mt-8" />
  if (error) return <ErrorLine error={error} onRetry={onRetry} />
  if (!items || items.length === 0) return <p className="py-8 text-center text-[13px] text-faint">暂无对话</p>
  return (
    <ul className="space-y-0.5">
      {items.map((c) => (
        <li key={c.id} className="group relative">
          <Link
            to={`/chat/${c.id}`}
            onClick={onPick}
            className={cx(
              'block rounded-lg py-2 pr-9 pl-3 transition-colors',
              c.id === currentId ? 'bg-raised shadow-[inset_0_0_0_1px_var(--line-strong)]' : 'hover:bg-raised/60',
            )}
          >
            <div className="flex min-w-0 items-center gap-1.5">
              <span className={cx('min-w-0 truncate text-sm', c.title ? 'text-text' : 'text-muted')}>{c.title || '未命名对话'}</span>
              {c.ended && <span className="shrink-0 text-[11px] text-faint">已结束</span>}
            </div>
            <div className="mt-0.5 flex items-center gap-2 text-xs text-faint tabular-nums">
              <span>{formatTime(c.updated_at)}</span>
              {c.source === 'bot' && <span className="rounded border border-line-strong px-1 text-[11px] leading-4">机器人</span>}
            </div>
          </Link>
          <button
            type="button"
            onClick={() => onRemove(c)}
            className="absolute top-1/2 right-1.5 grid size-7 -translate-y-1/2 place-items-center rounded-md text-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-err/10 hover:text-err focus-visible:opacity-100 pointer-coarse:opacity-100"
            aria-label={`删除 ${c.title || '未命名对话'}`}
            title="删除"
          >
            <Trash2 className="size-3.5" />
          </button>
        </li>
      ))}
    </ul>
  )
}

function Examples({ onPick, disabled }: { onPick: (q: string) => void; disabled: boolean }) {
  return (
    <div className="flex min-h-[40dvh] flex-col items-center justify-center gap-2.5 px-2">
      {examples.map((q) => (
        <button
          key={q}
          type="button"
          disabled={disabled}
          onClick={() => onPick(q)}
          className="w-full max-w-sm rounded-xl border border-line bg-surface/70 px-4 py-2.5 text-left text-sm text-muted transition-colors hover:border-accent/40 hover:bg-raised hover:text-text disabled:opacity-50"
        >
          {q}
        </button>
      ))}
    </div>
  )
}

function Composer({
  value,
  onChange,
  onSubmit,
  onStop,
  running,
  disabled,
  options,
  modelId,
  onModel,
}: {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  onStop: () => void
  running: boolean
  disabled: boolean
  options: ChatModelOption[]
  modelId: string
  onModel: (id: string) => void
}) {
  const area = useRef<HTMLTextAreaElement>(null)

  // 随内容自动增高，超过上限后在框内滚动
  useLayoutEffect(() => {
    const el = area.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 220)}px`
  }, [value])

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Enter' || e.shiftKey) return
    // 输入法组字时的回车用于上屏，不发送
    if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return
    e.preventDefault()
    if (!running) onSubmit()
  }

  const canSend = !running && !disabled && value.trim() !== ''

  return (
    <div className="rounded-xl border border-line-strong bg-surface transition-[border-color,box-shadow] duration-150 focus-within:border-accent focus-within:shadow-[0_0_0_3px_var(--accent-soft)]">
      <textarea
        ref={area}
        rows={1}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="输入问题，Enter 发送，Shift + Enter 换行"
        className="block max-h-[220px] w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed text-text outline-none placeholder:text-faint"
        aria-label="问题"
      />
      <div className="flex items-center justify-end gap-2 px-2.5 pt-1 pb-2.5">
        {options.length > 0 && (
          <select
            value={modelId}
            onChange={(e) => onModel(e.target.value)}
            aria-label="模型"
            title="模型"
            className="h-7 max-w-[55%] min-w-0 truncate rounded-md border border-line-strong bg-surface px-2 text-[13px] text-muted outline-none transition-[border-color,box-shadow] duration-150 hover:text-text focus:border-accent focus:shadow-[0_0_0_3px_var(--accent-soft)]"
          >
            {options.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name || m.model}
              </option>
            ))}
          </select>
        )}
        {running ? (
          <Button size="sm" onClick={onStop} aria-label="停止" title="停止">
            <Square className="size-3 fill-current" />
            停止
          </Button>
        ) : (
          <button
            type="button"
            onClick={onSubmit}
            disabled={!canSend}
            aria-label="发送"
            title="发送"
            className="grid size-7 shrink-0 place-items-center rounded-lg bg-accent text-on-accent transition-colors hover:bg-accent-strong disabled:cursor-not-allowed disabled:opacity-40"
          >
            <ArrowUp className="size-4" />
          </button>
        )}
      </div>
    </div>
  )
}
