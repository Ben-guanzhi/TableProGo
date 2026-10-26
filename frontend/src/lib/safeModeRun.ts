import type { QueryResult } from '../types/index';

// The Safe Mode decision, kept free of React and of the HTTP client so the
// branching is testable on its own (see tests/queryRun.test.ts) and the policy
// stays in one place — the backend decides *whether* to ask (safemode.go), this
// only collects the answer.

export const emptyResult = (): QueryResult => ({
  columns: [],
  rows: [],
  rowCount: 0,
  affectedRows: 0,
  lastInsertId: 0,
  executionTime: 0,
});

export const cancelledResult = (): QueryResult => ({
  ...emptyResult(),
  // Explicitly false: the user answered the prompt, so the UI must not keep
  // presenting the statement as still awaiting confirmation.
  needsConfirmation: false,
  error: 'Cancelled (Safe Mode)',
});

// confirmThenRun turns a paused result into either the confirmed execution or a
// cancellation. A result that was not paused is returned untouched, so callers
// can pipe every result through it.
//
// confirm may be synchronous or return a promise: the app answers through its
// own ConfirmDialog (P1-16), which is asynchronous, while tests answer inline.
export async function confirmThenRun(
  first: QueryResult,
  resend: () => Promise<QueryResult>,
  confirm: (message: string) => boolean | Promise<boolean>,
): Promise<QueryResult> {
  if (!first.needsConfirmation) return first;
  if (!(await confirm(confirmMessage(first)))) {
    return cancelledResult();
  }
  return resend();
}

// confirmMessage is the text shown in the prompt for a paused result.
export const confirmMessage = (result: QueryResult): string =>
  `${result.error ?? ''}\n\n仍要执行这条语句吗？`;
