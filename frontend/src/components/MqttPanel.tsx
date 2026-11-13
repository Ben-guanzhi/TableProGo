import React, { useCallback, useEffect, useState } from 'react';
import { FiAlertTriangle, FiMessageSquare, FiSend, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  CloseMqttSession,
  ListMqttSubscriptions,
  MqttMessage,
  MqttSubscription,
  MqttMessages,
  OpenMqttSession,
  PublishTopic,
  SubscribeTopic,
  UnsubscribeTopic,
} from '../lib/workbench';

// W-Q1: the MQTT console. Two verbs with OPPOSITE discipline, which is the whole
// point of the panel:
//
//   subscribe — an ACQUISITION. It lands as a reversible row on the host's
//               mqtt.subscriptions key (the panel lists those rows), and
//               unsubscribing — or unloading the plugin — removes it.
//   publish   — an EMISSION. gated: under Safe Mode the host answers
//               {withheld:true} and sends NOTHING to the broker; the confirm bar
//               re-posts through the separate confirmed call. The panel never
//               offers an undo after a publish, because there is none.
//
// Messages arrive through a bounded ring in the plugin; the panel polls with a
// cursor and shows the ring's total so a gap (while the panel was closed) is
// visible instead of silently missing.

const POLL_MS = 1200;

const MqttPanel: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const [session, setSession] = useState('');
  const [form, setForm] = useState({ host: '127.0.0.1', port: 1883, user: '', password: '' });
  const [topic, setTopic] = useState('ops/#');
  const [subs, setSubs] = useState<MqttSubscription[]>([]);
  const [messages, setMessages] = useState<MqttMessage[]>([]);
  const [cursor, setCursor] = useState(0);
  const [total, setTotal] = useState(0);
  const [pubTopic, setPubTopic] = useState('ops/alerts');
  const [pubPayload, setPubPayload] = useState('');
  const [withheld, setWithheld] = useState<{ topic: string; payload: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const poll = useCallback(async () => {
    if (!session) return;
    try {
      const res = await MqttMessages(session, cursor);
      if ((res.messages?.length ?? 0) > 0) {
        setMessages(prev => [...prev, ...(res.messages ?? [])].slice(-500));
      }
      setCursor(res.next ?? cursor);
      setTotal(res.total ?? total);
    } catch {
      // A closed session is reported by the action itself; polling stays quiet.
    }
  }, [session, cursor, total]);

  useEffect(() => {
    if (!session) return;
    const timer = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(timer);
  }, [session, poll]);

  const open = async () => {
    setBusy(true);
    try {
      const res = await OpenMqttSession({
        host: form.host,
        port: form.port,
        user: form.user || undefined,
        password: form.password || undefined,
      });
      setSession(res.sessionId);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const close = async () => {
    if (session) {
      try {
        await CloseMqttSession(session);
      } catch (e) {
        toast.error((e as Error).message);
      }
    }
    setSession('');
    setSubs([]);
    setMessages([]);
    setCursor(0);
  };

  const subscribe = async () => {
    if (!session || topic.trim() === '') return;
    try {
      const res = await SubscribeTopic(session, topic.trim());
      setSubs(res.subscriptions ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const unsubscribe = async (t: string) => {
    try {
      const res = await UnsubscribeTopic(session, t);
      setSubs(res.subscriptions ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const refreshSubs = async () => {
    try {
      const res = await ListMqttSubscriptions(session);
      setSubs(res.subscriptions ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const publish = async (confirmed: boolean) => {
    if (!session || pubTopic.trim() === '') return;
    setBusy(true);
    try {
      const res = await PublishTopic(session, pubTopic.trim(), pubPayload, confirmed);
      if (res.withheld) {
        setWithheld({ topic: res.topic ?? pubTopic, payload: res.payload ?? pubPayload });
        return;
      }
      setWithheld(null);
      toast.success(`已发布 ${res.bytes ?? 0} 字节到 ${res.topic}（不可撤回）`);
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
        aria-label="消息工作台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-4xl h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiMessageSquare size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">
            消息（MQTT）{session && <span className="text-2xs text-muted">（会话 {session}）</span>}
          </span>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        {!session ? (
          <div className="p-6 space-y-3">
            <div className="grid grid-cols-2 gap-2 text-2xs">
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="代理主机"
                value={form.host} onChange={e => setForm(f => ({ ...f, host: e.target.value }))} aria-label="代理主机" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="端口" type="number"
                value={form.port} onChange={e => setForm(f => ({ ...f, port: Number(e.target.value) }))} aria-label="端口" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="用户（可空）"
                value={form.user} onChange={e => setForm(f => ({ ...f, user: e.target.value }))} aria-label="用户" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="密码（可空）" type="password"
                value={form.password} onChange={e => setForm(f => ({ ...f, password: e.target.value }))} aria-label="密码" />
            </div>
            <button
              className="w-full py-1.5 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void open()}
              disabled={busy || form.host === ''}
            >
              {busy ? '连接中…' : '连接代理'}
            </button>
            <p className="text-2xs text-muted leading-relaxed">
              订阅是获取：它会作为可逆的一行登记在 mqtt.subscriptions，取消订阅或卸载插件即撤回。
              发布是发射：Safe Mode 打开时会被扣留（不向代理发送任何字节），确认后不可撤回。
            </p>
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line text-2xs">
              <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-ink font-mono"
                value={topic} onChange={e => setTopic(e.target.value)} aria-label="订阅主题" />
              <button className="px-2 py-1 rounded border border-line text-secondary hover:text-ink"
                onClick={() => void subscribe()}>订阅</button>
              <button className="px-2 py-1 rounded border border-line text-secondary hover:text-ink"
                onClick={() => void refreshSubs()}>刷新</button>
              <button className="text-secondary hover:text-danger" onClick={() => void close()} aria-label="断开">
                <FiX size={12} />
              </button>
            </div>

            <div className="px-3 py-1.5 border-b border-line text-2xs flex flex-wrap gap-1.5 items-center">
              <span className="text-muted">订阅行：</span>
              {subs.length === 0 && <span className="text-muted">（无）</span>}
              {subs.map(s => (
                <span key={s.tag} className="flex items-center gap-1 px-1.5 py-0.5 rounded bg-raised text-ink font-mono">
                  {s.topic ?? s.tag}
                  <button className="text-secondary hover:text-danger" onClick={() => void unsubscribe(s.topic ?? '')}
                    aria-label={`取消订阅 ${s.topic ?? s.tag}`}>
                    ×
                  </button>
                </span>
              ))}
            </div>

            <div className="flex-1 min-h-0 overflow-auto px-3 py-1">
              {messages.length === 0 ? (
                <div className="text-2xs text-muted py-2">尚未收到消息（总计 {total}）。</div>
              ) : (
                <table className="w-full text-2xs font-mono">
                  <tbody>
                    {messages.map((m, i) => (
                      <tr key={i} className="border-t border-line">
                        <td className="px-1 py-0.5 text-muted whitespace-nowrap">{m.at?.slice(11, 19)}</td>
                        <td className="px-1 py-0.5 text-accent whitespace-nowrap">{m.topic}</td>
                        <td className="px-1 py-0.5 text-ink break-all">{m.payload}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>

            <div className="border-t border-line px-3 py-2 space-y-1.5">
              {withheld && (
                <div className="flex items-center gap-2 text-2xs text-warning" role="alert">
                  <FiAlertTriangle size={12} />
                  <span>
                    Safe Mode 扣留：将「{withheld.payload}」发布到 {withheld.topic}？确认后不可撤回。
                  </span>
                  <button className="px-2 py-0.5 rounded bg-warning/20 text-warning hover:bg-warning/30"
                    onClick={() => void publish(true)} disabled={busy}>
                    确认发布
                  </button>
                  <button className="text-secondary hover:text-ink" onClick={() => setWithheld(null)}>取消</button>
                </div>
              )}
              <div className="flex items-center gap-2 text-2xs">
                <input className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink font-mono w-40"
                  value={pubTopic} onChange={e => setPubTopic(e.target.value)} aria-label="发布主题" />
                <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-ink"
                  value={pubPayload} placeholder="载荷" onChange={e => setPubPayload(e.target.value)} aria-label="发布载荷" />
                <button
                  className="flex items-center gap-1 px-2 py-1 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
                  onClick={() => void publish(false)}
                  disabled={busy || pubTopic.trim() === ''}
                >
                  <FiSend size={10} /> 发布
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default MqttPanel;
