// 时间与时长格式化，一律显示具体时间点

const pad = (n: number) => String(n).padStart(2, '0')

export function formatTime(iso: string | null | undefined, withSeconds = false) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const base = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return withSeconds ? `${base}:${pad(d.getSeconds())}` : base
}

export function formatDuration(seconds: number) {
  if (!seconds || seconds < 0) return '—'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (h > 0) return `${h} 小时 ${m} 分`
  if (m > 0) return `${m} 分 ${s} 秒`
  return `${s} 秒`
}

// 毫秒级耗时，不足 1 秒时按毫秒显示
export function formatMillis(ms: number) {
  if (!ms || ms < 0) return '—'
  if (ms < 1000) return `${ms} 毫秒`
  return formatDuration(Math.round(ms / 1000))
}

const grouped = new Intl.NumberFormat('zh-CN')
const compact = new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 })

// 带千位分隔符的完整数字
export function formatCount(n: number) {
  return grouped.format(n)
}

// 统计卡片与列表里的 token 数，过万后写成「12.3万」
export function formatTokens(n: number) {
  return n > 0 ? compact.format(n) : '—'
}

export function formatCountdown(seconds: number) {
  const s = Math.max(0, Math.floor(seconds))
  return `${pad(Math.floor(s / 60))}:${pad(s % 60)}`
}
