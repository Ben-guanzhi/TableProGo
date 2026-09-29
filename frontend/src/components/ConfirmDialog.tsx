import React, { useEffect, useRef } from 'react';
import { useDialog } from '../dialog';

export interface ConfirmRequest {
  title: string;
  message: string;
  confirmLabel: string;
  // Destructive requests start on the safe button, so Enter cannot wipe a table
  // by reflex.
  destructive?: boolean;
  onConfirm: () => void;
}

interface Props {
  request: ConfirmRequest;
  onDismiss: () => void;
}

const ConfirmDialog: React.FC<Props> = ({ request, onDismiss }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);

  useDialog(panelRef, onDismiss);

  useEffect(() => {
    // Destructive requests open on the safe button, so Enter cannot wipe a table
    // by reflex.
    (request.destructive ? cancelRef : confirmRef).current?.focus();
  }, [request]);

  const run = (accept: boolean) => {
    onDismiss();
    if (accept) request.onConfirm();
  };

  return (
    <div
      className="fixed inset-0 z-[60] bg-scrim/50 flex items-center justify-center px-6"
      onClick={() => run(false)}
    >
      <div
        ref={panelRef}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        aria-describedby="confirm-message"
        className="w-full max-w-sm bg-panel border border-line-strong rounded-lg shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <h2 id="confirm-title" className="px-5 pt-4 text-sm font-semibold">
          {request.title}
        </h2>
        <p id="confirm-message" className="px-5 py-3 text-[0.75rem] leading-relaxed text-secondary whitespace-pre-line">
          {request.message}
        </p>
        <div className="flex justify-end gap-2 px-5 pb-4">
          <button
            ref={cancelRef}
            className="px-3 py-1 rounded text-[0.75rem] text-secondary hover:text-ink hover:bg-raised"
            onClick={() => run(false)}
          >
            取消
          </button>
          <button
            ref={confirmRef}
            className={`px-3 py-1 rounded text-[0.75rem] ${
              request.destructive
                ? 'bg-danger hover:bg-danger-hover text-on-danger'
                : 'bg-accent hover:bg-accent-hover text-on-accent'
            }`}
            onClick={() => run(true)}
          >
            {request.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
};

export default ConfirmDialog;
