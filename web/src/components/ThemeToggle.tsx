import { Monitor, Moon, Sun } from 'lucide-react'
import { useTheme } from '../lib/theme'

const labels = { system: '跟随系统', dark: '深色', light: '浅色' }

// 主题切换：跟随系统 → 深色 → 浅色 循环
export function ThemeToggle() {
  const { mode, cycle } = useTheme()
  const Icon = mode === 'system' ? Monitor : mode === 'dark' ? Moon : Sun
  return (
    <button
      type="button"
      onClick={cycle}
      title={`主题：${labels[mode]}`}
      aria-label={`主题：${labels[mode]}`}
      className="grid size-8 place-items-center rounded-lg text-muted transition-colors hover:bg-raised hover:text-text"
    >
      <Icon className="size-4" />
    </button>
  )
}
