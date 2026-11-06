import { useMutation } from '@tanstack/react-query'
import { CircleAlert, CircleCheck, Plug } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import type { LlmTestResult, Settings } from '../lib/api'
import { api } from '../lib/api'
import { Button, Field, Input } from './ui'

// 模型接口表单：设置页与初始化向导第 3 步共用
export function LlmForm({
  initial,
  submitText = '保存',
  onSaved,
}: {
  initial?: Settings['llm']
  submitText?: string
  onSaved: (s: Settings) => void
}) {
  const [baseUrl, setBaseUrl] = useState(initial?.base_url ?? '')
  const [model, setModel] = useState(initial?.model ?? '')
  const [apiKey, setApiKey] = useState('')
  const [result, setResult] = useState<LlmTestResult | null>(null)
  const keySet = initial?.api_key_set ?? false
  // 地址改了之后已保存的密钥不再沿用
  const urlChanged = keySet && baseUrl.trim().replace(/\/+$/, '') !== (initial?.base_url ?? '')

  const test = useMutation({
    mutationFn: () => api.testLlm({ base_url: baseUrl.trim(), model: model.trim(), ...(apiKey ? { api_key: apiKey } : {}) }),
    onMutate: () => setResult(null),
    onSuccess: setResult,
    onError: (e) => setResult({ ok: false, error: e.message }),
  })

  const save = useMutation({
    mutationFn: () => api.updateSettings({ llm: { base_url: baseUrl.trim(), model: model.trim(), ...(apiKey ? { api_key: apiKey } : {}) } }),
    onSuccess: (s) => {
      setApiKey('')
      onSaved(s)
    },
  })

  // 密钥可以留空，本地部署的接口通常不需要
  const canSubmit = baseUrl.trim() !== '' && model.trim() !== ''

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (canSubmit) save.mutate()
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <Field label="接口地址" htmlFor="llm-url" hint="OpenAI 格式接口，填到版本路径，例如 https://api.openai.com/v1">
        <Input id="llm-url" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://api.example.com/v1" autoComplete="off" spellCheck={false} />
      </Field>
      <Field label="模型" htmlFor="llm-model">
        <Input id="llm-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder="模型名称" autoComplete="off" spellCheck={false} />
      </Field>
      <Field label="API Key" htmlFor="llm-key" hint={urlChanged ? '接口地址已修改，原密钥不再沿用，需要时重新填写' : keySet ? '已保存，留空则不修改' : '接口不需要密钥时留空'}>
        <Input
          id="llm-key"
          type="password"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={keySet && !urlChanged ? '••••••••••••' : 'sk-…'}
          autoComplete="new-password"
          spellCheck={false}
        />
      </Field>

      {result && (
        <div className="anim-fade rounded-lg border border-line bg-surface px-3.5 py-2.5 text-[13px]">
          {result.ok ? (
            <div className="space-y-1">
              <div className="flex items-center gap-1.5 text-ok">
                <CircleCheck className="size-4" />
                连接正常，耗时 {result.latency_ms} ms
              </div>
              {result.reply && <p className="line-clamp-3 text-muted">{result.reply}</p>}
            </div>
          ) : (
            <div className="flex items-start gap-1.5 text-err">
              <CircleAlert className="mt-0.5 size-4 shrink-0" />
              <span className="break-all">{result.error}</span>
            </div>
          )}
        </div>
      )}
      {save.isError && <p className="text-[13px] text-err">{save.error.message}</p>}

      <div className="flex flex-wrap gap-2 pt-1">
        <Button variant="primary" type="submit" loading={save.isPending} disabled={!canSubmit}>
          {submitText}
        </Button>
        <Button onClick={() => test.mutate()} loading={test.isPending} disabled={!canSubmit}>
          <Plug className="size-4" />
          测试连接
        </Button>
      </div>
    </form>
  )
}
