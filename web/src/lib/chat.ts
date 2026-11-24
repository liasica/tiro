import type { ChatContext, ChatSource } from './api'

// 对话页的纯函数：引用标记、时间段

export const MAX_CONTEXT_MINUTES = 20

export function emptyContext(): ChatContext {
  return { scope: 'mine', minute_ids: [], start_date: '', end_date: '' }
}

// 流式输出里的原始引用标记 [#12]、[#12@03:14]，以及末尾尚未写完的 [#… 片段
const rawCite = /\[#\d+(?:@[\d:]*)?\]/g
const rawTail = /\[(?:#[\d@:]*)?$/

export function stripRawCites(text: string) {
  return text.replace(rawCite, '').replace(rawTail, '')
}

// 最终正文里的 {{cite:N}}、{{cite:N@mm:ss}} 换成 #cite- 开头的 Markdown 链接，由链接组件渲染成角标
const finalCite = /\{\{cite:(\d+)(?:@(\d{1,2}(?::\d{1,2}){1,2}))?\}\}/g

export function linkCites(text: string) {
  return text.replace(finalCite, (_, n: string, t?: string) => `[${n}](#cite-${n}${t ? `@${t}` : ''})`)
}

export function parseCiteHref(href: string): { index: number; time: string } | null {
  let value = href
  try {
    value = decodeURIComponent(href)
  } catch {
    // 无法解码时按原样匹配
  }
  const m = /^#cite-(\d+)(?:@([\d:]+))?$/.exec(value)
  return m ? { index: Number(m[1]), time: m[2] ?? '' } : null
}

// 时间标签换算成秒，03:14 与 3:14 视为同一时间点
function labelSeconds(label: string) {
  const parts = label.split(':').map(Number)
  if (parts.length === 0 || parts.some((p) => Number.isNaN(p))) return -1
  return parts.reduce((n, p) => n * 60 + p, 0)
}

// 角标的跳转地址：带时间的跳到妙记对应时间点，不带时间的打开纪要文档
export function citeTarget(src: ChatSource, time: string) {
  if (time) {
    const s = labelSeconds(time)
    const moment = src.moments.find((m) => m.label === time || (s >= 0 && labelSeconds(m.label) === s))
    return moment?.url || src.minute_url
  }
  return src.doc_url || src.minute_url
}

const pad = (n: number) => String(n).padStart(2, '0')

function dateKey(d: Date) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

function addDays(d: Date, n: number) {
  const x = new Date(d)
  x.setDate(x.getDate() + n)
  return x
}

export interface RangePreset {
  label: string
  start: string
  end: string
}

// 时间段快捷项，按当天计算，一周从周一开始
export function rangePresets(): RangePreset[] {
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const weekday = (today.getDay() + 6) % 7
  const monday = addDays(today, -weekday)
  const y = today.getFullYear()
  const m = today.getMonth()
  return [
    { label: '本周', start: dateKey(monday), end: dateKey(addDays(monday, 6)) },
    { label: '上周', start: dateKey(addDays(monday, -7)), end: dateKey(addDays(monday, -1)) },
    { label: '本月', start: dateKey(new Date(y, m, 1)), end: dateKey(new Date(y, m + 1, 0)) },
    { label: '最近 7 天', start: dateKey(addDays(today, -6)), end: dateKey(today) },
    { label: '最近 30 天', start: dateKey(addDays(today, -29)), end: dateKey(today) },
  ]
}

// 2026-11-10 显示成 11-10，不在今年的保留年份
function shortDate(value: string) {
  const [y, m, d] = value.split('-')
  if (!m || !d) return value
  return Number(y) === new Date().getFullYear() ? `${m}-${d}` : value
}

export function rangeLabel(start: string, end: string) {
  if (start && end) return start === end ? shortDate(start) : `${shortDate(start)} ~ ${shortDate(end)}`
  if (start) return `${shortDate(start)} ~`
  return `~ ${shortDate(end)}`
}
