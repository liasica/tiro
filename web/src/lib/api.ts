// 接口类型与请求封装，契约见后端 /api

export type Stage = 'app' | 'auth' | 'llm' | 'ready'
export type Role = 'admin' | 'member'
export type AuthStatus = 'active' | 'expired'

export interface User {
  id: number
  open_id: string
  name: string
  avatar_url: string
  role: Role
  auth_status: AuthStatus
}

export interface AppState {
  stage: Stage
  version: string
  app_id: string
  user: User | null
}

export interface Flow {
  flow_id: string
  url: string
  expires_in: number
}

export type FlowStatus = 'pending' | 'success' | 'denied' | 'expired' | 'error'

export interface FlowState {
  status: FlowStatus
  error: string
}

export type RecordStatus = 'pending' | 'processing' | 'done' | 'failed' | 'skipped'

export interface Owner {
  id: number
  name: string
  avatar_url: string
}

export interface MinuteRecord {
  id: number
  minute_token: string
  minute_url: string
  title: string
  owner: Owner
  minute_created_at: string | null
  duration_seconds: number
  status: RecordStatus
  reason: string
  attempts: number
  doc_url: string
  total_tokens: number
  created_at: string | null
  finished_at: string | null
}

export interface LlmCall {
  id: number
  model: string
  prompt_tokens: number
  cached_tokens: number
  completion_tokens: number
  reasoning_tokens: number
  total_tokens: number
  duration_ms: number
  error: string
  created_at: string | null
}

export interface MinuteDetail extends MinuteRecord {
  summary_markdown: string
  llm_calls: LlmCall[]
}

export interface Page<T> {
  items: T[]
  total: number
}

export type Scope = 'mine' | 'all'

export interface Stats {
  total: number
  done: number
  failed: number
  pending: number
  skipped: number
  total_tokens: number
  last_sync_at: string | null
}

export interface ChatContext {
  scope: Scope
  minute_ids: number[]
  start_date: string
  end_date: string
}

export interface ChatModelOption {
  id: string
  name: string
  model: string
}

export interface ChatSummary {
  id: number
  title: string
  source: 'web' | 'bot'
  ended: boolean
  created_at: string | null
  updated_at: string | null
}

export type ChatStepKind = 'search' | 'read' | 'transcript'

export interface ChatStep {
  kind: ChatStepKind
  text: string
}

export interface ChatMoment {
  label: string
  url: string
}

export interface ChatSource {
  index: number
  minute_id: number
  title: string
  owner: string
  minute_created_at: string | null
  duration_seconds: number
  minute_url: string
  doc_url: string
  moments: ChatMoment[]
}

// 回答整理成结果文档的状态，空串表示这条回答不整理
export type ChatResultState = '' | 'pending' | 'done' | 'skipped' | 'failed'

export interface ChatResult {
  state: ChatResultState
  kind: string
  label: string
  title: string
  url: string
  error: string
  // 失败后还会自动再试
  retrying: boolean
}

export interface ChatMessage {
  id: number
  role: 'user' | 'assistant'
  content: string
  steps: ChatStep[]
  sources: ChatSource[]
  model: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  duration_ms: number
  error: string
  result: ChatResult
  created_at: string | null
}

export interface ContextMinute {
  id: number
  title: string
  minute_created_at: string | null
}

export interface ChatDetail extends ChatSummary {
  context: ChatContext
  context_minutes: ContextMinute[]
  model_id: string
  messages: ChatMessage[]
}

export interface ChatAskInput {
  chat_id: number | null
  question: string
  context: ChatContext
  model_id: string
}

// 提问接口推送的 SSE 事件
export type ChatEvent =
  | { event: 'chat'; data: ChatSummary }
  | { event: 'question'; data: ChatMessage }
  | { event: 'step'; data: ChatStep }
  | { event: 'delta'; data: { text: string } }
  | { event: 'reset'; data: Record<string, never> }
  | { event: 'answer'; data: ChatMessage }

// 对话模型按接口配置，models 是勾选给对话用的模型
// 对话模型接口的格式
export type ChatApi = 'chat_completions' | 'responses'

export interface ChatProviderSetting {
  id: string
  name: string
  base_url: string
  api: ChatApi
  api_key_set: boolean
  models: string[]
}

export interface ChatProviderInput {
  id: string
  name: string
  base_url: string
  api: ChatApi
  api_key?: string
  models: string[]
}

export interface Settings {
  feishu: { app_id: string; app_secret_set: boolean }
  llm: { base_url: string; model: string; api_key_set: boolean }
  // default_id 形如 `${provider_id}:${model}`
  chat: { providers: ChatProviderSetting[]; default_id: string }
  bot: { connected: boolean; error: string; connected_at: string | null }
  poll_interval_seconds: number
  folder_name: string
  min_transcript_chars: number
  glossary: string
  prompt: string
  prompt_is_default: boolean
}

export interface SettingsPatch {
  llm?: { base_url?: string; model?: string; api_key?: string }
  chat?: { providers: ChatProviderInput[]; default_id: string }
  poll_interval_seconds?: number
  folder_name?: string
  min_transcript_chars?: number
  glossary?: string
  prompt?: string
}

export interface LlmTestInput {
  base_url: string
  model: string
  api_key?: string
}

export interface LlmModelsInput {
  base_url: string
  api_key?: string
  provider_id?: string
}

export type LlmModelsResult = { ok: true; models: { id: string; owned_by: string }[] } | { ok: false; error: string }

export type LlmTestResult =
  | { ok: true; reply: string; latency_ms: number }
  | { ok: false; error: string }

export interface Member {
  id: number
  open_id: string
  name: string
  avatar_url: string
  role: Role
  enabled: boolean
  auth_status: AuthStatus
  authorized_at: string | null
  last_sync_at: string | null
  minutes_total: number
}

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

// 401 时通知上层重新拉取全局状态
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

// 发出请求并处理错误响应，成功时返回原始响应
async function send(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<Response> {
  let res: Response
  try {
    res = await fetch(`/api${path}`, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (e) {
    if (signal?.aborted) throw e
    throw new ApiError(0, '无法连接服务')
  }

  if (!res.ok) {
    let message = `请求失败（${res.status}）`
    try {
      const data = (await res.json()) as { error?: string }
      if (data.error) message = data.error
    } catch {
      // 响应体不是 JSON 时沿用默认信息
    }
    if (res.status === 401 && path !== '/state') onUnauthorized?.()
    throw new ApiError(res.status, message)
  }
  return res
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await send(method, path, body)
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

// 解析一个 SSE 事件块，只认 event 与 data 字段
function parseEvent(block: string): ChatEvent | null {
  let event = 'message'
  const data: string[] = []
  for (const line of block.split(/\r?\n/)) {
    if (!line || line.startsWith(':')) continue
    const i = line.indexOf(':')
    const field = i < 0 ? line : line.slice(0, i)
    let value = i < 0 ? '' : line.slice(i + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'data') data.push(value)
  }
  if (data.length === 0) return null
  try {
    return { event, data: JSON.parse(data.join('\n')) } as ChatEvent
  } catch {
    return null
  }
}

// 逐块读取 SSE 流，一个 chunk 可能只含半个事件，按空行切分后再解析
async function readEvents(body: ReadableStream<Uint8Array>, onEvent: (e: ChatEvent) => void) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  const boundary = /\r?\n\r?\n/
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) {
      buf += decoder.decode()
      break
    }
    buf += decoder.decode(value, { stream: true })
    let m: RegExpExecArray | null
    while ((m = boundary.exec(buf))) {
      const block = buf.slice(0, m.index)
      buf = buf.slice(m.index + m[0].length)
      const e = parseEvent(block)
      if (e) onEvent(e)
    }
  }
  const rest = buf.trim() ? parseEvent(buf) : null
  if (rest) onEvent(rest)
}

function qs(params: Record<string, string | number | undefined>) {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') s.set(k, String(v))
  }
  const str = s.toString()
  return str ? `?${str}` : ''
}

export const api = {
  state: () => request<AppState>('GET', '/state'),

  beginAppFlow: () => request<Flow>('POST', '/setup/app/begin'),
  manualApp: (app_id: string, app_secret: string) =>
    request<void>('POST', '/setup/app/manual', { app_id, app_secret }),
  beginAuthFlow: () => request<Flow>('POST', '/auth/begin'),
  flow: (id: string) => request<FlowState>('GET', `/flows/${encodeURIComponent(id)}`),
  logout: () => request<void>('POST', '/auth/logout'),

  minutes: (p: { page: number; page_size: number; status: RecordStatus | ''; scope: Scope; q?: string }) =>
    request<Page<MinuteRecord>>('GET', `/minutes${qs(p)}`),
  minute: (id: number) => request<MinuteDetail>('GET', `/minutes/${id}`),
  generate: (id: number) => request<void>('POST', `/minutes/${id}/generate`),
  addMinute: (url: string) => request<MinuteRecord>('POST', '/minutes', { url }),
  stats: (scope: Scope) => request<Stats>('GET', `/stats${qs({ scope })}`),

  settings: () => request<Settings>('GET', '/settings'),
  updateSettings: (patch: SettingsPatch) => request<Settings>('PUT', '/settings', patch),
  testLlm: (input: LlmTestInput) => request<LlmTestResult>('POST', '/settings/llm/test', input),
  llmModels: (input: LlmModelsInput) => request<LlmModelsResult>('POST', '/settings/llm/models', input),

  chatModels: () => request<{ items: ChatModelOption[]; default_id: string }>('GET', '/chat/models'),
  chats: () => request<{ items: ChatSummary[] }>('GET', '/chats'),
  chat: (id: number) => request<ChatDetail>('GET', `/chats/${id}`),
  deleteChat: (id: number) => request<void>('DELETE', `/chats/${id}`),
  endChat: (id: number) => request<ChatSummary>('POST', `/chats/${id}/end`),
  // 提问：校验失败时抛出 ApiError，成功后按顺序回调流中的事件，直到流结束
  ask: async (input: ChatAskInput, onEvent: (e: ChatEvent) => void, signal?: AbortSignal) => {
    const res = await send('POST', '/chats/ask', input, signal)
    if (!res.body) throw new ApiError(0, '响应为空')
    await readEvents(res.body, onEvent)
  },

  users: () => request<{ items: Member[] }>('GET', '/users'),
  updateUser: (id: number, patch: { enabled?: boolean; role?: Role }) =>
    request<Member>('PATCH', `/users/${id}`, patch),
}
