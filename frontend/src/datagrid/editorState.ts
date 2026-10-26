// The edit/apply pipeline: what is typed into a cell, what is queued for the
// database, and what the review dialog shows. Kept in one reducer because the
// queue and the open editor never change alone - committing an edit closes the
// editor, applying clears both, and the preview text is the payload of the
// apply. Held out of the component so the transition table is readable in
// isolation (see tests/editorState.test.ts).

import {
  addRow,
  dropRow,
  emptyQueue,
  setRowValue,
  toggleDelete,
  withEdit,
} from '../gridView.ts';
import type { ChangeQueue } from '../gridView.ts';

export interface EditingCell {
  key: string;
  column: string;
}

// GridCursor is the focused cell (P1-5): arrows/Tab/Enter move it, F2 edits it.
// row indexes the *view* rows the grid shows (sorted/filtered order), column
// indexes the visible columns.
export interface GridCursor {
  row: number;
  column: number;
}

export interface EditState {
  queue: ChangeQueue;
  // One snapshot per mutating gesture, popped by undo. Queues are immutable in
  // this reducer, so the snapshot is just the previous reference; the cap keeps
  // a long editing session from pinning hundreds of queues.
  undoStack: ChangeQueue[];
  // Selected rows (P1-6): sorted, de-duplicated indices into the view. A plain
  // click selects one row; Shift extends from the anchor; Ctrl/Cmd toggles.
  selected: number[];
  anchor: number | null;
  cursor: GridCursor | null;
  editing: EditingCell | null;
  // What the open editor holds. It is state, not a prop, because the FK list
  // narrows on it while the cell is still being typed into.
  draft: string;
  review: boolean;
  applying: boolean;
  applyError: string;
  statements: string[];
  planError: string;
}

export const emptyEditState = (): EditState => ({
  queue: emptyQueue(),
  undoStack: [],
  selected: [],
  anchor: null,
  cursor: null,
  editing: null,
  draft: '',
  review: false,
  applying: false,
  applyError: '',
  statements: [],
  planError: '',
});

// primarySelected is the row row-actions operate on: the cursor's row when there
// is one, else the first selected row. Everything that used to read a single
// `selected` index reads this instead.
export const primarySelected = (state: EditState): number | null => {
  if (state.cursor) return state.cursor.row;
  return state.selected.length > 0 ? state.selected[0] : null;
};

// rangeBetween is the inclusive span two rows cover, ascending.
const rangeBetween = (a: number, b: number): number[] => {
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  const out: number[] = [];
  for (let i = lo; i <= hi; i++) out.push(i);
  return out;
};

export type EditAction =
  | { type: 'editBegin'; key: string; column: string; value: string }
  | { type: 'editDraft'; value: string }
  // cursorSet is a click: plain selects one row, extend (Shift) spans from the
  // anchor, toggle (Ctrl/Cmd) flips one row in or out.
  | { type: 'cursorSet'; row: number; column: number; extend?: boolean; toggle?: boolean }
  // cursorMove is the keyboard: it moves the focused cell and, with extend,
  // grows the row selection the way Shift+arrow does.
  | { type: 'cursorMove'; row: number; column: number; extend?: boolean }
  | { type: 'editClose' }
  | { type: 'editCommitFetched'; index: number; column: string; value: string; original: string }
  | { type: 'editCommitFresh'; id: number; column: string; value: string }
  | { type: 'rowAppend'; id: number; column: string }
  | { type: 'rowDuplicate'; id: number; values: Record<string, string> }
  | { type: 'rowDrop'; id: number }
  | { type: 'deleteToggle'; index: number }
  | { type: 'queueClear' }
  | { type: 'undo' }
  | { type: 'applyStart' }
  | { type: 'applyResult'; ok: boolean; error: string }
  | { type: 'preview'; statements: string[]; error: string }
  | { type: 'reviewOpen' }
  | { type: 'reviewClose' }
  | { type: 'editReset' };

// withUndo threads every queue mutation through the undo stack: a gesture
// that actually changed the queue pushes the previous one.
const withUndo = (state: EditState, next: Partial<EditState>): EditState => {
  const changed = next.queue !== undefined && next.queue !== state.queue;
  return {
    ...state,
    ...next,
    undoStack: changed ? [...state.undoStack.slice(-49), state.queue] : state.undoStack,
  };
};

export const editReducer = (state: EditState, action: EditAction): EditState => {
  switch (action.type) {
    case 'editBegin':
      return { ...state, editing: { key: action.key, column: action.column }, draft: action.value };

    case 'editDraft':
      return { ...state, draft: action.value };

    case 'cursorSet': {
      const cursor = { row: action.row, column: action.column };
      if (action.toggle) {
        // Ctrl/Cmd-click: flip this row in or out. The anchor follows the
        // click, so a following Shift-click spans from the row just toggled —
        // the same reading Excel and most grids give it.
        const has = state.selected.includes(action.row);
        const selected = has
          ? state.selected.filter(i => i !== action.row)
          : [...state.selected, action.row].sort((a, b) => a - b);
        return { ...state, cursor, selected, anchor: action.row };
      }
      if (action.extend && state.anchor !== null) {
        return { ...state, cursor, selected: rangeBetween(state.anchor, action.row) };
      }
      return { ...state, cursor, selected: [action.row], anchor: action.row };
    }

    case 'cursorMove': {
      const cursor = { row: action.row, column: action.column };
      // Shift+arrow grows from the anchor; a plain move re-anchors.
      if (action.extend && state.anchor !== null) {
        return { ...state, cursor, selected: rangeBetween(state.anchor, action.row) };
      }
      return { ...state, cursor, selected: [action.row], anchor: action.row };
    }

    case 'editClose':
      // Nothing to cancel; keep the reference so an idle Escape costs no render.
      return state.editing ? { ...state, editing: null } : state;

    // The editor closes and the queue takes the value in the same step, so a
    // keystroke that blurs the cell can never be dropped between them.
    case 'editCommitFetched':
      return withUndo(state, {
        editing: null,
        queue: {
          ...state.queue,
          updates: withEdit(state.queue.updates, action.index, action.column, action.original, action.value),
        },
      });

    case 'editCommitFresh':
      return withUndo(state, {
        editing: null,
        queue: setRowValue(state.queue, action.id, action.column, action.value),
      });

    case 'rowAppend':
      return withUndo(state, {
        queue: addRow(state.queue, action.id),
        editing: { key: `n${action.id}`, column: action.column },
        draft: '',
      });

    case 'rowDuplicate':
      return withUndo(state, { queue: addRow(state.queue, action.id, action.values) });

    // Cancelling an added row drops it, and the editor with it if that row was
    // the one open - a closed cell must not keep a stale draft behind it.
    case 'rowDrop':
      return withUndo(state, {
        queue: dropRow(state.queue, action.id),
        editing: state.editing?.key === `n${action.id}` ? null : state.editing,
      });

    case 'deleteToggle':
      return withUndo(state, { queue: toggleDelete(state.queue, action.index), editing: null });

    case 'queueClear':
      return withUndo(state, { queue: emptyQueue() });

    case 'undo': {
      const prev = state.undoStack[state.undoStack.length - 1];
      if (!prev) return state;
      // A closed editor must not survive the gesture it belonged to.
      return { ...state, queue: prev, undoStack: state.undoStack.slice(0, -1), editing: null };
    }

    case 'applyStart':
      return { ...state, applying: true, applyError: '' };

    case 'applyResult':
      return action.ok
        ? { ...state, applying: false, review: false, queue: emptyQueue(), statements: [] }
        : { ...state, applying: false, applyError: action.error };

    case 'preview':
      return { ...state, statements: action.statements, planError: action.error };

    case 'reviewOpen':
      return { ...state, applyError: '', review: true };

    case 'reviewClose':
      return { ...state, review: false };

    // A new result shares nothing with the rows that were queued, so the whole
    // pipeline goes. Sorting and filtering are views over the columns, not the
    // rows, so they are left to the column reducer.
    case 'editReset':
      return emptyEditState();

    default:
      return state;
  }
};
