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

export function formatCountdown(seconds: number) {
  const s = Math.max(0, Math.floor(seconds))
  return `${pad(Math.floor(s / 60))}:${pad(s % 60)}`
}
