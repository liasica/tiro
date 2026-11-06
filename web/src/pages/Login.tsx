import { useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { QrFlow } from '../components/QrFlow'
import type { AppState } from '../lib/api'
import { api } from '../lib/api'
import { StageFrame } from './Setup'

// 登录页：扫码授权即登录
export function Login({ state }: { state: AppState }) {
  const qc = useQueryClient()
  const refresh = useCallback(() => qc.invalidateQueries({ queryKey: ['state'] }), [qc])

  return (
    <StageFrame version={state.version}>
      <div className="anim-rise flex flex-col items-center text-center">
        <h1 className="text-[22px] font-semibold tracking-tight">登录</h1>
        <p className="mt-2 text-sm text-muted">使用飞书扫码授权，授权后自动登录。</p>
        <div className="mt-8">
          <QrFlow begin={api.beginAuthFlow} onSuccess={refresh} successText="已登录" />
        </div>
      </div>
    </StageFrame>
  )
}
