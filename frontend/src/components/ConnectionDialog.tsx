import { useState, useEffect, useRef } from 'react';
import { ConnectionConfig } from '../types';
import type { SSHConfig } from '../types';
import { FiX, FiDatabase, FiCheck } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { useDialog } from '../dialog';

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
  onAdd: (config: ConnectionConfig) => void;
  onUpdate?: (config: ConnectionConfig) => void;
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
  });

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<'success' | 'error' | null>(null);

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
        options: initialConfig.options || {},
        ssh: initialConfig.ssh ? { ...emptySSH, ...initialConfig.ssh } : { ...emptySSH },
        databaseFilter: initialConfig.databaseFilter,
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
    };

    const defaults: Record<string, { username: string; database: string }> = {
      mysql: { username: 'root', database: '' },
      postgresql: { username: 'postgres', database: '' },
      sqlite: { username: '', database: '' },
      sqlserver: { username: 'sa', database: 'master' },
      oracle: { username: 'system', database: 'ORCL' },
      redis: { username: '', database: '0' },
    };

    const d = defaults[type] || { username: 'root', database: '' };

    setConfig({
      ...config,
      type: type as 'mysql' | 'postgresql' | 'sqlite' | 'sqlserver' | 'oracle' | 'redis',
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
        toast.success('Connection successful!');
      } else {
        setTestResult('error');
        toast.error('Connection failed: unknown error');
      }
    } catch (error: unknown) {
      setTestResult('error');
      const msg = error instanceof Error ? error.message : String(error);
      toast.error(`Connection failed: ${msg}`, { duration: 8000 });
    } finally {
      setTesting(false);
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (isEdit && onUpdate) {
      onUpdate(config);
    } else {
      onAdd(config);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="connection-dialog-title" className="bg-panel rounded-lg w-full max-w-md shadow-xl">
        <div className="flex items-center justify-between px-6 py-4 border-b border-line">
          <h2 id="connection-dialog-title" className="text-lg font-semibold">{isEdit ? 'Edit Connection' : 'New Connection'}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭对话框">
            <FiX size={20} />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          <div>
            <label htmlFor="conn-name" className="block text-sm text-secondary mb-1">Connection Name</label>
            <input
              id="conn-name"
              autoFocus
              type="text"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={config.name}
              onChange={(e) => setConfig({ ...config, name: e.target.value })}
              placeholder="My Database"
              required
            />
          </div>

          <div>
            <label htmlFor="conn-type" className="block text-sm text-secondary mb-1">Database Type</label>
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
              <option value="redis">Redis</option>
            </select>
          </div>

          {config.type !== 'sqlite' && (
            <div className="grid grid-cols-3 gap-4">
              <div className="col-span-2">
                <label htmlFor="conn-host" className="block text-sm text-secondary mb-1">Host</label>
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
                <label htmlFor="conn-port" className="block text-sm text-secondary mb-1">Port</label>
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
              {config.type === 'sqlite' ? 'Database File Path' :
               config.type === 'oracle' ? 'Service Name / SID' :
               config.type === 'redis' ? 'Logical Database (0-15)' : 'Database'}
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
                'database name'
              }
            />
          </div>

          {config.type !== 'sqlite' && (
            <>
              <div>
                <label htmlFor="conn-username" className="block text-sm text-secondary mb-1">Username</label>
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
                  {isEdit ? 'Password (leave blank to keep current)' : 'Password'}
                </label>
                <input
                  id="conn-password"
                  type="password"
                  autoComplete="off"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={config.password}
                  onChange={(e) => setConfig({ ...config, password: e.target.value })}
                  placeholder={isEdit ? '•••••••• (blank = keep)' : '••••••••'}
                />
              </div>
            </>
          )}

          {config.type !== 'sqlite' && (
            <div>
              <label htmlFor="conn-ssl" className="block text-sm text-secondary mb-1">
                {config.type === 'sqlserver' ? 'Encrypt' : config.type === 'redis' ? 'TLS' : 'SSL Mode'}
              </label>
              <select
                id="conn-ssl"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={config.sslMode}
                onChange={(e) => setConfig({ ...config, sslMode: e.target.value })}
              >
                {config.type === 'sqlserver' ? (
                  <>
                    <option value="false">False (Default)</option>
                    <option value="true">True</option>
                    <option value="disable">Disable</option>
                    <option value="strict">Strict</option>
                  </>
                ) : (
                  <>
                    <option value="false">Disable</option>
                    <option value="true">Enable</option>
                    <option value="require">Require</option>
                  </>
                )}
              </select>
            </div>
          )}

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
                Route through an SSH tunnel (bastion host)
              </label>

              {config.ssh?.enabled && (
                <div className="grid grid-cols-3 gap-3">
                  <div className="col-span-2">
                    <label htmlFor="ssh-host" className="block text-sm text-secondary mb-1">SSH Host</label>
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
                    <label htmlFor="ssh-port" className="block text-sm text-secondary mb-1">SSH Port</label>
                    <input
                      id="ssh-port"
                      type="number"
                      className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                      value={config.ssh.port}
                      onChange={(e) => setConfig({ ...config, ssh: { ...config.ssh!, port: parseInt(e.target.value) || 22 } })}
                    />
                  </div>
                  <div className="col-span-3">
                    <label htmlFor="ssh-user" className="block text-sm text-secondary mb-1">SSH User</label>
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
                      {isEdit ? 'SSH Password (leave blank to keep current)' : 'SSH Password'}
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
                      Private Key File (alternative to password)
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
                      <label htmlFor="ssh-passphrase" className="block text-sm text-secondary mb-1">Key Passphrase</label>
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
                'Testing...'
              ) : testResult === 'success' ? (
                <>
                  <FiCheck className="mr-2" />
                  Connected
                </>
              ) : (
                <>
                  <FiDatabase className="mr-2" />
                  Test Connection
                </>
              )}
            </button>
          </div>

          <div className="flex justify-end space-x-3 pt-4 border-t border-line">
            <button
              type="button"
              className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink"
              onClick={onClose}
            >
              Cancel
            </button>
            <button
              type="submit"
              className="px-4 py-2 rounded text-sm bg-accent hover:bg-accent-hover text-on-accent"
            >
              {isEdit ? 'Update Connection' : 'Save Connection'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};

export default ConnectionDialog;
