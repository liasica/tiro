import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { Button, Spinner, Wordmark } from './components/ui'
import { api, setUnauthorizedHandler } from './lib/api'
import { Login } from './pages/Login'
import { Setup } from './pages/Setup'
import { Shell } from './pages/Shell'

// 按全局状态分流：初始化向导、登录页或后台
export function App() {
  const qc = useQueryClient()
  const state = useQuery({ queryKey: ['state'], queryFn: api.state, retry: 1 })

  useEffect(() => {
    setUnauthorizedHandler(() => qc.invalidateQueries({ queryKey: ['state'] }))
  }, [qc])

  if (state.isPending) {
    return (
      <div className="grid min-h-dvh place-items-center">
        <div className="flex flex-col items-center gap-4 opacity-70">
          <Wordmark size="lg" />
          <Spinner />
        </div>
      </div>
    )
  }

  if (state.isError) {
    return (
      <div className="grid min-h-dvh place-items-center px-4">
        <div className="flex flex-col items-center gap-4 text-center">
          <Wordmark size="lg" />
          <p className="text-sm text-err">{state.error.message}</p>
          <Button onClick={() => state.refetch()} loading={state.isFetching}>
            重新加载
          </Button>
        </div>
      </div>
    )
  }

  const s = state.data
  if (s.stage === 'app' || s.stage === 'auth') return <Setup state={s} />
  if (!s.user) return <Login state={s} />
  if (s.stage === 'llm' && s.user.role === 'admin') return <Setup state={s} />
  return <Shell state={s} user={s.user} />
}
