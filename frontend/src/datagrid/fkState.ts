// The foreign-key picker: which cell's list is open, what has been typed into
// it, which option is highlighted, and the candidate values cached per column.
// One reducer because opening a cell resets the highlight and the query, and
// typing narrows the list and moves the highlight together. Held out of the
// component so the transition table is readable in isolation (see
// tests/fkState.test.ts).

import { filterCandidates } from '../gridView.ts';
import type { ForeignKeyCandidates } from '../api.ts';
import type { ForeignKeyInfo } from '../types/index.ts';

// The open candidate list: which cell it belongs to and where to draw it. The
// values themselves come from the per-column cache, so this stays about position.
export interface FKPicker {
  rowKey: string;
  column: string;
  left: number;
  top: number;
  width: number;
  error: string;
}

export interface FKState {
  picker: FKPicker | null;
  // What has been typed since the list opened. The cell's own value stays in
  // the editor, so the list starts full and narrows only once typing starts.
  query: string;
  // -1 until the arrow keys or the list itself place the highlight somewhere.
  activeOption: number;
  // Candidate values per column, kept after the first read so reopening a cell
  // costs nothing.
  lists: Record<string, ForeignKeyCandidates>;
  // Constraints of the edited table, so a cell knows it holds a reference and
  // can offer a list instead of a bare text box.
  foreignKeys: ForeignKeyInfo[];
}

export const emptyFKState = (): FKState => ({
  picker: null,
  query: '',
  activeOption: -1,
  lists: {},
  foreignKeys: [],
});

export type FKAction =
  | { type: 'pickerOpen'; picker: FKPicker }
  | { type: 'pickerClose' }
  | { type: 'pickerError'; rowKey: string; column: string; error: string }
  | { type: 'queryType'; value: string }
  | { type: 'activeMove'; active: number }
  | { type: 'listSet'; key: string; value: ForeignKeyCandidates }
  | { type: 'constraintsSet'; foreignKeys: ForeignKeyInfo[] }
  | { type: 'fkReset' };

export const fkReducer = (state: FKState, action: FKAction): FKState => {
  switch (action.type) {
    // Opening resets the highlight and the query: the new list is full and the
    // arrow keys start from the top.
    case 'pickerOpen':
      return { ...state, picker: action.picker, query: '', activeOption: -1 };

    case 'pickerClose':
      return state.picker ? { ...state, picker: null } : state;

    // A failed read lands on the cell that asked, and only if it is still the
    // one showing the list - a stale response must not annotate a new cell.
    case 'pickerError':
      return state.picker && state.picker.rowKey === action.rowKey && state.picker.column === action.column
        ? { ...state, picker: { ...state.picker, error: action.error } }
        : state;

    // Typing narrows the list and puts the highlight back on its top row.
    case 'queryType':
      return { ...state, query: action.value, activeOption: 0 };

    case 'activeMove':
      return { ...state, activeOption: action.active };

    // A later response for the same column is dropped: the first read wins, so
    // a slow second request cannot replace what is already on screen.
    case 'listSet':
      return state.lists[action.key] ? state : { ...state, lists: { ...state.lists, [action.key]: action.value } };

    case 'constraintsSet':
      return { ...state, foreignKeys: action.foreignKeys };

    // A new source of its own is a catalog of its own: values cached for the
    // previous table would be another table's keys.
    case 'fkReset':
      return emptyFKState();

    default:
      return state;
  }
};

// The list as it stands for what has been typed. One definition feeds the
// panel and the cell's arrow keys, so the highlight can never disagree with
// the rows on screen.
export const pickerList = (state: FKState): ForeignKeyCandidates['values'] => {
  const read = state.picker ? state.lists[state.picker.column.toLowerCase()] : undefined;
  return read ? filterCandidates(read.values, state.query) : [];
};

// Which row that leaves highlighted, clamped so narrowing the list cannot keep
// a highlight on a row that has disappeared - Enter would take an unseen value.
export const highlightedIndex = (activeOption: number, length: number): number =>
  length ? Math.max(0, Math.min(activeOption, length - 1)) : -1;
