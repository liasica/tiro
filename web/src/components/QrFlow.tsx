import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, ExternalLink, RotateCw, TimerOff, X } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import type { Flow, FlowStatus } from '../lib/api'
import { api } from '../lib/api'
import { formatCountdown } from '../lib/format'
import { cx } from '../lib/ui'
import { Button, Spinner } from './ui'

const QR_SIZE = 240

// 扫码流程：进入即生成二维码，每 2 秒轮询状态，成功后回调
export function QrFlow({ begin, onSuccess, successText }: { begin: () => Promise<Flow>; onSuccess: () => void; successText: string }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [deadline, setDeadline] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const started = useRef(false)
  const done = useRef(false)

  const beginMut = useMutation({
    mutationFn: begin,
    onSuccess: (f) => {
      setFlow(f)
      setDeadline(Date.now() + f.expires_in * 1000)
      setNow(Date.now())
    },
  })

  const regenerate = useCallback(() => {
    setFlow(null)
    beginMut.mutate()
  }, [beginMut])

  useEffect(() => {
    if (started.current) return
    started.current = true
    beginMut.mutate()
  }, [beginMut])

  const localExpired = flow !== null && now >= deadline

  const poll = useQuery({
    queryKey: ['flow', flow?.flow_id],
    queryFn: () => api.flow(flow!.flow_id),
    enabled: flow !== null && !localExpired,
    refetchInterval: (q) => (q.state.data && q.state.data.status !== 'pending' ? false : 2000),
    refetchIntervalInBackground: true,
    retry: false,
  })

  let status: FlowStatus | 'loading' = 'loading'
  if (flow) {
    // 查询本身失败（例如服务重启后流程丢失）按出错处理，提示重新生成
    const remote = poll.isError ? 'error' : (poll.data?.status ?? 'pending')
    status = remote === 'pending' && localExpired ? 'expired' : remote
  }

  // 倒计时
  useEffect(() => {
    if (status !== 'pending') return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [status])

  useEffect(() => {
    if (status === 'success' && !done.current) {
      done.current = true
      const t = window.setTimeout(onSuccess, 700)
      return () => window.clearTimeout(t)
    }
  }, [status, onSuccess])

  const remaining = Math.max(0, (deadline - now) / 1000)
  const failed = status === 'expired' || status === 'denied' || status === 'error'
  const beginError = beginMut.error instanceof Error ? beginMut.error.message : ''

  return (
    <div className="flex flex-col items-center">
      <div className="relative" style={{ ['--scan-h' as string]: `${QR_SIZE}px` }}>
        {/* 取景框四角 */}
        <Corners active={status === 'pending'} ok={status === 'success'} />
        <div className="relative m-4 overflow-hidden rounded-xl bg-[#eef0f6] p-4 shadow-[0_0_0_1px_var(--line-strong),0_0_60px_-10px_var(--glow)]">
          <div style={{ width: QR_SIZE, height: QR_SIZE }} className={cx('transition-[filter,opacity] duration-300', (failed || status === 'success') && 'opacity-15 blur-[3px]')}>
            {flow ? (
              <QRCodeSVG value={flow.url} size={QR_SIZE} bgColor="#eef0f6" fgColor="#0d1017" level="M" marginSize={0} />
            ) : (
              <div className="grid size-full place-items-center">{beginMut.isError ? <X className="size-6 text-[#c7443f]" /> : <Spinner className="size-6 text-[#5f667a]" />}</div>
            )}
          </div>
          {status === 'pending' && (
            <div className="scan-line pointer-events-none absolute inset-x-4 top-4 h-px bg-[#3f52d6] shadow-[0_0_12px_2px_rgb(63_82_214/0.45)]" />
          )}
          {status === 'success' && (
            <Overlay>
              <span className="anim-rise grid size-14 place-items-center rounded-full bg-[#1d8660] text-white shadow-[0_0_0_8px_rgb(29_134_96/0.15)]">
                <Check className="size-7" strokeWidth={2.5} />
              </span>
            </Overlay>
          )}
          {failed && (
            <Overlay>
              <Button variant="primary" onClick={regenerate} loading={beginMut.isPending} className="shadow-lg">
                <RotateCw className="size-4" />
                重新生成二维码
              </Button>
            </Overlay>
          )}
        </div>
      </div>

      <div className="mt-3 flex h-6 items-center gap-2 text-sm" aria-live="polite">
        {status === 'loading' && !beginMut.isError && <span className="text-muted">正在生成二维码</span>}
        {beginMut.isError && (
          <span className="flex items-center gap-2 text-err">
            {beginError}
            <Button size="sm" variant="ghost" onClick={regenerate}>
              重试
            </Button>
          </span>
        )}
        {status === 'pending' && (
          <>
            <span className="pulse-dot size-1.5 rounded-full bg-accent" />
            <span className="text-text">等待扫码</span>
            <span className="text-faint tabular-nums">{formatCountdown(remaining)} 后过期</span>
          </>
        )}
        {status === 'success' && <span className="text-ok">{successText}</span>}
        {status === 'expired' && (
          <span className="flex items-center gap-1.5 text-warn">
            <TimerOff className="size-4" />
            二维码已过期
          </span>
        )}
        {status === 'denied' && <span className="text-err">授权已被拒绝</span>}
        {status === 'error' && <span className="text-err">{poll.data?.error || (poll.error instanceof Error ? poll.error.message : '') || '扫码失败'}</span>}
      </div>

      {flow && status === 'pending' && (
        <a
          href={flow.url}
          target="_blank"
          rel="noreferrer"
          className="mt-2 inline-flex items-center gap-1 text-[13px] text-muted underline decoration-line-strong underline-offset-4 hover:text-accent hover:decoration-accent"
        >
          在浏览器中打开
          <ExternalLink className="size-3.5" />
        </a>
      )}
    </div>
  )
}

function Overlay({ children }: { children: ReactNode }) {
  return <div className="anim-fade absolute inset-0 grid place-items-center">{children}</div>
}

function Corners({ active, ok }: { active: boolean; ok: boolean }) {
  const tone = ok ? 'border-ok' : active ? 'border-accent' : 'border-line-strong'
  const base = cx('absolute size-5 transition-colors duration-300', tone)
  return (
    <div className="pointer-events-none absolute inset-0" aria-hidden>
      <span className={cx(base, 'top-0 left-0 rounded-tl-lg border-t-2 border-l-2')} />
      <span className={cx(base, 'top-0 right-0 rounded-tr-lg border-t-2 border-r-2')} />
      <span className={cx(base, 'bottom-0 left-0 rounded-bl-lg border-b-2 border-l-2')} />
      <span className={cx(base, 'right-0 bottom-0 rounded-br-lg border-r-2 border-b-2')} />
    </div>
  )
}
