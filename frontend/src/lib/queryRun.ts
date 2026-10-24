import {
  ExecuteQuery,
  ExecuteQueryConfirmed,
  ExecuteQueryStream,
} from '../api';
import { Column, QueryResult } from '../types';
import { confirmThenRun, emptyResult } from './safeModeRun';

// Running one statement from the editor: the buffered path, the streaming path,
// and the Safe Mode prompt both share.
//
// These live outside App.tsx because they hold no React state: they take the
// connection, the SQL and a callback for partial rows, and return the final
// result. That makes them the one piece of the editor's behaviour that can be
// tested without mounting the app.


// runGuarded sends a statement and, when Safe Mode asks, shows one blocking
// confirmation before re-sending through the confirmed endpoint. The backend has
// already decided *whether* to ask; this only collects the answer, so the policy
// lives in one place (safemode.go) instead of the UI.
export async function runGuarded(
  connId: string,
  sql: string,
  database: string,
  confirm: (message: string) => boolean = message => window.confirm(message),
): Promise<QueryResult> {
  const first = (await ExecuteQuery(connId, sql, database)) as unknown as QueryResult;
  return confirmThenRun(
    first,
    async () => (await ExecuteQueryConfirmed(connId, sql, database)) as unknown as QueryResult,
    confirm,
  );
}

// runStreamed paints the grid while the server is still reading: each NDJSON
// batch is handed to onPartial, so a large result shows its first screen
// immediately. A Safe Mode pause arrives as an error event with nothing
// executed; it is confirmed here and re-sent through the buffered confirmed
// endpoint.
export async function runStreamed(
  connId: string,
  sql: string,
  database: string,
  onPartial: (result: QueryResult) => void,
  confirm: (message: string) => boolean = message => window.confirm(message),
): Promise<QueryResult> {
  let columns: Column[] = [];
  let rows: any[][] = [];
  const partial = (): QueryResult => ({
    ...emptyResult(),
    columns,
    rows,
    rowCount: rows.length,
  });
  const done = await ExecuteQueryStream(connId, sql, database, {
    onColumns: next => {
      columns = next;
      onPartial(partial());
    },
    onRows: batch => {
      rows = rows.concat(batch);
      onPartial(partial());
    },
  });
  if (done.type === 'error' && done.needsConfirmation) {
    const paused: QueryResult = { ...emptyResult(), error: done.error, needsConfirmation: true };
    const answer = confirmThenRun(
      paused,
      async () => (await ExecuteQueryConfirmed(connId, sql, database)) as unknown as QueryResult,
      confirm,
    );
    return await answer;
  }
  return {
    ...partial(),
    rowCount: done.rowCount ?? rows.length,
    truncated: done.truncated,
    executionTime: done.executionTime ?? 0,
    error: done.error,
  };
}
