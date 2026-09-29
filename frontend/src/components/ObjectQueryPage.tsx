import React from 'react';
import QueryEditor, { QueryEditorHandle } from './QueryEditor';
import DataGrid from './DataGrid';
import ErrorBanner from './ErrorBanner';
import RowSplitter, { EDITOR_MIN_HEIGHT, editorMaxHeight } from './RowSplitter';
import { AppSettings } from '../api';
import { ColumnInfo, QueryResult, SectionState, TableRef } from '../types';

interface ObjectQueryPageProps {
  sql: string;
  onSqlChange: (sql: string) => void;
  onExecute: () => void;
  loading: boolean;
  isConnected: boolean;
  dialect: string;
  databases: string[];
  database: string;
  onDatabaseChange: (database: string) => void;
  settings: AppSettings;
  onSettingsChange: (settings: AppSettings) => void;
  getTables: (database?: string) => Promise<string[]>;
  getColumns: (table: string, database?: string) => Promise<ColumnInfo[]>;
  result: SectionState<QueryResult>;
  comments?: Record<string, string>;
  editSource?: TableRef | null;
  onApplied?: () => void;
  onPendingChange?: (total: number) => void;
  exportName: string;
  onDismissError: () => void;
  editorRef: React.Ref<QueryEditorHandle>;
  height: number;
  onResizeStart: (event: React.PointerEvent) => void;
  onResizeStep: (delta: number) => void;
}

// The object tab's query page: a statement of its own that runs against the
// table's database, with the result right below it.
const ObjectQueryPage: React.FC<ObjectQueryPageProps> = ({
  sql,
  onSqlChange,
  onExecute,
  loading,
  isConnected,
  dialect,
  databases,
  database,
  onDatabaseChange,
  settings,
  onSettingsChange,
  getTables,
  getColumns,
  result,
  comments,
  editSource,
  onApplied,
  onPendingChange,
  exportName,
  onDismissError,
  editorRef,
  height,
  onResizeStart,
  onResizeStep,
}) => {
  const isError = !!result?.error && !loading;

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div style={{ height: isError ? undefined : height }} className={`min-h-0 ${isError ? 'flex-1' : 'shrink-0'}`}>
        <QueryEditor
          ref={editorRef}
          query={sql}
          onQueryChange={onSqlChange}
          onExecute={onExecute}
          isConnected={isConnected}
          loading={loading}
          dialect={dialect}
          databases={databases}
          database={database}
          onDatabaseChange={onDatabaseChange}
          settings={settings}
          onSettingsChange={onSettingsChange}
          getTables={getTables}
          getColumns={getColumns}
        />
      </div>

      <RowSplitter
        onPointerDown={onResizeStart}
        onStep={onResizeStep}
        label="编辑器高度"
        valueNow={height}
        valueMin={EDITOR_MIN_HEIGHT}
        valueMax={editorMaxHeight()}
      />

      {loading && (
        <div className="flex-1 flex flex-col items-center justify-center gap-2 text-muted text-sm border-t border-line">
          <span>执行中…</span>
        </div>
      )}

      {!loading && result?.error && (
        <div className="shrink-0">
          <ErrorBanner message={result.error} onDismiss={onDismissError} />
        </div>
      )}

      {!loading && !result?.error && result?.data && (
        <div className="flex-1 flex flex-col min-h-0">
          <DataGrid
            result={result.data}
            comments={comments}
            showComments={settings.showColumnComments}
            editSource={editSource}
            onApplied={onApplied}
            onPendingChange={onPendingChange}
            exportName={exportName}
          />
        </div>
      )}

      {!loading && !result && (
        <div className="flex-1 flex items-center justify-center text-muted text-sm border-t border-line">
          按 Ctrl+Enter 执行，结果就地显示在下方
        </div>
      )}
    </div>
  );
};

export default ObjectQueryPage;
