import {
  ExecuteQuery,
  ExecuteQueryConfirmed,
  ExecuteQueryInTab,
  ExecuteQueryInTabConfirmed,
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
  // Required on purpose: the app answers through its own ConfirmDialog
  // (P1-16), and a silent window.confirm default is exactly the inconsistency
  // this parameter exists to remove.
  confirm: (message: string) => boolean | Promise<boolean>,
  // tabId routes the statement through the tab-scoped endpoint so it joins that
  // tab's open transaction. Empty means "no transaction", which is what a
  // background statement wants.
  tabId = '',
): Promise<QueryResult> {
  const first = (tabId
    ? await ExecuteQueryInTab(connId, tabId, sql, database)
    : await ExecuteQuery(connId, sql, database)) as unknown as QueryResult;
  return confirmThenRun(
    first,
    async () =>
      (tabId
        ? await ExecuteQueryInTabConfirmed(connId, tabId, sql, database)
        : await ExecuteQueryConfirmed(connId, sql, database)) as unknown as QueryResult,
    confirm,
  );
}

// runStreamed paints the grid while the server is still reading: each NDJSON
// batch is handed to onPartial, so a large result shows its first screen
// immediately. A Safe Mode pause arrives as an error event with nothing
// executed; it is confirmed here and re-sent through the buffered confirmed
// endpoint.
//
// cancel (optional AbortSignal) aborts the browser side of the stream: the
// server-side statement is stopped through /api/cancel-query as before, and
// this signal ends the fetch/reader immediately instead of draining whatever
// the server still pushes afterwards.
export async function runStreamed(
  connId: string,
  sql: string,
  database: string,
  onPartial: (result: QueryResult) => void,
  // When the tab holds an open transaction the statement must run inside it, and
  // only the buffered tab-scoped endpoint does that (the streaming endpoint talks
  // to the pool directly).
  tabId = '',
  txActive = false,
  confirm: (message: string) => boolean | Promise<boolean> = () => false,
  cancel?: AbortSignal,
): Promise<QueryResult> {
  let columns: Column[] = [];
  let rows: any[][] = [];
  const partial = (): QueryResult => ({
    ...emptyResult(),
    columns,
    rows,
    rowCount: rows.length,
  });
  if (txActive && tabId) {
    const first = (await ExecuteQueryInTab(connId, tabId, sql, database)) as unknown as QueryResult;
    // Safe Mode may pause a write inside the transaction too: the answer is
    // collected here exactly as on the non-tx path, and the confirmed
    // variant re-runs inside the same tab transaction. Piping through
    // confirmThenRun is a no-op for results that were not paused.
    const final = await confirmThenRun(
      first,
      async () =>
        (await ExecuteQueryInTabConfirmed(connId, tabId, sql, database)) as unknown as QueryResult,
      confirm,
    );
    onPartial(final);
    return final;
  }
  const done = await ExecuteQueryStream(
    connId,
    sql,
    database,
    {
      onColumns: next => {
        columns = next;
        onPartial(partial());
      },
      onRows: batch => {
        // Push, not concat: concat creates a new array each batch (O(n) per
        // batch, O(n²) total). For a 100k-row result this is 500 × 100k =
        // 50M element copies. Push is O(1) amortized per row.
        rows.push(...batch);
        // Defer the caller's callback so the reader loop never blocks on
        // DOM/React work: if onPartial is slow, the browser's receive buffer
        // grows while the reader.read() loop is stuck. queueMicrotask runs
        // after the current event handler, giving the caller a chance to
        // process without delaying the next reader.read().
        queueMicrotask(() => onPartial(partial()));
      },
      signal: cancel,
    },
    // The run registers under this tab's id: /api/cancel-query with a tabId
    // stops exactly this statement, not a neighbouring tab's.
    tabId,
  );
  if (done.type === 'error' && done.needsConfirmation) {
    const paused: QueryResult = { ...emptyResult(), error: done.error, needsConfirmation: true };
    // The confirmed re-send must stay on the path the original statement was
    // going to take: the streaming endpoint paused a statement that may belong
    // to the tab's transaction (stream.go routes a write through
    // ExecuteQueryInTab when the tab holds one). Re-sending through the
    // connection-scoped /api/execute-confirmed ran it in autocommit — the user
    // confirmed a write they believed was governed by their transaction, and
    // rollback could no longer undo it. The tab-scoped confirmed variant keeps
    // the transaction governing it; a background run (no tabId) has nothing to
    // escape and keeps the connection-scoped route.
    const answer = confirmThenRun(
      paused,
      tabId
        ? async () =>
            (await ExecuteQueryInTabConfirmed(connId, tabId, sql, database)) as unknown as QueryResult
        : async () => (await ExecuteQueryConfirmed(connId, sql, database)) as unknown as QueryResult,
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
