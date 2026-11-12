import React, { useEffect, useRef, useState } from 'react';
import { FiKey, FiLock, FiShield } from 'react-icons/fi';

// W16: the server-mode login.
//
// Only reachable in the server deployment (the page is served by the API and no token
// is injected), so the desktop app never sees it. Two things it deliberately does NOT
// do:
//
//   - it does not say whether the password or the account was wrong — one message for
//     every failure, so an unauthenticated caller cannot probe the setup state;
//   - it does not persist anything locally: the credential is the HttpOnly session
//     cookie the server sets, and no token is stored in the page.

interface LoginScreenProps {
  onLoggedIn: () => void;
}

// postLogin is local to this file: the login route must not carry the shared
// authHeaders (there is no session yet) and must not throw AuthRequiredError in a loop.
const postLogin = async (body: Record<string, string>): Promise<any> => {
  const res = await fetch('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  return { ok: res.ok, status: res.status, data };
};

const LoginScreen: React.FC<LoginScreenProps> = ({ onLoggedIn }) => {
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [needsCode, setNeedsCode] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    passwordRef.current?.focus();
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      const res = await postLogin(needsCode ? { password, totp: code } : { password });
      if (res.ok && res.data?.ok) {
        onLoggedIn();
        return;
      }
      if (res.data?.needsTotp) {
        // The password was right. Saying so is not a leak: it tells this caller only
        // what their own password already proved, and it is the only way to ask for
        // the second factor without a separate "is TOTP on" endpoint.
        setNeedsCode(true);
        setError('');
        return;
      }
      setError(res.data?.error || '登录失败');
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-screen flex items-center justify-center bg-canvas text-ink">
      <form onSubmit={submit} className="w-full max-w-sm bg-panel border border-line rounded-lg p-6 space-y-4">
        <div className="flex items-center gap-2">
          <FiLock size={16} className="text-accent" />
          <h1 className="text-sm font-semibold">TablePro Go 服务器</h1>
        </div>
        <p className="text-2xs text-muted leading-relaxed">
          服务器模式默认只读，并需要登录。会话保存在内存里：进程重启后需要重新登录。
        </p>

        <label className="block space-y-1">
          <span className="text-2xs text-secondary">口令</span>
          <input
            ref={passwordRef}
            type="password"
            className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
            value={password}
            onChange={e => setPassword(e.target.value)}
            autoComplete="current-password"
            aria-label="服务器口令"
          />
        </label>

        {needsCode && (
          <label className="block space-y-1">
            <span className="text-2xs text-secondary flex items-center gap-1">
              <FiShield size={10} /> 两步验证码
            </span>
            <input
              type="text"
              inputMode="numeric"
              className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink font-mono tracking-widest focus:outline-none focus:border-accent"
              value={code}
              onChange={e => setCode(e.target.value)}
              placeholder="123456"
              autoComplete="one-time-code"
              aria-label="两步验证码"
            />
          </label>
        )}

        {error && (
          <p className="text-2xs text-danger" role="alert">
            {error}
          </p>
        )}

        <button
          type="submit"
          className="w-full py-1.5 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40 flex items-center justify-center gap-1"
          disabled={busy || password === '' || (needsCode && code === '')}
        >
          <FiKey size={12} />
          {busy ? '登录中…' : '登录'}
        </button>
      </form>
    </div>
  );
};

export default LoginScreen;
