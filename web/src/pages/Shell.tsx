import { TriangleAlert } from 'lucide-react'
import { lazy, Suspense } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router'
import { ThemeToggle } from '../components/ThemeToggle'
import { Avatar, Spinner, Wordmark } from '../components/ui'
import type { AppState, User } from '../lib/api'
import { cx } from '../lib/ui'
import { Minutes } from './Minutes'
import { SettingsPage } from './Settings'
import { LogoutButton } from './Setup'
import { Users } from './Users'

// 对话页含 Markdown 渲染，按需加载
const Chat = lazy(() => import('./Chat').then((m) => ({ default: m.Chat })))

// 后台框架：顶栏导航 + 内容区
export function Shell({ state, user }: { state: AppState; user: User }) {
  const admin = user.role === 'admin'
  const nav = [
    { to: '/', label: '纪要', end: true },
    { to: '/chat', label: '对话', end: false },
    ...(admin
      ? [
          { to: '/users', label: '成员', end: true },
          { to: '/settings', label: '设置', end: true },
        ]
      : []),
  ]
  // 对话页撑满视口，消息区在页面内部滚动
  const { pathname } = useLocation()
  const full = pathname === '/chat' || pathname.startsWith('/chat/')

  return (
    <div className={cx('ruled flex flex-col', full ? 'h-dvh' : 'min-h-dvh')}>
      <header className="sticky top-0 z-30 border-b border-line bg-bg/80 backdrop-blur-md">
        <div className="mx-auto flex h-14 max-w-[1200px] items-center gap-2 px-4 sm:gap-8 sm:px-6">
          <NavLink to="/" aria-label="首页" className="shrink-0">
            <img src="/logo.svg" alt="" className="size-[34px] sm:hidden" draggable={false} />
            <span className="hidden sm:inline">
              <Wordmark />
            </span>
          </NavLink>
          <nav className="flex h-full min-w-0 items-stretch gap-0.5 sm:gap-1">
            {nav.map((n) => (
              <NavLink
                key={n.to}
                to={n.to}
                end={n.end}
                className={({ isActive }) =>
                  cx(
                    'relative flex items-center px-2 text-sm whitespace-nowrap transition-colors sm:px-2.5',
                    isActive
                      ? 'text-text after:absolute after:inset-x-2 after:-bottom-px after:h-px after:bg-accent after:shadow-[0_0_8px_var(--accent)] sm:after:inset-x-2.5'
                      : 'text-muted hover:text-text',
                  )
                }
              >
                {n.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex shrink-0 items-center gap-1 sm:gap-2">
            <ThemeToggle />
            <span className="mx-1 hidden h-4 w-px bg-line-strong sm:block" />
            <div className="flex items-center gap-2">
              <Avatar src={user.avatar_url} name={user.name} size={26} />
              <span className="hidden text-sm md:inline">{user.name}</span>
            </div>
            <LogoutButton compact />
          </div>
        </div>
      </header>

      {user.auth_status === 'expired' && (
        <div className="border-b border-err/25 bg-err/8">
          <div className="mx-auto flex max-w-[1200px] items-center gap-2 px-4 py-2 text-[13px] text-err sm:px-6">
            <TriangleAlert className="size-4 shrink-0" />
            飞书授权已失效，纪要暂不生成。退出登录后重新扫码即可恢复
          </div>
        </div>
      )}

      {state.stage === 'llm' && (
        <div className="border-b border-warn/25 bg-warn/8">
          <div className="mx-auto flex max-w-[1200px] items-center gap-2 px-4 py-2 text-[13px] text-warn sm:px-6">
            <TriangleAlert className="size-4 shrink-0" />
            模型接口尚未配置，纪要暂不生成
          </div>
        </div>
      )}

      <main className={cx('mx-auto w-full max-w-[1200px] flex-1 px-4 sm:px-6', full ? 'flex min-h-0 flex-col pt-2 pb-3 sm:pb-4' : 'py-8')}>
        <Routes>
          <Route path="/" element={<Minutes admin={admin} />} />
          <Route
            path="/chat/:id?"
            element={
              <Suspense fallback={<Spinner className="mx-auto mt-16" />}>
                <Chat admin={admin} />
              </Suspense>
            }
          />
          {admin && <Route path="/users" element={<Users currentUserId={user.id} />} />}
          {admin && <Route path="/settings" element={<SettingsPage />} />}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
      {!full && <footer className="mx-auto w-full max-w-[1200px] px-4 pb-5 text-xs text-faint tabular-nums sm:px-6">tiro {state.version}</footer>}
    </div>
  )
}
