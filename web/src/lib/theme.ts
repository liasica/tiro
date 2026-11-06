import { useEffect, useState } from 'react'

// 主题模式：跟随系统或手动指定，手动指定时记在 localStorage
export type ThemeMode = 'system' | 'dark' | 'light'

const KEY = 'tiro-theme'
const media = () => window.matchMedia('(prefers-color-scheme: dark)')

function readMode(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'dark' || v === 'light') return v
  } catch {
    // 存储不可用时按跟随系统处理
  }
  return 'system'
}

function apply(mode: ThemeMode) {
  const dark = mode === 'system' ? media().matches : mode === 'dark'
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
}

export function useTheme() {
  const [mode, setMode] = useState<ThemeMode>(readMode)

  useEffect(() => {
    apply(mode)
    try {
      if (mode === 'system') localStorage.removeItem(KEY)
      else localStorage.setItem(KEY, mode)
    } catch {
      // 忽略存储异常
    }
    if (mode !== 'system') return
    const mq = media()
    const onChange = () => apply('system')
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [mode])

  const cycle = () => setMode((m) => (m === 'system' ? 'dark' : m === 'dark' ? 'light' : 'system'))

  return { mode, cycle }
}
