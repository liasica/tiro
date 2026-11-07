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

export interface Settings {
  feishu: { app_id: string; app_secret_set: boolean }
  llm: { base_url: string; model: string; api_key_set: boolean }
  poll_interval_seconds: number
  folder_name: string
  min_transcript_chars: number
  glossary: string
  prompt: string
  prompt_is_default: boolean
}

export interface SettingsPatch {
  llm?: { base_url?: string; model?: string; api_key?: string }
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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(`/api${path}`, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
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

  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
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

  minutes: (p: { page: number; page_size: number; status: RecordStatus | ''; scope: Scope }) =>
    request<Page<MinuteRecord>>('GET', `/minutes${qs(p)}`),
  minute: (id: number) => request<MinuteDetail>('GET', `/minutes/${id}`),
  generate: (id: number) => request<void>('POST', `/minutes/${id}/generate`),
  addMinute: (url: string) => request<MinuteRecord>('POST', '/minutes', { url }),
  stats: (scope: Scope) => request<Stats>('GET', `/stats${qs({ scope })}`),

  settings: () => request<Settings>('GET', '/settings'),
  updateSettings: (patch: SettingsPatch) => request<Settings>('PUT', '/settings', patch),
  testLlm: (input: LlmTestInput) => request<LlmTestResult>('POST', '/settings/llm/test', input),

  users: () => request<{ items: Member[] }>('GET', '/users'),
  updateUser: (id: number, patch: { enabled?: boolean; role?: Role }) =>
    request<Member>('PATCH', `/users/${id}`, patch),
}
