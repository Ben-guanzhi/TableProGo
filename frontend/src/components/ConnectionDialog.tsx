import { useState, useEffect, useRef } from 'react';
import { ConnectionConfig, EngineType, CONN_COLORS } from '../types';
import type { SSHConfig } from '../types';
import { FiX, FiDatabase, FiCheck } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { useDialog } from '../dialog';
import { ParseConnectionURI, ResolveSSHConfig } from '../api';

import { Connection } from '../types';

// A disabled SSH block that still carries defaults, so toggling the checkbox
// never leaves port 0 or a half-filled object behind.
const emptySSH: SSHConfig = {
  enabled: false,
  host: '',
  port: 22,
  user: '',
  password: '',
  keyFile: '',
  passphrase: '',
};

interface ConnectionDialogProps {
  initialConfig?: Connection | null;
  onAdd: (config: ConnectionConfig) => void | Promise<void>;
  onUpdate?: (config: ConnectionConfig) => void | Promise<void>;
  onClose: () => void;
  onTest: (config: ConnectionConfig) => Promise<boolean>;
}

const ConnectionDialog: React.FC<ConnectionDialogProps> = ({ initialConfig, onAdd, onUpdate, onClose, onTest }) => {
  const isEdit = !!initialConfig;
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [config, setConfig] = useState<ConnectionConfig>({
    name: '',
    type: 'mysql',
    host: 'localhost',
    port: 3306,
    username: 'root',
    password: '',
    database: '',
    sslMode: 'false',
    options: {},
    ssh: { ...emptySSH },
    isolationLevel: '',
    color: '',
  });

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<'success' | 'error' | null>(null);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');
  // W4-d: the paste-a-URL path. uriMasked echoes the parsed URL back with the
  // password replaced, so the user can confirm the parse without the secret
  // being shown a second time.
  const [uriText, setUriText] = useState('');
  const [uriMasked, setUriMasked] = useState('');
  const [uriError, setUriError] = useState('');
  // W4-a: reference an alias from ~/.ssh/config instead of retyping it.
  const [sshAlias, setSshAlias] = useState('');
  const [sshConfigNote, setSshConfigNote] = useState('');
  const [sshConfigBad, setSshConfigBad] = useState(false);

  const fillFromSSHConfig = async () => {
    setSshConfigNote('');
    setSshConfigBad(false);
    try {
      const res = await ResolveSSHConfig(sshAlias.trim());
      if (!res.found) {
        setSshConfigBad(true);
        setSshConfigNote(`~/.ssh/config 里没有 ${sshAlias.trim()} 的 Host 块`);
        return;
      }
      // Only fields the config actually answered are overwritten; a missing
      // User or Port keeps whatever the user typed rather than becoming empty.
      setConfig(prev => ({
        ...prev,
        ssh: {
          ...(prev.ssh ?? emptySSH),
          enabled: true,
          host: res.hostName || prev.ssh?.host || '',
          port: res.port || prev.ssh?.port || 22,
          user: res.user || prev.ssh?.user || '',
          keyFile: (res.keyFiles && res.keyFiles.length > 0 ? res.keyFiles[0] : prev.ssh?.keyFile) || '',
          configAlias: sshAlias.trim(),
        },
      }));
      const parts: string[] = [`已填入 ${res.hostName}:${res.port}`];
      if (res.keyFiles && res.keyFiles.length > 0) parts.push(`${res.keyFiles.length} 个可用私钥`);
      const rejected = res.rejected ?? [];
      if (rejected.length > 0) {
        setSshConfigBad(true);
        parts.push(`已忽略: ${rejected.map(r => `${r.Directive}（${r.Reason}）`).join('; ')}`);
      }
      setSshConfigNote(parts.join(' · '));
    } catch (error) {
      setSshConfigBad(true);
      setSshConfigNote((error as Error).message);
    }
  };

  const applyURI = async () => {
    setUriError('');
    setUriMasked('');
    try {
      const parsed = await ParseConnectionURI(uriText);
      const next = parsed.config;
      setConfig(prev => ({
        ...prev,
        ...next,
        // A parsed URL never carries the tunnel/proxy settings; keep whatever
        // the user already configured for those.
        ssh: prev.ssh,
        options: next.options ?? prev.options,
        name: next.name || prev.name,
        type: (next.type as EngineType) ?? prev.type,
        host: next.host ?? prev.host,
        port: next.port ?? prev.port,
        username: next.username ?? prev.username,
        password: next.password ?? prev.password,
        database: next.database ?? prev.database,
      }));
      setUriMasked(parsed.masked);
    } catch (error) {
      setUriError((error as Error).message);
    }
  };

  useEffect(() => {
    if (initialConfig) {
      setConfig({
        id: initialConfig.id,
        name: initialConfig.name,
        type: initialConfig.type,
        host: initialConfig.host,
        port: initialConfig.port,
        username: initialConfig.username,
        password: initialConfig.password ?? '',
        database: initialConfig.database,
        sslMode: initialConfig.sslMode || 'false',
        group: initialConfig.group,
        options: initialConfig.options || {},
        ssh: initialConfig.ssh ? { ...emptySSH, ...initialConfig.ssh } : { ...emptySSH },
        databaseFilter: initialConfig.databaseFilter,
        isolationLevel: initialConfig.isolationLevel ?? '',
    color: initialConfig.color ?? '',
      });
    }
  }, [initialConfig]);

  const handleTypeChange = (type: string) => {
    const defaultPorts: Record<string, number> = {
      mysql: 3306,
      postgresql: 5432,
      sqlite: 0,
      sqlserver: 1433,
      oracle: 1521,
      redis: 6379,
      // 达梦 DM8 的默认端口。
      dm: 5236,
      // 协议兼容引擎的常规端口 (OceanBase 2881, StarRocks/Doris FE 9030)。
      oceanbase: 2881,
      greatsql: 3306,
      starrocks: 9030,
      doris: 9030,
      opengauss: 5432,
      gaussdb: 5432,
      kingbase: 5432,
    };

    const defaults: Record<string, { username: string; database: string }> = {
      mysql: { username: 'root', database: '' },
      postgresql: { username: 'postgres', database: '' },
      sqlite: { username: '', database: '' },
      sqlserver: { username: 'sa', database: 'master' },
      oracle: { username: 'system', database: 'ORCL' },
      redis: { username: '', database: '0' },
      // 达梦默认管理员 SYSDBA，且「库」在达梦里是 schema。
      dm: { username: 'SYSDBA', database: 'SYSDBA' },
      oceanbase: { username: 'root', database: '' },
      greatsql: { username: 'root', database: '' },
      starrocks: { username: 'root', database: '' },
      doris: { username: 'root', database: '' },
      opengauss: { username: 'gaussdb', database: 'postgres' },
      gaussdb: { username: 'gaussdb', database: 'postgres' },
      kingbase: { username: 'system', database: 'test' },
    };

    const d = defaults[type] || { username: 'root', database: '' };

    setConfig({
      ...config,
      type: type as EngineType,
      port: defaultPorts[type] || 3306,
      username: d.username,
      database: d.database,
    });
  };

  const handleTest = async () => {
    setTesting(true);
    setTestResult(null);

    try {
      const success = await onTest(config);
      if (success) {
        setTestResult('success');
        toast.success('连接成功');
      } else {
        setTestResult('error');
        toast.error('连接失败：未知错误');
      }
    } catch (error: unknown) {
      setTestResult('error');
      const msg = error instanceof Error ? error.message : String(error);
      toast.error(`连接失败：${msg}`, { duration: 8000 });
    } finally {
      setTesting(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError('');
    // Only the name is checked by the browser's `required`; a connection with
    // no host or a zero port would only fail later, at connect time.
    if (config.type !== 'sqlite') {
      if (!config.host.trim()) {
        setFormError('主机不能为空');
        return;
      }
      if (!config.port || config.port <= 0) {
        setFormError('端口必须是正整数');
        return;
      }
    }
    if (config.ssh?.enabled) {
      if (!config.ssh.host.trim() || !config.ssh.user.trim()) {
        setFormError('启用 SSH 隧道时，SSH 主机和用户名必填');
        return;
      }
    }
    setSaving(true);
    try {
      if (isEdit && onUpdate) {
        await onUpdate(config);
      } else {
        await onAdd(config);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="connection-dialog-title" className="bg-panel rounded-lg w-full max-w-md shadow-xl">
        <div className="flex items-center justify-between px-6 py-4 border-b border-line">
          <h2 id="connection-dialog-title" className="text-lg font-semibold">{isEdit ? '编辑连接' : '新建连接'}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭对话框">
            <FiX size={20} />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          {/* W4-d: paste a URL instead of filling the form. The backend owns
              the scheme table, so an unsupported scheme is refused with its
              name rather than connecting somewhere unintended. */}
          <div>
            <label htmlFor="conn-uri" className="block text-sm text-secondary mb-1">
              粘贴连接串 <span className="text-2xs text-muted">（可选，postgres:// mysql:// sqlserver:// …）</span>
            </label>
            <div className="flex gap-2">
              <input
                id="conn-uri"
                type="text"
                className="flex-1 px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none font-mono text-xs"
                value={uriText}
                onChange={e => setUriText(e.target.value)}
                onKeyDown={e => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    void applyURI();
                  }
                }}
                placeholder="postgres://user:pw@host:5432/db"
              />
              <button
                type="button"
                className="px-3 py-2 rounded text-xs bg-raised hover:bg-emphasis text-ink border border-line"
                onClick={() => void applyURI()}
                disabled={!uriText.trim()}
              >
                解析
              </button>
            </div>
            {uriError && <p className="mt-1 text-xs text-danger break-words">{uriError}</p>}
            {uriMasked && !uriError && (
              <p className="mt-1 text-2xs text-muted break-all">已填入（口令已隐藏）：{uriMasked}</p>
            )}
          </div>

          <div>
            <label htmlFor="conn-name" className="block text-sm text-secondary mb-1">连接名称</label>
            <input
              id="conn-name"
              autoFocus
              type="text"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={config.name}
              onChange={(e) => setConfig({ ...config, name: e.target.value })}
              placeholder="我的数据库"
              required
            />
          </div>

          <div>
            <label htmlFor="conn-type" className="block text-sm text-secondary mb-1">数据库类型</label>
            <select
              id="conn-type"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={config.type}
              onChange={(e) => handleTypeChange(e.target.value)}
            >
              <option value="mysql">MySQL</option>
              <option value="postgresql">PostgreSQL</option>
              <option value="sqlite">SQLite</option>
              <option value="sqlserver">SQL Server</option>
              <option value="oracle">Oracle</option>
              <option value="dm">达梦 DM8</option>
              <option value="redis">Redis</option>
              {/* 协议兼容引擎: 与底层引擎同一驱动栈, 仅端口与目录细节不同 */}
              <option value="oceanbase">OceanBase (MySQL 模式)</option>
              <option value="greatsql">GreatSQL</option>
              <option value="starrocks">StarRocks</option>
              <option value="doris">Apache Doris (FE)</option>
              <option value="opengauss">openGauss</option>
              <option value="gaussdb">华为 GaussDB</option>
              <option value="kingbase">人大金仓 KingbaseES</option>
            </select>
          </div>

          {config.type !== 'sqlite' && (
            <div className="grid grid-cols-3 gap-4">
              <div className="col-span-2">
                <label htmlFor="conn-host" className="block text-sm text-secondary mb-1">主机</label>
                <input
                  id="conn-host"
                  type="text"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={config.host}
                  onChange={(e) => setConfig({ ...config, host: e.target.value })}
                  placeholder="localhost"
                />
              </div>
              <div>
                <label htmlFor="conn-port" className="block text-sm text-secondary mb-1">端口</label>
                <input
                  id="conn-port"
                  type="number"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={config.port}
                  onChange={(e) => setConfig({ ...config, port: parseInt(e.target.value) || 0 })}
                />
              </div>
            </div>
          )}

          <div>
            <label htmlFor="conn-database" className="block text-sm text-secondary mb-1">
              {config.type === 'sqlite' ? '数据库文件路径' :
               config.type === 'oracle' ? '服务名 / SID' :
               config.type === 'redis' ? '逻辑库（0-15）' : '数据库'}
            </label>
            <input
              id="conn-database"
              type="text"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={config.database}
              onChange={(e) => setConfig({ ...config, database: e.target.value })}
              placeholder={
                config.type === 'sqlite' ? '/path/to/database.db' :
                config.type === 'oracle' ? 'ORCL' :
                config.type === 'redis' ? '0' :
                '数据库名'
              }
            />
          </div>

          <div>
            <label htmlFor="conn-group" className="block text-sm text-secondary mb-1">分组</label>
            <input
              id="conn-group"
              type="text"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={config.group ?? ''}
              onChange={(e) => setConfig({ ...config, group: e.target.value })}
              placeholder="留空为未分组（侧栏顶部）"
            />
          </div>

          {config.type !== 'sqlite' && (
            <>
              <div>
                <label htmlFor="conn-username" className="block text-sm text-secondary mb-1">用户名</label>
                <input
                  id="conn-username"
                  type="text"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={config.username}
                  onChange={(e) => setConfig({ ...config, username: e.target.value })}
                  placeholder={config.type === 'oracle' ? 'system' : 'root'}
                />
              </div>
              <div>
                <label htmlFor="conn-password" className="block text-sm text-secondary mb-1">
                  {isEdit ? '密码（留空保持不变）' : '密码'}
                </label>
                <input
                  id="conn-password"
                  type="password"
                  autoComplete="off"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={config.password}
                  onChange={(e) => setConfig({ ...config, password: e.target.value })}
                  placeholder={isEdit ? '••••••••（留空保持不变）' : '••••••••'}
                />
              </div>
            </>
          )}

          {config.type !== 'sqlite' && (
            <div>
              <label htmlFor="conn-ssl" className="block text-sm text-secondary mb-1">
                {config.type === 'sqlserver' ? '加密' : config.type === 'redis' ? 'TLS' : 'SSL 模式'}
              </label>
              <select
                id="conn-ssl"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={config.sslMode}
                onChange={(e) => setConfig({ ...config, sslMode: e.target.value })}
              >
                {config.type === 'sqlserver' ? (
                  <>
                    <option value="false">关（默认）</option>
                    <option value="true">开</option>
                    <option value="disable">禁用</option>
                    <option value="strict">严格</option>
                  </>
                ) : (
                  <>
                    <option value="false">禁用</option>
                    <option value="true">启用</option>
                    <option value="require">要求</option>
                  </>
                )}
              </select>
            </div>
          )}

          {config.type !== 'sqlite' && config.type !== 'redis' && (
            <div>
              <label htmlFor="conn-isolation" className="block text-sm text-secondary mb-1">
                事务隔离级别
              </label>
              <select
                id="conn-isolation"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={config.isolationLevel ?? ''}
                onChange={(e) => setConfig({ ...config, isolationLevel: e.target.value })}
              >
                <option value="">引擎默认</option>
                <option value="read_uncommitted">READ UNCOMMITTED</option>
                <option value="read_committed">READ COMMITTED</option>
                <option value="repeatable_read">REPEATABLE READ</option>
                <option value="serializable">SERIALIZABLE</option>
              </select>
              <p className="mt-1 text-xs text-secondary">
                作用于本连接开启的每个事务；留空跟随引擎默认（MySQL REPEATABLE READ、其余 READ COMMITTED）。
              </p>
            </div>
          )}

          <div>
            <label htmlFor="conn-color" className="block text-sm text-secondary mb-1">
              连接颜色
            </label>
            <div id="conn-color" role="radiogroup" aria-label="连接颜色" className="flex items-center gap-2 flex-wrap">
              {CONN_COLORS.map(c => (
                <button
                  key={c.key || 'none'}
                  type="button"
                  role="radio"
                  aria-checked={(config.color ?? '') === c.key}
                  title={c.label}
                  onClick={() => setConfig({ ...config, color: c.key })}
                  className={`w-6 h-6 rounded-full border-2 flex items-center justify-center ${
                    (config.color ?? '') === c.key ? 'border-ink' : 'border-transparent'
                  }`}
                  style={{ background: c.hex || 'transparent' }}
                >
                  {!c.hex && <span className="text-2xs text-secondary">无</span>}
                </button>
              ))}
            </div>
            <p className="mt-1 text-xs text-secondary">
              侧栏里该连接名旁显示色点（红=生产、橙=预发……）——写语句前先看见「连的是哪台」。
            </p>
          </div>

          {config.type !== 'sqlite' && (
            <div className="border-t border-line pt-4 space-y-3">
              <label className="flex items-center gap-2 text-sm text-secondary">
                <input
                  type="checkbox"
                  checked={config.ssh?.enabled ?? false}
                  onChange={(e) =>
                    setConfig({
                      ...config,
                      ssh: { ...(config.ssh ?? emptySSH), enabled: e.target.checked },
                    })
                  }
                />
                经 SSH 隧道连接（跳板机）
              </label>

              {config.ssh?.enabled && (
                <>
                  {/* W4-a: fill from the user's own ssh config. The backend
                      parses ~/.ssh/config and refuses transport directives
                      (ProxyJump & co) with a reason, which is surfaced here
                      rather than swallowed. */}
                  <div className="flex items-center gap-2">
                    <label htmlFor="ssh-alias" className="sr-only">~/.ssh/config 别名</label>
                    <input
                      id="ssh-alias"
                      type="text"
                      className="flex-1 px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none text-xs"
                      value={sshAlias}
                      onChange={e => setSshAlias(e.target.value)}
                      placeholder="~/.ssh/config 里的别名（如 staging）"
                      aria-label="~/.ssh/config 别名"
                    />
                    <button
                      type="button"
                      className="px-3 py-2 rounded text-xs bg-raised hover:bg-emphasis text-ink border border-line"
                      onClick={() => void fillFromSSHConfig()}
                      disabled={!sshAlias.trim()}
                      aria-label="从 ~/.ssh/config 读取该别名的配置"
                    >
                      读取配置
                    </button>
                  </div>
                  {sshConfigNote && (
                    <p className={`text-2xs ${sshConfigBad ? 'text-danger' : 'text-muted'} break-words`}>
                      {sshConfigNote}
                    </p>
                  )}

                  <div className="grid grid-cols-3 gap-3">
                  <div className="col-span-2">
                    <label htmlFor="ssh-host" className="block text-sm text-secondary mb-1">SSH 主机</label>
                    <input
                      id="ssh-host"
                      type="text"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.host}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, host: e.target.value } })}
                      placeholder="bastion.example.com"
                    />
                  </div>
                  <div>
                    <label htmlFor="ssh-port" className="block text-sm text-secondary mb-1">SSH 端口</label>
                    <input
                      id="ssh-port"
                      type="number"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.port}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, port: parseInt(e.target.value) || 22 } })}
                    />
                  </div>
                  <div className="col-span-3">
                    <label htmlFor="ssh-user" className="block text-sm text-secondary mb-1">SSH 用户名</label>
                    <input
                      id="ssh-user"
                      type="text"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.user}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, user: e.target.value } })}
                      placeholder="deploy"
                    />
                  </div>
                  <div className="col-span-3">
                    <label htmlFor="ssh-password" className="block text-sm text-secondary mb-1">
                      {isEdit ? 'SSH 密码（留空保持不变）' : 'SSH 密码'}
                    </label>
                    <input
                      id="ssh-password"
                      type="password"
                      autoComplete="off"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.password}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, password: e.target.value } })}
                      placeholder="••••••••"
                    />
                  </div>
                  <div className="col-span-3">
                    <label htmlFor="ssh-key" className="block text-sm text-secondary mb-1">
                      私钥文件（可用作密码替代）
                    </label>
                    <input
                      id="ssh-key"
                      type="text"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.keyFile}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, keyFile: e.target.value } })}
                      placeholder="C:\\Users\\me\\.ssh\\id_ed25519"
                    />
                  </div>
                  {config.ssh.keyFile !== '' && (
                    <div className="col-span-3">
                      <label htmlFor="ssh-passphrase" className="block text-sm text-secondary mb-1">私钥口令</label>
                      <input
                        id="ssh-passphrase"
                        type="password"
                        autoComplete="off"
                        className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                        value={config.ssh.passphrase}
                        onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, passphrase: e.target.value } })}
                      />
                    </div>
                  )}
                  </div>
                </>
              )}
            </div>
          )}

          <div className="flex items-center space-x-2">
            <button
              type="button"
              className={`flex items-center px-4 py-2 rounded text-sm ${
                testing
                  ? 'bg-emphasis text-secondary'
                  : testResult === 'success'
                  ? 'bg-success text-on-success'
                  : testResult === 'error'
                  ? 'bg-danger text-on-danger'
                  : 'bg-raised hover:bg-emphasis text-ink'
              }`}
              onClick={handleTest}
              disabled={testing}
            >
              {testing ? (
                '测试中…'
              ) : testResult === 'success' ? (
                <>
                  <FiCheck className="mr-2" />
                  已连接
                </>
              ) : (
                <>
                  <FiDatabase className="mr-2" />
                  测试连接
                </>
              )}
            </button>
          </div>

          {formError && (
            <p className="text-xs text-danger" role="alert">{formError}</p>
          )}

          <div className="flex justify-end space-x-3 pt-4 border-t border-line">
            <button
              type="button"
              className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink"
              onClick={onClose}
              disabled={saving}
            >
              取消
            </button>
            <button
              type="submit"
              className="px-4 py-2 rounded text-sm bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-40"
              disabled={saving}
            >
              {saving ? '保存中…' : isEdit ? '保存修改' : '保存连接'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};

export default ConnectionDialog;
