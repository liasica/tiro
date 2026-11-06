import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent, type ReactNode } from 'react'
import { LlmForm } from '../components/LlmForm'
import { Button, ErrorLine, Field, Input, PageHeader, Spinner, Textarea } from '../components/ui'
import type { Settings, SettingsPatch } from '../lib/api'
import { api } from '../lib/api'
import { useToast } from '../lib/toast'

// 设置页（仅管理员）：每个分区单独保存
export function SettingsPage() {
  const q = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const qc = useQueryClient()
  const toast = useToast()

  const saved = (s: Settings) => {
    qc.setQueryData(['settings'], s)
    qc.invalidateQueries({ queryKey: ['state'] })
    toast('已保存')
  }

  return (
    <div>
      <PageHeader title="设置" />
      {q.isPending && <Spinner className="mx-auto mt-16" />}
      {q.isError && <ErrorLine error={q.error} onRetry={() => q.refetch()} />}
      {q.data && (
        <div className="divide-y divide-line border-y border-line">
          <Section title="模型接口" desc="生成纪要使用的 OpenAI 格式接口。">
            <LlmForm initial={q.data.llm} onSaved={saved} />
          </Section>
          <Section title="运行参数">
            <RuntimeForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="术语表" desc="纪要按术语表纠正专有名词的转写错误。">
            <GlossaryForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="纪要提示词" desc="交给模型的完整提示词。">
            <PromptForm settings={q.data} onSaved={saved} />
          </Section>
          <Section title="飞书应用">
            <dl className="space-y-3 text-sm">
              <div className="flex items-center gap-4">
                <dt className="w-24 shrink-0 text-[13px] text-muted">App ID</dt>
                <dd className="tabular-nums select-all">{q.data.feishu.app_id || '—'}</dd>
              </div>
              <div className="flex items-center gap-4">
                <dt className="w-24 shrink-0 text-[13px] text-muted">App Secret</dt>
                <dd className={q.data.feishu.app_secret_set ? 'text-ok' : 'text-err'}>{q.data.feishu.app_secret_set ? '已保存' : '未保存'}</dd>
              </div>
            </dl>
          </Section>
        </div>
      )}
    </div>
  )
}

function Section({ title, desc, children }: { title: string; desc?: string; children: ReactNode }) {
  return (
    <section className="grid gap-4 py-8 md:grid-cols-[220px_minmax(0,1fr)] md:gap-10">
      <div>
        <h2 className="text-[15px] font-semibold">{title}</h2>
        {desc && <p className="mt-1 text-[13px] leading-relaxed text-muted">{desc}</p>}
      </div>
      <div className="max-w-2xl">{children}</div>
    </section>
  )
}

function useSave(onSaved: (s: Settings) => void) {
  return useMutation({ mutationFn: (p: SettingsPatch) => api.updateSettings(p), onSuccess: onSaved })
}

function SaveRow({ pending, disabled, error, extra }: { pending: boolean; disabled?: boolean; error?: Error | null; extra?: ReactNode }) {
  return (
    <div className="space-y-2 pt-1">
      {error && <p className="text-[13px] text-err">{error.message}</p>}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" type="submit" loading={pending} disabled={disabled}>
          保存
        </Button>
        {extra}
      </div>
    </div>
  )
}

function RuntimeForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [interval, setPollInterval] = useState(String(settings.poll_interval_seconds))
  const [folder, setFolder] = useState(settings.folder_name)
  const [minChars, setMinChars] = useState(String(settings.min_transcript_chars))
  const save = useSave(onSaved)

  const intervalN = Number(interval)
  const minN = Number(minChars)
  const valid = Number.isInteger(intervalN) && intervalN > 0 && Number.isInteger(minN) && minN >= 0 && folder.trim() !== ''

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (valid) save.mutate({ poll_interval_seconds: intervalN, folder_name: folder.trim(), min_transcript_chars: minN })
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="检查间隔" htmlFor="rt-interval" hint="单位：秒">
          <Input id="rt-interval" type="number" min={1} step={1} value={interval} onChange={(e) => setPollInterval(e.target.value)} />
        </Field>
        <Field label="逐字稿最短字数" htmlFor="rt-min" hint="少于该字数的妙记跳过">
          <Input id="rt-min" type="number" min={0} step={1} value={minChars} onChange={(e) => setMinChars(e.target.value)} />
        </Field>
      </div>
      <Field label="云空间文件夹" htmlFor="rt-folder" hint="纪要文档创建在每位成员云空间的该文件夹下">
        <Input id="rt-folder" value={folder} onChange={(e) => setFolder(e.target.value)} />
      </Field>
      <SaveRow pending={save.isPending} disabled={!valid} error={save.error} />
    </form>
  )
}

function GlossaryForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [text, setText] = useState(settings.glossary)
  const save = useSave(onSaved)
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate({ glossary: text })
  }
  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <Field label="术语" htmlFor="glossary" hint="每行一条，写正确写法与常见的误转写，例如：Kubernetes：酷伯内提斯、库伯内特斯">
        <Textarea id="glossary" rows={8} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      </Field>
      <SaveRow pending={save.isPending} disabled={text === settings.glossary} error={save.error} />
    </form>
  )
}

function PromptForm({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [text, setText] = useState(settings.prompt)
  const save = useMutation({
    mutationFn: (p: SettingsPatch) => api.updateSettings(p),
    onSuccess: (s) => {
      setText(s.prompt)
      onSaved(s)
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (text.trim()) save.mutate({ prompt: text })
  }
  const reset = () => {
    if (window.confirm('恢复默认提示词？当前内容将被覆盖。')) save.mutate({ prompt: '' })
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <label htmlFor="prompt" className="text-[13px] font-medium text-muted">
            提示词
          </label>
          <span className="text-xs text-faint">{settings.prompt_is_default ? '默认' : '已自定义'}</span>
        </div>
        <Textarea id="prompt" rows={18} value={text} onChange={(e) => setText(e.target.value)} className="text-[13px]" spellCheck={false} />
      </div>
      <SaveRow
        pending={save.isPending}
        disabled={!text.trim() || text === settings.prompt}
        error={save.error}
        extra={
          !settings.prompt_is_default && (
            <Button variant="ghost" onClick={reset} disabled={save.isPending}>
              恢复默认
            </Button>
          )
        }
      />
    </form>
  )
}
