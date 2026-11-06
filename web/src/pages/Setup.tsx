import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, LogOut } from 'lucide-react'
import { useCallback, useState, type FormEvent, type ReactNode } from 'react'
import { LlmForm } from '../components/LlmForm'
import { QrFlow } from '../components/QrFlow'
import { ThemeToggle } from '../components/ThemeToggle'
import { Button, Field, Input, Wordmark } from '../components/ui'
import type { AppState } from '../lib/api'
import { api } from '../lib/api'
import { cx } from '../lib/ui'

const steps = ['创建飞书应用', '授权账号', '配置模型接口']

// 初始化向导：stage 决定当前步骤
export function Setup({ state }: { state: AppState }) {
  const step = state.stage === 'app' ? 0 : state.stage === 'auth' ? 1 : 2
  const qc = useQueryClient()
  const refresh = useCallback(() => qc.invalidateQueries({ queryKey: ['state'] }), [qc])

  return (
    <StageFrame
      version={state.version}
      right={state.user ? <LogoutButton /> : null}
    >
      <ol className="mb-10 flex items-center justify-center gap-3 text-[13px]">
        {steps.map((label, i) => (
          <li key={label} className="flex items-center gap-3">
            {i > 0 && <span className={cx('h-px w-6 sm:w-10', i <= step ? 'bg-accent/60' : 'bg-line-strong')} />}
            <span className={cx('flex items-center gap-2', i === step ? 'text-text' : i < step ? 'text-muted' : 'text-faint')}>
              <span
                className={cx(
                  'grid size-6 place-items-center rounded-full border text-xs tabular-nums',
                  i < step && 'border-accent/60 bg-accent-soft text-accent',
                  i === step && 'border-accent text-accent shadow-[0_0_0_4px_var(--accent-soft)]',
                  i > step && 'border-line-strong',
                )}
              >
                {i < step ? <Check className="size-3.5" strokeWidth={2.5} /> : i + 1}
              </span>
              <span className={cx(i !== step && 'hidden sm:inline')}>{label}</span>
            </span>
          </li>
        ))}
      </ol>

      <div key={step} className="anim-rise">
        {step === 0 && <AppStep onDone={refresh} />}
        {step === 1 && (
          <StepBody title="授权飞书账号" desc="使用飞书扫码授权。第一个授权的账号成为管理员。" meta={state.app_id ? `App ID ${state.app_id}` : undefined}>
            <QrFlow begin={api.beginAuthFlow} onSuccess={refresh} successText="已授权" />
          </StepBody>
        )}
        {step === 2 && (
          <StepBody title="配置模型接口" desc="纪要由 OpenAI 格式接口生成。保存后进入后台。">
            <div className="mx-auto w-full max-w-sm text-left">
              <LlmForm submitText="保存并进入后台" onSaved={refresh} />
            </div>
          </StepBody>
        )}
      </div>
    </StageFrame>
  )
}

function AppStep({ onDone }: { onDone: () => void }) {
  const [manual, setManual] = useState(false)

  if (manual) {
    return (
      <StepBody title="填写已有应用" desc="应用需开启机器人能力，并开通妙记搜索与读取、创建云文档与文件夹、以应用身份发消息的权限。">
        <ManualAppForm onDone={onDone} onBack={() => setManual(false)} />
      </StepBody>
    )
  }

  return (
    <StepBody title="创建飞书应用" desc="使用飞书扫码，按提示创建应用。创建完成后自动进入下一步。">
      <QrFlow begin={api.beginAppFlow} onSuccess={onDone} successText="应用已创建" />
      <button type="button" onClick={() => setManual(true)} className="mt-6 text-[13px] text-muted transition-colors hover:text-accent">
        手动填写已有应用
      </button>
    </StepBody>
  )
}

function ManualAppForm({ onDone, onBack }: { onDone: () => void; onBack: () => void }) {
  const [appId, setAppId] = useState('')
  const [secret, setSecret] = useState('')
  const save = useMutation({ mutationFn: () => api.manualApp(appId.trim(), secret.trim()), onSuccess: onDone })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (appId.trim() && secret.trim()) save.mutate()
  }

  return (
    <form onSubmit={onSubmit} className="mx-auto w-full max-w-sm space-y-4 text-left">
      <Field label="App ID" htmlFor="app-id">
        <Input id="app-id" value={appId} onChange={(e) => setAppId(e.target.value)} placeholder="cli_…" autoComplete="off" spellCheck={false} />
      </Field>
      <Field label="App Secret" htmlFor="app-secret">
        <Input id="app-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="new-password" spellCheck={false} />
      </Field>
      {save.isError && <p className="text-[13px] text-err">{save.error.message}</p>}
      <div className="flex gap-2 pt-1">
        <Button variant="primary" type="submit" loading={save.isPending} disabled={!appId.trim() || !secret.trim()}>
          保存并继续
        </Button>
        <Button variant="ghost" onClick={onBack}>
          返回扫码创建
        </Button>
      </div>
    </form>
  )
}

function StepBody({ title, desc, meta, children }: { title: string; desc: string; meta?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col items-center text-center">
      <h1 className="text-[22px] font-semibold tracking-tight">{title}</h1>
      <p className="mt-2 max-w-md text-sm text-balance text-muted">{desc}</p>
      {meta && <p className="mt-1 text-xs text-faint tabular-nums">{meta}</p>}
      <div className="mt-8 flex w-full flex-col items-center">{children}</div>
    </div>
  )
}

export function LogoutButton({ compact }: { compact?: boolean }) {
  const qc = useQueryClient()
  const logout = useMutation({
    mutationFn: api.logout,
    // 先重新拉全局状态让界面切到登录页，再丢弃上一个用户的其余缓存
    onSettled: async () => {
      await qc.invalidateQueries({ queryKey: ['state'] })
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'state' })
    },
  })
  return (
    <Button variant="ghost" size="sm" onClick={() => logout.mutate()} loading={logout.isPending} title="退出登录" aria-label="退出登录">
      {!logout.isPending && <LogOut className="size-3.5" />}
      {!compact && '退出登录'}
    </Button>
  )
}

// 全屏居中的外框：初始化向导与登录页共用
export function StageFrame({ children, version, right }: { children: ReactNode; version?: string; right?: ReactNode }) {
  return (
    <div className="ruled flex min-h-dvh flex-col">
      <header className="flex h-14 items-center justify-between px-4 sm:px-6">
        <Wordmark />
        <div className="flex items-center gap-1">
          {right}
          <ThemeToggle />
        </div>
      </header>
      <main className="flex flex-1 flex-col items-center justify-center px-4 py-10">
        <div className="w-full max-w-lg">{children}</div>
      </main>
      {version && <footer className="pb-5 text-center text-xs text-faint tabular-nums">tiro {version}</footer>}
    </div>
  )
}
