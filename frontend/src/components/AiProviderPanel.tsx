import React, { useState } from 'react';
import { FiCpu, FiPlay, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { AIChat, AIContext, AINL2SQL, AIProviderInfo, AISQL, OpenAIProvider } from '../lib/workbench';

// W-A1: the PROVIDER + CONTEXT + SQL-PREVIEW surface.
//
// This is deliberately a DIFFERENT panel from AiPanel ("问数据库"), which is the
// in-app read-only chat. This one configures the model endpoint and decides which
// host-owned sources get attached — and its NL→SQL output is a PREVIEW by
// construction:
//
//   1. THE CONTEXT IS THE HOST'S. The checkboxes choose which sources the HOST
//      attaches; it is the only component that can read them all (schema from the
//      tab, terminal tail from the host's scrollback, notes, a remote file head).
//      The plugin receives a finished block and never reads a key.
//   2. NL→SQL IS A PREVIEW. The result is SQL TEXT plus an explanation; there is
//      no "run" button because the host has no execute path for it (the API refuses
//      an `execute` field). The operator copies it into the editor, where the
//      ordinary confirm path applies.
//
// A local gateway (ollama/llama.cpp) needs no API key; a hosted endpoint does, and
// the key travels by AD-4's lend.

const AiProviderPanel: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const [provider, setProvider] = useState<AIProviderInfo | null>(null);
  const [form, setForm] = useState({ baseUrl: 'http://127.0.0.1:11434/v1', model: 'qwen2.5-coder', apiKey: '' });
  const [sources, setSources] = useState({ schema: true, terminal: false, notes: false, file: false });
  const [schema, setSchema] = useState('');
  const [question, setQuestion] = useState('列出最近 7 天未支付的订单');
  const [answer, setAnswer] = useState('');
  const [sql, setSql] = useState<AISQL | null>(null);
  const [busy, setBusy] = useState(false);

  // Only the schema source is wired to a textarea here; terminal/notes/file are
  // attached server-side from what the host already owns (the panel says which).
  const context = (): AIContext => ({ schema: sources.schema ? schema : undefined });

  const open = async () => {
    setBusy(true);
    try {
      const p = await OpenAIProvider({
        baseUrl: form.baseUrl || undefined,
        model: form.model || undefined,
        apiKey: form.apiKey || undefined,
      });
      setProvider(p);
      toast.success(`已连接 ${p.model}`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const ask = async () => {
    if (!provider) return;
    setBusy(true);
    setSql(null);
    try {
      const res = await AIChat(provider.session, [{ role: 'user', content: question }], context());
      setAnswer(res.text ?? '');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const toSQL = async () => {
    if (!provider) return;
    setBusy(true);
    setAnswer('');
    try {
      const res = await AINL2SQL(provider.session, question, context(), 'postgres');
      setSql(res);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="AI 提供商"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-3xl max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiCpu size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">
            AI 提供商与 SQL 预览 {provider && <span className="text-2xs text-muted">（{provider.model}）</span>}
          </span>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="p-3 space-y-3 overflow-auto">
          <div className="grid grid-cols-3 gap-2 text-2xs">
            <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink col-span-2"
              placeholder="端点（OpenAI 兼容，默认本地 ollama）" value={form.baseUrl}
              onChange={e => setForm(f => ({ ...f, baseUrl: e.target.value }))} aria-label="端点" />
            <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="模型"
              value={form.model} onChange={e => setForm(f => ({ ...f, model: e.target.value }))} aria-label="模型" />
            <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink col-span-2" type="password"
              placeholder="API Key（本地网关可空；经秘密借出传输）" value={form.apiKey}
              onChange={e => setForm(f => ({ ...f, apiKey: e.target.value }))} aria-label="API Key" />
            <button className="py-1.5 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void open()} disabled={busy}>
              {provider ? '重连' : '连接'}
            </button>
          </div>

          <div className="text-2xs space-y-1">
            <div className="text-muted">宿主附加的上下文来源（只有宿主能读）：</div>
            <div className="flex flex-wrap gap-3">
              {([
                ['schema', '选中表结构'],
                ['terminal', '终端最近输出'],
                ['notes', '笔记'],
                ['file', '远端文件片段'],
              ] as const).map(([key, label]) => (
                <label key={key} className="flex items-center gap-1 text-secondary">
                  <input type="checkbox" checked={sources[key]}
                    onChange={e => setSources(s => ({ ...s, [key]: e.target.checked }))} />
                  {label}
                </label>
              ))}
            </div>
            {sources.schema && (
              <textarea className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-ink font-mono"
                rows={3} placeholder="粘贴/选择表结构（宿主视图里选中的表）" value={schema}
                onChange={e => setSchema(e.target.value)} aria-label="表结构" />
            )}
          </div>

          <div className="flex items-center gap-2 text-2xs">
            <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink"
              value={question} onChange={e => setQuestion(e.target.value)} aria-label="问题" />
            <button className="px-2 py-1.5 rounded border border-line text-secondary hover:text-ink disabled:opacity-40"
              onClick={() => void ask()} disabled={busy || !provider}>问答</button>
            <button className="flex items-center gap-1 px-2 py-1.5 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void toSQL()} disabled={busy || !provider}>
              <FiPlay size={10} /> 生成 SQL（预览）
            </button>
          </div>

          {answer && (
            <pre className="text-2xs text-ink font-mono whitespace-pre-wrap border border-line rounded p-2 max-h-48 overflow-auto">
              {answer}
            </pre>
          )}

          {sql && (
            <div className="border border-line rounded p-2 space-y-1 text-2xs">
              <div className="flex items-center gap-2">
                <span className="text-warning">预览（不会执行）</span>
                <span className="flex-1" />
                <button className="text-secondary hover:text-ink"
                  onClick={() => void navigator.clipboard?.writeText(sql.sql)}>复制 SQL</button>
              </div>
              <pre className="text-ink font-mono whitespace-pre-wrap">{sql.sql}</pre>
              {sql.explanation && <div className="text-muted">{sql.explanation}</div>}
              <div className="text-muted">{sql.source}</div>
            </div>
          )}

          <p className="text-2xs text-muted leading-relaxed">
            上下文由宿主附加：只有宿主能读全部键面（表结构/终端尾部/笔记/远端文件片段），插件只收到
            拼好的文本。生成 SQL 只返回预览——要执行请把 SQL 放进编辑器，走正常的确认路径。
          </p>
        </div>
      </div>
    </div>
  );
};

export default AiProviderPanel;
