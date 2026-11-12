import React, { useEffect, useRef, useState } from 'react';
import { FiCheck, FiDownloadCloud, FiInfo, FiX } from 'react-icons/fi';
import { BuildInfo, CheckUpdate, GetBuildInfo, UpdateCheckResult } from '../api';
import { useDialog } from '../dialog';

interface AboutDialogProps {
  // The version URL the owner configured (empty = the check is off).
  updateUrl?: string;
  onClose: () => void;
}

// W15 (private distribution): 关于.
//
// Two jobs, both for a build that is handed out privately rather than published:
// say exactly which build this is (the first question in any bug report), and offer
// an update check that the owner enables by pointing at a version file they host.
//
// The check never runs on its own: with no URL the panel says so instead of showing
// a green "up to date" it did not verify.
const AboutDialog: React.FC<AboutDialogProps> = ({ updateUrl = '', onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [info, setInfo] = useState<BuildInfo | null>(null);
  const [check, setCheck] = useState<UpdateCheckResult | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void GetBuildInfo()
      .then(setInfo)
      .catch(() => setInfo(null));
  }, []);

  const runCheck = async () => {
    setBusy(true);
    try {
      setCheck(await CheckUpdate(updateUrl));
    } catch (e) {
      setCheck({ current: info?.version ?? '', checked: false, error: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="about-title"
        className="bg-panel rounded-lg w-full max-w-md flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line">
          <FiInfo size={15} className="mr-2 text-accent" />
          <h2 id="about-title" className="text-sm font-semibold">
            关于
          </h2>
          <button className="ml-auto p-1 text-secondary hover:text-ink" onClick={onClose} aria-label="关闭关于">
            <FiX size={16} />
          </button>
        </div>

        <div className="p-5 space-y-3">
          {info ? (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
              <dt className="text-muted">版本</dt>
              <dd className="font-mono text-ink">{info.version}</dd>
              <dt className="text-muted">提交</dt>
              <dd className="font-mono text-ink">{info.commit}</dd>
              <dt className="text-muted">构建日期</dt>
              <dd className="font-mono text-ink">{info.date}</dd>
              <dt className="text-muted">平台</dt>
              <dd className="font-mono text-ink">
                {info.os}/{info.arch} · {info.go}
              </dd>
            </dl>
          ) : (
            <p className="text-xs text-muted">读取构建信息…</p>
          )}

          <div className="border-t border-line pt-3 space-y-1.5">
            <div className="text-2xs font-semibold text-secondary">更新检查</div>
            {updateUrl.trim() === '' ? (
              <p className="text-2xs text-muted leading-relaxed">
                未配置版本文件地址，因此不会联网检查。本程序不会自行联网；需要检查时，
                请把 <span className="font-mono">updateUrl</span> 指向你自托管的版本文件
                （<span className="font-mono">{'{"version":"1.2.0","notes":"…"}'}</span>）。
              </p>
            ) : (
              <>
                <p className="text-2xs text-muted break-all font-mono">{updateUrl}</p>
                <button
                  className="px-2 py-1 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised disabled:opacity-40 flex items-center gap-1"
                  onClick={() => void runCheck()}
                  disabled={busy}
                >
                  <FiDownloadCloud size={11} />
                  {busy ? '检查中…' : '检查更新'}
                </button>
              </>
            )}

            {check && (
              <div className="text-2xs space-y-0.5 pt-1">
                {check.error && <p className="text-danger break-words">检查失败：{check.error}</p>}
                {!check.error && check.newer && (
                  <p className="text-success flex items-center gap-1">
                    <FiCheck size={10} /> 有新版本 {check.newer}（当前 {check.current}）
                  </p>
                )}
                {!check.error && !check.newer && check.checked && (
                  <p className="text-muted">已是最新（当前 {check.current}）</p>
                )}
                {check.notes && <p className="text-muted break-words whitespace-pre-wrap">{check.notes}</p>}
              </div>
            )}
          </div>
        </div>

        <div className="flex justify-end px-5 py-3 border-t border-line">
          <button className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  );
};

export default AboutDialog;
