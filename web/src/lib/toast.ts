import { createContext, useContext } from 'react'

// 轻提示：操作结果的简短反馈
export type ToastTone = 'ok' | 'err'

export const ToastContext = createContext<(message: string, tone?: ToastTone) => void>(() => {})

export function useToast() {
  return useContext(ToastContext)
}
