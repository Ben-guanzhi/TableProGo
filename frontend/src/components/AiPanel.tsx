import React, { useEffect, useRef, useState } from 'react';
import { FiSend, FiTrash2, FiX } from 'react-icons/fi';
import {
  AcceptLlmProposal,
  AskLlm,
  ClearLlmHistory,
  LlmConfig,
  LlmMessage,
  LlmProposal,
  LlmProvider,
  LoadLlmProposals,
  RejectLlmProposal,
  LoadLlmConfig,
  LoadLlmHistory,
  SaveLlmConfig,
  defaultLlmConfig,
} from '../lib/llm';

// The AI assistant panel: a question, an answer, and the provider settings.
//
// The model is never given the database directly — it reaches the schema through
// the same read-only tools the MCP server offers, so what it sees is what the
// user sees.

interface AiPanelProps {
  onClose: () => void;
}

const AiPanel: React.FC<AiPanelProps> = ({ onClose }) => {
  const [messages, setMessages] = useState<LlmMessage[]>([]);
  const [question, setQuestion] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [showSettings, setShowSettings] = useState(false);
  const [config, setConfig] = useState<LlmConfig>(defaultLlmConfig);
  const [providers, setProviders] = useState<LlmProvider[]>([]);
  const [proposals, setProposals] = useState<LlmProposal[]>([]);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    LoadLlmHistory()
      .then(setMessages)
      .catch(() => {});
    LoadLlmConfig()
      .then(res => {
        setConfig(res.config);
        setProviders(res.providers);
      })
      .catch(() => {});
    refreshProposals();
  }, []);

  // A proposal appears while the assistant is answering, so the list is polled
  // rather than pushed: the backend has no channel to the webview.
  const refreshProposals = async () => {
    try {
      setProposals(await LoadLlmProposals());
    } catch {
      /* the panel stays as it is */
    }
  };

  // The answer is long; keep the newest in view.
  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  const accept = async (id: string) => {
    setError('');
    const result = await AcceptLlmProposal(id).catch(e => ({ error: (e as Error).message }));
    await refreshProposals();
    if (result.error) {
      setError(result.error);
      return;
    }
    setMessages(current => [
      ...current,
      { role: 'assistant', content: `已执行你确认的语句。` },
    ]);
  };

  const reject = async (id: string) => {
    await RejectLlmProposal(id).catch(() => {});
    await refreshProposals();
  };

  const ask = async () => {
    const text = question.trim();
    if (!text || busy) return;
    setQuestion('');
    setError('');
    setMessages(current => [...current, { role: 'user', content: text }]);
    setBusy(true);
    try {
      const answer = await AskLlm(text);
      setMessages(current => [...current, { role: 'assistant', content: answer }]);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      // The model may have proposed a statement while it was answering.
      await refreshProposals();
    }
  };

  const clear = async () => {
    await ClearLlmHistory().catch(() => {});
    setMessages([]);
    setError('');
  };

  const save = async () => {
    setError('');
    try {
      await SaveLlmConfig(config);
      setShowSettings(false);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const pickProvider = (id: string) => {
    const provider = providers.find(p => p.ID === id);
    setConfig(current => ({
      ...current,
      provider: id,
      baseURL: provider?.DefaultBaseURL ?? current.baseURL,
      model: provider?.DefaultModel ?? current.model,
    }));
  };

  return (
    <div className="w-80 bg-panel border-l border-line flex flex-col min-h-0" data-testid="ai-panel">
      <div className="p-2.5 border-b border-line flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-secondary">AI 助手</span>
        <div className="flex items-center gap-1">
          <button
            className="text-secondary hover:text-ink p-1"
            onClick={() => setShowSettings(current => !current)}
            title="AI 助手设置"
            aria-label="AI 助手设置"
          >
            ⚙
          </button>
          <button
            className="text-secondary hover:text-ink p-1"
            onClick={clear}
            title="清空对话"
            aria-label="清空对话"
          >
            <FiTrash2 size={13} />
          </button>
          <button
            className="text-secondary hover:text-ink p-1"
            onClick={onClose}
            title="关闭"
            aria-label="关闭 AI 助手"
          >
            <FiX size={13} />
          </button>
        </div>
      </div>

      {showSettings && (
        <div className="p-2.5 border-b border-line space-y-2">
          <label className="block">
            <span className="text-2xs text-muted">服务商</span>
            <select
              className="w-full mt-0.5 bg-canvas border border-line rounded px-1.5 py-1 text-xs text-ink"
              value={config.provider}
              onChange={e => pickProvider(e.target.value)}
              aria-label="AI 服务商"
            >
              {providers.map(p => (
                <option key={p.ID} value={p.ID}>
                  {p.Label}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="text-2xs text-muted">Base URL</span>
            <input
              className="w-full mt-0.5 bg-canvas border border-line rounded px-1.5 py-1 text-xs text-ink"
              value={config.baseURL}
              onChange={e => setConfig(current => ({ ...current, baseURL: e.target.value }))}
              aria-label="Base URL"
            />
          </label>
          <label className="block">
            <span className="text-2xs text-muted">模型</span>
            <input
              className="w-full mt-0.5 bg-canvas border border-line rounded px-1.5 py-1 text-xs text-ink"
              value={config.model}
              onChange={e => setConfig(current => ({ ...current, model: e.target.value }))}
              aria-label="模型"
            />
          </label>
          <label className="block">
            <span className="text-2xs text-muted">API Key</span>
            <input
              type="password"
              className="w-full mt-0.5 bg-canvas border border-line rounded px-1.5 py-1 text-xs text-ink"
              value={config.apiKey}
              onChange={e => setConfig(current => ({ ...current, apiKey: e.target.value }))}
              aria-label="API Key"
            />
          </label>
          <label className="flex items-center gap-1.5 text-2xs text-secondary">
            <input
              type="checkbox"
              checked={config.enabled}
              onChange={e => setConfig(current => ({ ...current, enabled: e.target.checked }))}
              aria-label="启用 AI 助手"
            />
            启用
          </label>
          <label className="flex items-center gap-1.5 text-2xs text-secondary">
            <input
              type="checkbox"
              checked={config.llmAllowProposals}
              onChange={e => setConfig(current => ({ ...current, llmAllowProposals: e.target.checked }))}
              aria-label="允许助手提议语句"
            />
            允许助手提议语句（仍需你确认才执行）
          </label>
          <button
            className="w-full px-2 py-1 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover"
            onClick={save}
          >
            保存设置
          </button>
        </div>
      )}

      <div ref={logRef} className="flex-1 overflow-y-auto p-2.5 space-y-2">
        {messages.length === 0 && (
          <div className="text-2xs text-muted leading-relaxed">
            用自然语言问数据库。助手只能读：它看得到目录、能跑 SELECT，写操作请在编辑器里执行。
          </div>
        )}
        {messages.map((m, i) => (
          <div
            key={i}
            className={`text-2xs leading-relaxed whitespace-pre-wrap ${
              m.role === 'user' ? 'text-ink' : 'text-secondary'
            }`}
          >
            {m.role === 'user' ? `你：${m.content}` : m.content}
          </div>
        ))}
        {busy && <div className="text-2xs text-muted">思考中…</div>}
        {error && <div className="text-2xs text-danger break-words">{error}</div>}
      </div>

      {proposals.length > 0 && (
        <div className="border-t border-line p-2 space-y-1.5 max-h-48 overflow-y-auto" data-testid="ai-proposals">
          <div className="text-2xs text-warning">助手想执行下面的语句，由你决定：</div>
          {proposals.map(p => (
            <div key={p.id} className="border border-line rounded p-1.5 bg-canvas">
              {p.reason && <div className="text-2xs text-muted mb-1">{p.reason}</div>}
              <pre className="text-2xs text-ink whitespace-pre-wrap break-words mb-1.5">{p.sql}</pre>
              <div className="flex items-center gap-1">
                <button
                  className="px-2 py-0.5 rounded text-2xs bg-success text-on-success hover:bg-success-hover"
                  onClick={() => accept(p.id)}
                >
                  执行
                </button>
                <button
                  className="px-2 py-0.5 rounded text-2xs bg-raised text-secondary hover:text-ink"
                  onClick={() => reject(p.id)}
                >
                  放弃
                </button>
                <span className="text-2xs text-muted ml-auto">连接 {p.connection}</span>
              </div>
            </div>
          ))}
        </div>
      )}

      <div className="p-2 border-t border-line">
        <div className="flex items-end gap-1">
          <textarea
            className="flex-1 bg-canvas border border-line rounded px-1.5 py-1 text-xs text-ink focus:outline-none focus:border-accent resize-none"
            rows={2}
            value={question}
            placeholder="问点什么…（Enter 发送，Shift+Enter 换行）"
            aria-label="向 AI 助手提问"
            onChange={e => setQuestion(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                ask();
              }
            }}
          />
          <button
            className="flex items-center justify-center w-7 h-7 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40 shrink-0"
            onClick={ask}
            disabled={busy || !question.trim()}
            title="发送"
            aria-label="发送问题"
          >
            <FiSend size={12} />
          </button>
        </div>
      </div>
    </div>
  );
};

export default AiPanel;
