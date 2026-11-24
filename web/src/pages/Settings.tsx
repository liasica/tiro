import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, ExternalLink, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useState, type FormEvent, type ReactNode } from 'react'
import { LlmForm } from '../components/LlmForm'
import { Button, ErrorLine, Field, Input, LinkButton, Modal, PageHeader, Spinner, Textarea } from '../components/ui'
import type { ChatApi, Settings, SettingsPatch } from '../lib/api'
import { api } from '../lib/api'
import { formatTime } from '../lib/format'
import { useToast } from '../lib/toast'
import { atLeast, cx, inputClass } from '../lib/ui'

// 设置页（仅管理员）：每个分区单独保存
export function SettingsPage() {
  const q = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const qc = useQueryClient()
  const toast = useToast()

  const saved = (s: Settings) => {
    qc.setQueryData(['settings'], s)
    qc.invalidateQueries({ queryKey: ['state'] })
    toast('已保存')
  }

  return (
    <div>
      <PageHeader title="设置" />
      {q.isPending && <Spinner className="mx-auto mt-16" />}
      {q.isError && <ErrorLine error={q.error} onRetry={() => q.refetch()} />}
      {q.data && (
        <div className="divide-y divide-line border-y border-line">
          <Section title="模型接口" desc="生成纪要使用的 OpenAI 格式接口。">
            <LlmForm initial={q.data.llm} onSaved={saved} />
          </Section>
          <Section title="对话模型" desc="对话可选的模型，按接口添加，未添加时使用纪要的模型接口。">
            <ChatModelsForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="运行参数">
            <RuntimeForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="术语表" desc="纪要按术语表纠正专有名词的转写错误。">
            <GlossaryForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="纪要提示词" desc="交给模型的完整提示词。">
            <PromptForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="飞书应用">
            <dl className="space-y-3 text-sm">
              <div className="flex items-center gap-4">
                <dt className="w-24 shrink-0 text-[13px] text-muted">App ID</dt>
                <dd className="tabular-nums select-all">{q.data.feishu.app_id || '—'}</dd>
              </div>
              <div className="flex items-center gap-4">
                <dt className="w-24 shrink-0 text-[13px] text-muted">App Secret</dt>
                <dd className={q.data.feishu.app_secret_set ? 'text-ok' : 'text-err'}>{q.data.feishu.app_secret_set ? '已保存' : '未保存'}</dd>
              </div>
            </dl>
          </Section>
          <Section title="机器人菜单" desc="在机器人私聊里点击菜单打开纪要页，飞书内打开时自动登录。">
            <MenuGuide appId={q.data.feishu.app_id} />
          </Section>
          <Section title="机器人对话" desc="在飞书里私聊机器人提问，按妙记内容回答。">
            <BotGuide bot={q.data.bot} appId={q.data.feishu.app_id} />
          </Section>
          <Section title="对话记录" desc="每个对话的问答写入提问人云空间里的多维表格，与纪要放在同一个文件夹。">
            <RecordGuide appId={q.data.feishu.app_id} />
          </Section>
        </div>
      )}
    </div>
  )
}

function Section({ title, desc, children }: { title: string; desc?: string; children: ReactNode }) {
  return (
    <section className="grid gap-4 py-8 md:grid-cols-[220px_minmax(0,1fr)] md:gap-10">
      <div>
        <h2 className="text-[15px] font-semibold">{title}</h2>
        {desc && <p className="mt-1 text-[13px] leading-relaxed text-muted">{desc}</p>}
      </div>
      <div className="max-w-2xl">{children}</div>
    </section>
  )
}

function useSave(onSaved: (s: Settings) => void) {
  return useMutation({ mutationFn: (p: SettingsPatch) => atLeast(api.updateSettings(p)), onSuccess: onSaved })
}

function SaveRow({ pending, disabled, error, extra }: { pending: boolean; disabled?: boolean; error?: Error | null; extra?: ReactNode }) {
  return (
    <div className="space-y-2 pt-1">
      {error && <p className="text-[13px] text-err">{error.message}</p>}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" type="submit" loading={pending} disabled={disabled}>
          保存
        </Button>
        {extra}
      </div>
    </div>
  )
}

function RuntimeForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [interval, setPollInterval] = useState(String(settings.poll_interval_seconds))
  const [folder, setFolder] = useState(settings.folder_name)
  const [minChars, setMinChars] = useState(String(settings.min_transcript_chars))
  const save = useSave(onSaved)

  const intervalN = Number(interval)
  const minN = Number(minChars)
  const valid = Number.isInteger(intervalN) && intervalN > 0 && Number.isInteger(minN) && minN >= 0 && folder.trim() !== ''
  const dirty =
    intervalN !== settings.poll_interval_seconds || folder.trim() !== settings.folder_name || minN !== settings.min_transcript_chars

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (valid && dirty) save.mutate({ poll_interval_seconds: intervalN, folder_name: folder.trim(), min_transcript_chars: minN })
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="检查间隔" htmlFor="rt-interval" hint="单位：秒">
          <Input id="rt-interval" type="number" min={1} step={1} value={interval} onChange={(e) => setPollInterval(e.target.value)} />
        </Field>
        <Field label="逐字稿最短字数" htmlFor="rt-min" hint="少于该字数的妙记跳过">
          <Input id="rt-min" type="number" min={0} step={1} value={minChars} onChange={(e) => setMinChars(e.target.value)} />
        </Field>
      </div>
      <Field label="云空间文件夹" htmlFor="rt-folder" hint="纪要文档创建在每位成员云空间的该文件夹下">
        <Input id="rt-folder" value={folder} onChange={(e) => setFolder(e.target.value)} />
      </Field>
      <SaveRow pending={save.isPending} disabled={!valid || !dirty} error={save.error} />
    </form>
  )
}

function GlossaryForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [text, setText] = useState(settings.glossary)
  const save = useSave(onSaved)
  const dirty = text.trim() !== settings.glossary
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (dirty) save.mutate({ glossary: text })
  }
  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <Field label="术语" htmlFor="glossary" hint="每行一条，写正确写法与常见的误转写，例如：Kubernetes：酷伯内提斯、库伯内特斯">
        <Textarea id="glossary" rows={8} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      </Field>
      <SaveRow pending={save.isPending} disabled={!dirty} error={save.error} />
    </form>
  )
}

function PromptForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [text, setText] = useState(settings.prompt)
  const save = useMutation({
    mutationFn: (p: SettingsPatch) => atLeast(api.updateSettings(p)),
    onSuccess: (s) => {
      setText(s.prompt)
      onSaved(s)
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (text.trim() && text !== settings.prompt) save.mutate({ prompt: text })
  }
  const reset = () => {
    if (window.confirm('恢复默认提示词？当前内容将被覆盖。')) save.mutate({ prompt: '' })
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <label htmlFor="prompt" className="text-[13px] font-medium text-muted">
            提示词
          </label>
          <span className="text-xs text-faint">{settings.prompt_is_default ? '默认' : '已自定义'}</span>
        </div>
        <Textarea id="prompt" rows={18} value={text} onChange={(e) => setText(e.target.value)} className="text-[13px]" spellCheck={false} />
      </div>
      <SaveRow
        pending={save.isPending}
        disabled={!text.trim() || text === settings.prompt}
        error={save.error}
        extra={
          !settings.prompt_is_default && (
            <Button variant="ghost" onClick={reset} disabled={save.isPending}>
              恢复默认
            </Button>
          )
        }
      />
    </form>
  )
}

// 对话模型接口的编辑草稿：整表在本地修改，统一保存
interface ProviderDraft {
  id: string
  name: string
  base_url: string
  api: ChatApi
  // 新填写的密钥，留空表示不修改
  api_key: string
  api_key_set: boolean
  // 已保存的地址，新接口为空串
  saved_url: string
  saved: boolean
  models: string[]
}

const trimUrl = (u: string) => u.trim().replace(/\/+$/, '')

function hostOf(url: string) {
  try {
    return new URL(url).host || url
  } catch {
    return url
  }
}

const providerLabel = (p: { name: string; base_url: string }) => p.name || hostOf(p.base_url)

const apiLabels: Record<ChatApi, string> = { responses: 'Responses', chat_completions: 'Chat Completions' }

// OpenAI 官方接口默认用 Responses，其余默认用 Chat Completions，与后端一致
const defaultApi = (url: string): ChatApi => (hostOf(trimUrl(url)) === 'api.openai.com' ? 'responses' : 'chat_completions')

function toDrafts(chat: Settings['chat']): ProviderDraft[] {
  return chat.providers.map((p) => ({
    id: p.id,
    name: p.name,
    base_url: p.base_url,
    api: p.api,
    api_key: '',
    api_key_set: p.api_key_set,
    saved_url: p.base_url,
    saved: true,
    models: p.models,
  }))
}

// 地址改了之后已保存的密钥不再沿用
const keyKept = (d: ProviderDraft) => d.api_key_set && trimUrl(d.base_url) === d.saved_url

// 接口 ID：8 位小写字母与数字；非 HTTPS 页面没有 randomUUID，用 getRandomValues 生成
function newProviderId(taken: string[]) {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789'
  for (;;) {
    const id = Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => chars[b % chars.length]).join('')
    if (!taken.includes(id)) return id
  }
}

function modelOptions(providers: ProviderDraft[]) {
  const multi = providers.length > 1
  return providers.flatMap((p) => p.models.map((m) => ({ value: `${p.id}:${m}`, label: multi ? `${providerLabel(p)} · ${m}` : m })))
}

// 默认模型不在勾选范围内时按后端规则取第一个接口的第一个模型
function effectiveDefault(providers: ProviderDraft[], id: string) {
  const options = modelOptions(providers)
  return options.some((o) => o.value === id) ? id : (options[0]?.value ?? '')
}

function toPatch(providers: ProviderDraft[], defaultId: string): NonNullable<SettingsPatch['chat']> {
  return {
    providers: providers.map((p) => ({
      id: p.id,
      name: p.name,
      base_url: p.base_url,
      api: p.api,
      ...(p.api_key ? { api_key: p.api_key } : {}),
      models: p.models,
    })),
    default_id: effectiveDefault(providers, defaultId),
  }
}

function ChatModelsForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const qc = useQueryClient()
  const [providers, setProviders] = useState(() => toDrafts(settings.chat))
  const [defaultId, setDefaultId] = useState(settings.chat.default_id)
  const [editing, setEditing] = useState<{ draft: ProviderDraft; isNew: boolean } | null>(null)

  const patch = toPatch(providers, defaultId)
  const dirty = JSON.stringify(patch) !== JSON.stringify(toPatch(toDrafts(settings.chat), settings.chat.default_id))
  const options = modelOptions(providers)

  const save = useMutation({
    mutationFn: () => atLeast(api.updateSettings({ chat: patch })),
    onSuccess: (s) => {
      setProviders(toDrafts(s.chat))
      setDefaultId(s.chat.default_id)
      qc.invalidateQueries({ queryKey: ['chat-models'] })
      onSaved(s)
    },
  })

  const add = () => {
    const baseUrl = settings.llm.base_url || 'https://api.openai.com/v1'
    setEditing({
      isNew: true,
      draft: {
        id: newProviderId(providers.map((p) => p.id)),
        name: '',
        base_url: baseUrl,
        api: defaultApi(baseUrl),
        api_key: '',
        api_key_set: false,
        saved_url: '',
        saved: false,
        models: [],
      },
    })
  }

  const commit = (d: ProviderDraft) => {
    setProviders((list) => (list.some((p) => p.id === d.id) ? list.map((p) => (p.id === d.id ? d : p)) : [...list, d]))
    setEditing(null)
  }

  return (
    <>
      <div className="space-y-4">
        {providers.length === 0 ? (
          <p className="rounded-lg border border-dashed border-line-strong px-3.5 py-3 text-[13px] text-muted">未添加时使用纪要的模型接口</p>
        ) : (
          <ul className="space-y-2">
            {providers.map((p) => (
              <ProviderCard
                key={p.id}
                draft={p}
                onEdit={() => setEditing({ draft: p, isNew: false })}
                onRemove={() => setProviders((list) => list.filter((x) => x.id !== p.id))}
              />
            ))}
          </ul>
        )}

        <Button onClick={add}>
          <Plus className="size-4" />
          添加接口
        </Button>

        {options.length > 0 && (
          <Field label="默认模型" htmlFor="chat-default">
            <select id="chat-default" value={patch.default_id} onChange={(e) => setDefaultId(e.target.value)} className={cx(inputClass, 'h-9 pr-8')}>
              {options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </Field>
        )}

        {save.isError && <p className="text-[13px] text-err">{save.error.message}</p>}
        <div className="pt-1">
          <Button variant="primary" onClick={() => save.mutate()} loading={save.isPending} disabled={!dirty}>
            保存
          </Button>
        </div>
      </div>
      {editing && <ProviderEditor draft={editing.draft} isNew={editing.isNew} onSubmit={commit} onClose={() => setEditing(null)} />}
    </>
  )
}

function ProviderCard({ draft: d, onEdit, onRemove }: { draft: ProviderDraft; onEdit: () => void; onRemove: () => void }) {
  const key = d.api_key ? '密钥已填写' : keyKept(d) ? '密钥已保存' : '未设置密钥'
  return (
    <li className="rounded-lg border border-line bg-surface px-3.5 py-3">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="min-w-0 text-sm font-medium break-all">{providerLabel(d)}</span>
            {!d.saved && <span className="shrink-0 text-[11px] text-faint">未保存</span>}
          </div>
          <div className="mt-0.5 text-xs break-all text-faint">
            {d.base_url} · {apiLabels[d.api]}
          </div>
          <div className={cx('mt-0.5 text-xs', key === '未设置密钥' ? 'text-faint' : 'text-ok')}>{key}</div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button size="sm" variant="ghost" onClick={onEdit} aria-label="编辑" title="编辑">
            <Pencil className="size-3.5" />
            <span className="hidden sm:inline">编辑</span>
          </Button>
          <Button size="sm" variant="ghost" onClick={onRemove} aria-label="删除" title="删除" className="hover:text-err">
            <Trash2 className="size-3.5" />
            <span className="hidden sm:inline">删除</span>
          </Button>
        </div>
      </div>
      <div className="mt-2.5 flex flex-wrap gap-1">
        {d.models.map((m) => (
          <span key={m} className="max-w-full rounded-md border border-line bg-raised px-1.5 py-px text-xs break-all text-muted">
            {m}
          </span>
        ))}
      </div>
    </li>
  )
}

function ProviderEditor({ draft, isNew, onSubmit, onClose }: { draft: ProviderDraft; isNew: boolean; onSubmit: (d: ProviderDraft) => void; onClose: () => void }) {
  const [name, setName] = useState(draft.name)
  const [baseUrl, setBaseUrl] = useState(draft.base_url)
  const [apiFormat, setApiFormat] = useState<ChatApi>(draft.api)
  // 新接口没有手动选过格式时，格式跟着地址变
  const [apiTouched, setApiTouched] = useState(!isNew)
  const [apiKey, setApiKey] = useState(draft.api_key)
  const [selected, setSelected] = useState<string[]>(draft.models)
  // 列表里除获取结果外还要保留的模型：原已勾选的与手动添加的
  const [known, setKnown] = useState<string[]>(draft.models)
  const [fetched, setFetched] = useState<{ id: string; owned_by: string }[] | null>(null)
  const [fetchError, setFetchError] = useState('')
  const [filter, setFilter] = useState('')
  const [manual, setManual] = useState('')

  const url = trimUrl(baseUrl)
  const urlChanged = draft.api_key_set && url !== draft.saved_url
  const keyKeptHere = draft.api_key_set && !urlChanged

  const load = useMutation({
    mutationFn: () =>
      api.llmModels({ base_url: url, ...(apiKey ? { api_key: apiKey } : {}), ...(draft.saved ? { provider_id: draft.id } : {}) }),
    onMutate: () => setFetchError(''),
    onSuccess: (r) => {
      if (r.ok) setFetched(r.models)
      else setFetchError(r.error)
    },
    onError: (e) => setFetchError(e.message),
  })

  const fetchedIds = new Set((fetched ?? []).map((m) => m.id))
  const rows = [...known.filter((m) => !fetchedIds.has(m)).map((id) => ({ id, owned_by: '' })), ...(fetched ?? [])]
  const q = filter.trim().toLowerCase()
  const visible = q ? rows.filter((r) => r.id.toLowerCase().includes(q)) : rows

  const toggle = (id: string) => setSelected((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))

  const addManual = () => {
    const id = manual.trim()
    if (!id) return
    if (!known.includes(id) && !fetchedIds.has(id)) setKnown((k) => [id, ...k])
    setSelected((s) => (s.includes(id) ? s : [...s, id]))
    setManual('')
  }

  const valid = url !== '' && selected.length > 0
  const submit = () => {
    if (valid) onSubmit({ ...draft, name: name.trim(), base_url: url, api: apiFormat, api_key: apiKey, models: selected })
  }

  return (
    <Modal
      title={isNew ? '添加接口' : '编辑接口'}
      size="lg"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" onClick={submit} disabled={!valid}>
            确定
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="名称（可选）" htmlFor="cp-name">
          <Input id="cp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={hostOf(url) || '接口名称'} autoComplete="off" />
        </Field>
        <Field label="接口地址" htmlFor="cp-url" hint="OpenAI 格式接口，填到版本路径，例如 https://api.openai.com/v1">
          <Input
            id="cp-url"
            value={baseUrl}
            onChange={(e) => {
              setBaseUrl(e.target.value)
              if (!apiTouched) setApiFormat(defaultApi(e.target.value))
            }}
            placeholder="https://api.example.com/v1"
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
        <Field label="接口格式" htmlFor="cp-api" hint="OpenAI 官方接口选 Responses">
          <select
            id="cp-api"
            value={apiFormat}
            onChange={(e) => {
              setApiFormat(e.target.value as ChatApi)
              setApiTouched(true)
            }}
            className={cx(inputClass, 'h-9 pr-8')}
          >
            <option value="responses">{apiLabels.responses}</option>
            <option value="chat_completions">{apiLabels.chat_completions}</option>
          </select>
        </Field>
        <Field
          label="API Key"
          htmlFor="cp-key"
          hint={urlChanged ? '接口地址已修改，原密钥不再沿用，需要时重新填写' : keyKeptHere ? undefined : '接口不需要密钥时留空'}
        >
          <Input
            id="cp-key"
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={keyKeptHere ? '已保存，留空不修改' : 'sk-…'}
            autoComplete="new-password"
            spellCheck={false}
          />
        </Field>
        <Button onClick={() => load.mutate()} loading={load.isPending} disabled={url === ''}>
          {!load.isPending && <RefreshCw className="size-4" />}
          获取模型列表
        </Button>

        {fetchError && <ErrorLine error={fetchError} />}

        <div className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <span className="text-[13px] font-medium text-muted">模型</span>
            <span className="text-xs text-faint tabular-nums">已选 {selected.length} 个</span>
          </div>
          {fetched !== null && <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="按模型名过滤" spellCheck={false} />}
          {rows.length > 0 && (
            <div className="max-h-64 overflow-y-auto rounded-lg border border-line">
              {visible.map((r) => (
                <label key={r.id} className="flex cursor-pointer items-center gap-3 border-b border-line px-3 py-2 last:border-0 hover:bg-raised/60">
                  <input type="checkbox" checked={selected.includes(r.id)} onChange={() => toggle(r.id)} className="size-3.5 shrink-0 accent-accent" />
                  <span className="min-w-0 flex-1 text-sm break-all">{r.id}</span>
                  {r.owned_by && <span className="max-w-[40%] shrink-0 truncate text-xs text-faint">{r.owned_by}</span>}
                </label>
              ))}
              {visible.length === 0 && <p className="py-6 text-center text-[13px] text-muted">没有符合条件的模型</p>}
            </div>
          )}
          <div className="flex gap-2">
            <Input
              value={manual}
              onChange={(e) => setManual(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  addManual()
                }
              }}
              placeholder="手动添加模型名"
              aria-label="手动添加模型名"
              autoComplete="off"
              spellCheck={false}
              className="min-w-0 flex-1"
            />
            <Button onClick={addManual} disabled={manual.trim() === ''}>
              <Plus className="size-4" />
              添加
            </Button>
          </div>
        </div>
      </div>
    </Modal>
  )
}

// 开发者后台的应用页链接，缺少 App ID 时只显示文字
function ConsoleLink({ appId, children }: { appId: string; children: ReactNode }) {
  if (!appId) return <>{children}</>
  return (
    <a href={`https://open.feishu.cn/app/${appId}`} target="_blank" rel="noreferrer" className="text-accent hover:text-accent-strong">
      {children}
    </a>
  )
}

function Code({ children }: { children: ReactNode }) {
  return <code className="rounded bg-raised px-1 py-0.5 text-xs text-text">{children}</code>
}

function BotGuide({ bot, appId }: { bot: Settings['bot']; appId: string }) {
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <span className={cx('inline-flex items-center gap-1.5', bot.connected ? 'text-ok' : 'text-warn')}>
          <span className="size-1.5 rounded-full bg-current" />
          {bot.connected ? '已连接' : '未连接'}
        </span>
        {bot.connected && bot.connected_at && <span className="text-[13px] text-muted tabular-nums">连接时间 {formatTime(bot.connected_at)}</span>}
        {!bot.connected && bot.error && <span className="min-w-0 text-[13px] break-all text-err">{bot.error}</span>}
      </div>
      <ol className="list-decimal space-y-1.5 pl-5 text-[13px] leading-relaxed text-muted">
        <li>
          在飞书开发者后台<ConsoleLink appId={appId}>打开应用</ConsoleLink>，进入「事件与回调」，订阅方式选择「使用长连接接收事件」并保存
        </li>
        <li>
          添加事件「接收消息」（<Code>im.message.receive_v1</Code>）
        </li>
        <li>
          「权限管理」中开通「读取用户发给机器人的单聊消息」（<Code>im:message.p2p_msg:readonly</Code>）
        </li>
        <li>创建版本并发布</li>
      </ol>
      <p className="text-[13px] leading-relaxed text-muted">私聊机器人发送「结束」或「新对话」结束当前对话，下一条提问开始新的对话。</p>
    </div>
  )
}

function RecordGuide({ appId }: { appId: string }) {
  return (
    <ol className="list-decimal space-y-1.5 pl-5 text-[13px] leading-relaxed text-muted">
      <li>
        在<ConsoleLink appId={appId}>开发者后台</ConsoleLink>「权限管理」开通「查看、评论、编辑和管理多维表格」（<Code>bitable:app</Code>）
      </li>
      <li>创建版本并发布</li>
      <li>已授权的成员重新扫码登录一次</li>
    </ol>
  )
}

// 机器人菜单的配置步骤，链接按当前访问的地址生成
function MenuGuide({ appId }: { appId: string }) {
  const page = `${location.origin}/`
  const links = [
    { label: '重定向 URL', value: `${location.origin}/api/auth/feishu/callback` },
    { label: '菜单链接', value: page },
    { label: '桌面端链接', value: `https://applink.feishu.cn/client/web_url/open?mode=sidebar-semi&url=${encodeURIComponent(page)}` },
  ]

  return (
    <div className="space-y-5">
      <ol className="list-decimal space-y-1.5 pl-5 text-[13px] leading-relaxed text-muted">
        <li>在开发者后台「安全设置」的重定向 URL 中添加下方的重定向 URL</li>
        <li>在「机器人」页开启机器人自定义菜单，添加菜单项，响应动作选「跳转至指定链接」并填写菜单链接；桌面端填写桌面端链接时，页面在聊天侧边栏打开</li>
        <li>创建版本并发布，约 5 分钟后菜单出现在机器人私聊的输入框旁</li>
      </ol>
      <dl className="space-y-3">
        {links.map((link) => (
          <CopyRow key={link.label} label={link.label} value={link.value} />
        ))}
      </dl>
      {appId && (
        <LinkButton href={`https://open.feishu.cn/app/${appId}`}>
          <ExternalLink className="size-4" />
          打开开发者后台
        </LinkButton>
      )}
    </div>
  )
}

function CopyRow({ label, value }: { label: string; value: string }) {
  const toast = useToast()
  // 非 HTTPS 页面里没有剪贴板接口，访问时抛错同样按复制失败处理
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      toast('已复制')
    } catch {
      toast('复制失败，请手动选择复制', 'err')
    }
  }

  return (
    <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-4">
      <dt className="w-24 shrink-0 text-[13px] text-muted">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center gap-2">
        <code className="min-w-0 flex-1 rounded-md border border-line bg-bg px-2.5 py-1.5 text-xs break-all select-all">{value}</code>
        <Button size="sm" variant="ghost" onClick={copy} title="复制" aria-label={`复制${label}`}>
          <Copy className="size-3.5" />
        </Button>
      </dd>
    </div>
  )
}
