import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Avatar, ErrorLine, PageHeader, Segmented, Spinner, Switch } from '../components/ui'
import type { Member, Role } from '../lib/api'
import { api } from '../lib/api'
import { formatTime } from '../lib/format'
import { useToast } from '../lib/toast'
import { cx } from '../lib/ui'

// 成员页：授权用户列表，管理员可切换启用状态与角色
export function Users({ currentUserId }: { currentUserId: number }) {
  const q = useQuery({ queryKey: ['users'], queryFn: api.users })
  const items = q.data?.items ?? []

  return (
    <div>
      <PageHeader title="成员" actions={q.data && <span className="text-[13px] text-muted tabular-nums">共 {items.length} 人</span>} />
      {q.isError && <ErrorLine error={q.error} onRetry={() => q.refetch()} />}

      <div className="overflow-x-auto rounded-xl border border-line bg-surface/70">
        <table className="w-full min-w-[760px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-line text-left text-xs text-faint">
              <th className="px-4 py-2.5 font-normal">成员</th>
              <th className="px-4 py-2.5 font-normal">角色</th>
              <th className="px-4 py-2.5 font-normal">授权状态</th>
              <th className="px-4 py-2.5 font-normal">授权时间</th>
              <th className="px-4 py-2.5 font-normal">最近检查</th>
              <th className="px-4 py-2.5 text-right font-normal">记录数</th>
              <th className="px-4 py-2.5 text-right font-normal">自动生成</th>
            </tr>
          </thead>
          <tbody>
            {q.isPending && (
              <tr>
                <td colSpan={7} className="py-16 text-center">
                  <Spinner className="mx-auto" />
                </td>
              </tr>
            )}
            {items.map((m) => (
              <MemberRow key={m.id} member={m} self={m.id === currentUserId} />
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-faint">关闭自动生成后，不再为该成员发现新妙记；授权失效的成员重新扫码登录后恢复。</p>
    </div>
  )
}

function MemberRow({ member: m, self }: { member: Member; self: boolean }) {
  const qc = useQueryClient()
  const toast = useToast()
  const patch = useMutation({
    mutationFn: (body: { enabled?: boolean; role?: Role }) => api.updateUser(m.id, body),
    onSuccess: (u) => {
      qc.setQueryData<{ items: Member[] }>(['users'], (old) => old && { items: old.items.map((x) => (x.id === u.id ? u : x)) })
      toast('已更新')
    },
    onError: (e) => toast(e.message, 'err'),
  })

  return (
    <tr className={cx('border-b border-line last:border-0', !m.enabled && 'text-muted')}>
      <td className="px-4 py-3">
        <div className="flex items-center gap-3">
          <Avatar src={m.avatar_url} name={m.name} size={30} />
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate">{m.name}</span>
              {self && <span className="rounded border border-line-strong px-1 text-[11px] text-faint">我</span>}
            </div>
            <div className="truncate text-xs text-faint">{m.open_id}</div>
          </div>
        </div>
      </td>
      <td className="px-4 py-3">
        <Segmented<Role>
          value={m.role}
          onChange={(role) => role !== m.role && patch.mutate({ role })}
          options={[
            { value: 'member', label: '成员' },
            { value: 'admin', label: '管理员' },
          ]}
          className={cx((self || patch.isPending) && 'pointer-events-none opacity-60')}
        />
      </td>
      <td className="px-4 py-3">
        {m.auth_status === 'active' ? (
          <span className="inline-flex items-center gap-1.5 text-[13px] text-ok">
            <span className="size-1.5 rounded-full bg-current" />
            有效
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5 text-[13px] text-warn">
            <span className="size-1.5 rounded-full bg-current" />
            需重新登录
          </span>
        )}
      </td>
      <td className="px-4 py-3 whitespace-nowrap text-muted tabular-nums">{formatTime(m.authorized_at)}</td>
      <td className="px-4 py-3 whitespace-nowrap text-muted tabular-nums">{formatTime(m.last_sync_at)}</td>
      <td className="px-4 py-3 text-right tabular-nums">{m.minutes_total}</td>
      <td className="px-4 py-3">
        <div className="flex justify-end">
          <Switch checked={m.enabled} onChange={(enabled) => patch.mutate({ enabled })} disabled={patch.isPending} label={`为 ${m.name} 自动生成纪要`} />
        </div>
      </td>
    </tr>
  )
}
