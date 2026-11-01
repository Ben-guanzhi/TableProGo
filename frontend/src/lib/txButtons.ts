// The tab's transaction button gate.
//
// The tx badge reflects the *server's* answer, so it lags the request: between
// a click on 开始事务 and the response, the badge still reads autocommit and the
// button is still there. A double-click therefore sends the same op twice, and
// the second request fails with "this tab already has an open transaction" — a
// spurious error toast for a click the user meant once.
//
// The gate is per tab and per op: a click while an op is in flight is ignored,
// and the button is disabled for the same reason. The server-side session
// refuses double Begins anyway (internal/txn), so this is UX hygiene, not a
// correctness boundary.

export type TxOp = 'begin' | 'commit' | 'rollback';

// txGate records a click on op unless this tab already has an op in flight.
// The returned value is the tab's new pending op (unchanged when ignored).
export function txGate(pending: TxOp | null, op: TxOp): TxOp | null {
  return pending !== null ? pending : op;
}

// txSettle clears the pending op once the request has settled, success or
// failure: the badge (or the error toast) has caught up by then.
export function txSettle(_pending: TxOp | null): TxOp | null {
  return null;
}

// txBusy reports whether a transaction op for the tab is still in flight; the
// buttons are disabled while it is.
export function txBusy(pending: TxOp | null): boolean {
  return pending !== null;
}
