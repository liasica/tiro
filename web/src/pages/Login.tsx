import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'
import { QrFlow } from '../components/QrFlow'
import { Button, Spinner } from '../components/ui'
import type { AppState } from '../lib/api'
import { api } from '../lib/api'
import { StageFrame } from './Setup'

// 飞书客户端内置浏览器的 UA 带 Lark/ 版本号
const inFeishu = /\bLark\/|Feishu/i.test(navigator.userAgent)

// 飞书网页授权入口，授权完成后回到当前页面
function feishuLoginURL() {
  return '/api/auth/feishu?next=' + encodeURIComponent(location.pathname + location.search)
}

// 登录页：飞书客户端内自动走网页授权，其他浏览器扫码授权，授权即登录
export function Login({ state }: { state: AppState }) {
  const qc = useQueryClient()
  const refresh = useCallback(() => qc.invalidateQueries({ queryKey: ['state'] }), [qc])
  const [error] = useState(() => new URLSearchParams(location.search).get('login_error') ?? '')
  const auto = inFeishu && !error

  useEffect(() => {
    if (auto) {
      location.replace(feishuLoginURL())
    } else if (error) {
      history.replaceState(null, '', location.pathname)
    }
  }, [auto, error])

  if (auto) {
    return (
      <StageFrame version={state.version}>
        <div className="flex flex-col items-center gap-3 text-sm text-muted">
          <Spinner />
          正在登录
        </div>
      </StageFrame>
    )
  }

  return (
    <StageFrame version={state.version}>
      <div className="anim-rise flex flex-col items-center text-center">
        <h1 className="text-[22px] font-semibold tracking-tight">登录</h1>
        <p className="mt-2 text-sm text-muted">使用飞书扫码授权，授权后自动登录。</p>
        {error && <p className="mt-4 max-w-sm text-[13px] text-err">{error}</p>}
        {inFeishu && (
          <Button variant="primary" className="mt-4" onClick={() => location.assign(feishuLoginURL())}>
            重新登录
          </Button>
        )}
        <div className="mt-8">
          <QrFlow begin={api.beginAuthFlow} onSuccess={refresh} successText="已登录" />
        </div>
      </div>
    </StageFrame>
  )
}
