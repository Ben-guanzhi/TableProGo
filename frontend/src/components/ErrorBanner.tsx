import React, { useState } from 'react';
import { FiAlertTriangle, FiCheck, FiCopy, FiX } from 'react-icons/fi';

interface ErrorBannerProps {
  message: string;
  onDismiss?: () => void;
}

const ErrorBanner: React.FC<ErrorBannerProps> = ({ message, onDismiss }) => {
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard.writeText(message).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="flex items-start gap-2 px-3 py-1.5 bg-danger/12 border-t border-danger/35 text-danger shrink-0">
      <FiAlertTriangle size={13} aria-hidden className="mt-[3px] shrink-0" />
      <div className="flex-1 min-w-0 max-h-[28vh] overflow-auto font-mono text-[0.78125rem] leading-relaxed whitespace-pre-wrap break-all">
        {message}
      </div>
      <button
        className="flex items-center justify-center w-6 h-6 shrink-0 text-danger hover:text-ink"
        onClick={copy}
        aria-label="复制错误信息"
        title="复制错误信息"
      >
        {copied ? <FiCheck size={13} /> : <FiCopy size={13} />}
      </button>
      {onDismiss && (
        <button
          className="flex items-center justify-center w-6 h-6 shrink-0 text-danger hover:text-ink"
          onClick={onDismiss}
          aria-label="关闭错误提示"
          title="关闭错误提示"
        >
          <FiX size={13} />
        </button>
      )}
    </div>
  );
};

export default ErrorBanner;
