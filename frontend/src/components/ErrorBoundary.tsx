import { Component, type ReactNode } from 'react';

import { boundaryDetail } from '../lib/boundaryText';

// A render crash used to unmount the whole app: React 18 shows a blank page and
// the user can only reload. The boundary catches the error at the tree level and
// offers exactly that, with the message that caused it.

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: string | null;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: null };

  static getDerivedStateFromError(error: unknown): ErrorBoundaryState {
    return { failed: boundaryDetail(error) };
  }

  render() {
    if (this.state.failed !== null) {
      return (
        <div className="fixed inset-0 flex items-center justify-center bg-bg p-6">
          <div className="max-w-lg rounded border border-line bg-raised p-4">
            <h1 className="text-sm font-semibold text-ink mb-2">界面渲染出错</h1>
            <p className="text-2xs text-secondary break-all whitespace-pre-wrap mb-3">
              {this.state.failed}
            </p>
            <button
              className="px-3 py-1 rounded text-2xs bg-raised text-secondary hover:text-ink border border-line"
              onClick={() => window.location.reload()}
            >
              重新加载
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
