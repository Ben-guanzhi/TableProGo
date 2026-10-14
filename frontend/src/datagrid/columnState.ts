// The column chrome: manual widths, the locate-column box, sorting, filtering,
// the filter menu, and where the export menu hangs. One reducer because the
// filter menu, its draft and the filter it sets are one gesture - picking an
// operator that needs a value keeps the menu open, one that does not sets the
// filter and closes it. Held out of the component so the transition table is
// readable in isolation (see tests/columnState.test.ts).

import { opNeedsValue } from '../gridView.ts';
import type { ColumnFilter, FilterOp, SortState } from '../gridView.ts';

// Anchored to the header cell that raised it, so it has to go away as soon as
// the click or scroll that would detach it happens.
export interface ColumnMenu {
  x: number;
  y: number;
  column: string;
  op: FilterOp | null;
}

export interface ColumnState {
  // Widths the user has dragged, keyed by column name. Anything not here falls
  // back to the sampled width, which is why the key is the name and not the
  // position - a sort can move a column without losing its size.
  overrides: Record<string, number>;
  locate: string;
  // Which match the locate box is pointing at. 0 until Enter walks it.
  activeMatch: number;
  sort: SortState | null;
  filters: Record<string, ColumnFilter>;
  menu: ColumnMenu | null;
  // What the menu's input box holds. Seeded from the existing filter, so
  // reopening a column keeps what was typed before.
  filterDraft: string;
  // Where the export menu hangs from the toolbar button. It is rendered next to
  // the button, so Tab walks straight into the items.
  exportMenu: { left: number; top: number } | null;
}

export const emptyColumnState = (): ColumnState => ({
  overrides: {},
  locate: '',
  activeMatch: 0,
  sort: null,
  filters: {},
  menu: null,
  filterDraft: '',
  exportMenu: null,
});

export type ColumnAction =
  | { type: 'locateType'; value: string }
  | { type: 'locateClear' }
  | { type: 'locateNext'; total: number }
  | { type: 'sortToggle'; column: string; dir: 'asc' | 'desc' }
  | { type: 'sortClear' }
  | { type: 'filterSet'; column: string; filter: ColumnFilter | null }
  | { type: 'menuOpen'; x: number; y: number; column: string }
  | { type: 'menuPickOp'; op: FilterOp }
  | { type: 'menuClose' }
  | { type: 'draftType'; value: string }
  | { type: 'viewsClear' }
  | { type: 'widthSet'; name: string; width: number }
  | { type: 'widthReset'; name: string }
  | { type: 'exportMenuOpen'; left: number; top: number }
  | { type: 'exportMenuClose' }
  | { type: 'columnReset' };

export const columnReducer = (state: ColumnState, action: ColumnAction): ColumnState => {
  switch (action.type) {
    // Typing starts the walk over again, so a stale match cannot be walked into.
    case 'locateType':
      return { ...state, locate: action.value, activeMatch: 0 };

    case 'locateClear':
      return { ...state, locate: '', activeMatch: 0 };

    // Enter walks the matches and wraps, so a long list never dead-ends.
    case 'locateNext':
      return action.total > 0 ? { ...state, activeMatch: (state.activeMatch + 1) % action.total } : state;

    // A click on the active direction lifts the sort rather than re-sorting it.
    case 'sortToggle':
      return {
        ...state,
        sort: state.sort && state.sort.column === action.column && state.sort.dir === action.dir ? null : { column: action.column, dir: action.dir },
      };

    case 'sortClear':
      return state.sort ? { ...state, sort: null } : state;

    case 'filterSet': {
      const filters = { ...state.filters };
      if (action.filter) filters[action.column] = action.filter;
      else delete filters[action.column];
      return { ...state, filters };
    }

    // Seeded from the existing filter so the box does not start empty.
    case 'menuOpen':
      return {
        ...state,
        filterDraft: state.filters[action.column]?.value ?? '',
        menu: { x: action.x, y: action.y, column: action.column, op: null },
      };

    // A value-taking operator keeps the menu open to take its value; the rest
    // set the filter and close, since there is nothing more to type.
    case 'menuPickOp': {
      const menu = state.menu;
      if (!menu) return state;
      if (opNeedsValue(action.op)) return { ...state, menu: { ...menu, op: action.op } };
      return { ...state, filters: { ...state.filters, [menu.column]: { op: action.op, value: '' } }, menu: null };
    }

    case 'menuClose':
      return state.menu ? { ...state, menu: null } : state;

    case 'draftType':
      return { ...state, filterDraft: action.value };

    case 'viewsClear':
      return { ...state, sort: null, filters: {} };

    case 'widthSet':
      return { ...state, overrides: { ...state.overrides, [action.name]: action.width } };

    case 'widthReset': {
      const overrides = { ...state.overrides };
      delete overrides[action.name];
      return { ...state, overrides };
    }

    case 'exportMenuOpen':
      return { ...state, exportMenu: { left: action.left, top: action.top } };

    case 'exportMenuClose':
      return state.exportMenu ? { ...state, exportMenu: null } : state;

    // A new result detaches both menus from the cells they were anchored to.
    // Sorting and filtering are views over the columns, not the rows, so they
    // are kept: a name that no longer exists simply matches nothing.
    case 'columnReset':
      return { ...state, menu: null, exportMenu: null };

    default:
      return state;
  }
};
