import React, { useEffect, useMemo, useReducer, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import {
  FiArrowDown,
  FiArrowUp,
  FiChevronDown,
  FiCopy,
  FiDownload,
  FiFilter,
  FiPlus,
  FiSearch,
  FiTrash2,
  FiX,
  FiXCircle,
} from 'react-icons/fi';
import {
  ApplyChanges,
  BuildChanges,
  ExportInsertStatements,
  ExportWorkbook,
  GetColumns,
  GetForeignKeyValues,
  GetTableForeignKeys,
} from '../api';
import { copyText } from '../clipboard';
import { scrollBehavior } from '../motion';
import { ChangeSet, QueryResult, TableRef } from '../types';
import {
  buildView,
  cellText,
  ColumnFilter,
  copyValues,
  cycleActive,
  displayCell,
  filterLabels,
  FilterOp,
  opNeedsValue,
  foreignKeyFor,
  NewRow,
  queueCounts,
  toCSV,
  toJSON,
  toMarkdown,
  toRowChanges,
} from '../gridView';
import { download, downloadBlob } from '../datagrid/download';
import { columnReducer, emptyColumnState } from '../datagrid/columnState';
import { editReducer, emptyEditState } from '../datagrid/editorState';
import { fkReducer, emptyFKState, highlightedIndex, pickerList } from '../datagrid/fkState';
import {
  MANUAL_MAX_WIDTH,
  MANUAL_MIN_WIDTH,
  ROW_NUMBER_WIDTH,
  useColumnWidths,
} from '../datagrid/geometry';
import { optionId, PICKER_HEIGHT } from '../datagrid/picker';
import ErrorBanner from './ErrorBanner';
import FKPickerPanel from './FKPickerPanel';
import FilterMenu, { MenuItem } from './FilterMenu';
import ReviewDialog from './ReviewDialog';

interface DataGridProps {
  result: QueryResult;
  // Column comments keyed by lower-cased column name.
  comments?: Record<string, string>;
  showComments?: boolean;
  onDismissError?: () => void;
  // File name stem used by the CSV/JSON export.
  exportName?: string;
  // The table these rows came from. Without it the grid stays read-only,
  // because there is no row to write back to.
  editSource?: TableRef | null;
  // Called after an applied change, so the owner can reload what it shows.
  onApplied?: () => void;
  // How many edits are waiting for the database. The owner needs it to warn
  // before a tab holding them goes away.
  onPendingChange?: (total: number) => void;
}

// The open candidate list: which cell it belongs to and where to draw it. The
// values themselves come from the per-column cache, so this stays about position.
interface FKPicker {
  rowKey: string;
  column: string;
  left: number;
  top: number;
  width: number;
  error: string;
}

// Sorting and filtering work on the rows already on screen; the grid never
// re-queries, so what is ordered here is exactly what was read (see gridView.ts).

// One row of the grid body: a fetched row addressed by its position in `rows`,
// or a queued new row that has no position yet.
interface BodyRow {
  key: string;
  index: number;
  fresh: NewRow | null;
}

// The shapes the export menu offers. The two SQL ones are the same script taken
// two different ways out; only the backend can render it, because only it knows
// what each column holds.
type ExportFormat = 'csv' | 'json' | 'markdown' | 'xlsx' | 'sql';

const DataGrid: React.FC<DataGridProps> = ({
  result,
  comments,
  showComments,
  onDismissError,
  exportName,
  editSource,
  onApplied,
  onPendingChange,
}) => {
  // Three clusters, one reducer each, so the transition table of every cluster is
  // readable on its own. The clusters barely touch: only the FK picker reaches
  // into the editor, by committing the cell it opened.
  const [editor, dispatchEditor] = useReducer(editReducer, undefined, emptyEditState);
  const [gridCols, dispatchColumns] = useReducer(columnReducer, undefined, emptyColumnState);
  const [fk, dispatchFk] = useReducer(fkReducer, undefined, emptyFKState);

  const { queue, selected, editing, draft, review, applying, applyError, statements, planError } = editor;
  const { overrides, locate, activeMatch, sort, filters, menu, filterDraft, exportMenu } = gridCols;
  const { picker, query: fkQuery, activeOption, lists: fkLists, foreignKeys } = fk;

  // The table's own key columns, read from the catalog: a duplicate leaves them
  // out, so the copy gets an identity instead of colliding with its source. Left
  // as its own state because it is fetched per source and read only by a copy.
  const [keyNames, setKeyNames] = useState<string[]>([]);

  // Ids for queued new rows, so typing in one cannot renumber another. A ref, not
  // state: it never re-renders and never needs to.
  const rowSeq = useRef(1);
  const scrollerRef = useRef<HTMLDivElement>(null);

  const autoWidths = useColumnWidths(result);
  const columns = result.columns ?? [];
  const rows = result.rows ?? [];
  const widths = columns.map((col, i) => overrides[col.name] ?? autoWidths[i]);

  // Row indices in display order. Changes stay keyed by the position in `rows`,
  // so reordering or filtering can never move a pending change onto another row.
  const view = useMemo(() => buildView(rows, columns, filters, sort), [rows, columns, filters, sort]);

  const counts = useMemo(() => queueCounts(queue), [queue]);

  // The queue lives in this component and dies with it, so the owner is told
  // what is waiting; that is the only way it can warn before a tab closes.
  useEffect(() => {
    onPendingChange?.(counts.total);
    return () => onPendingChange?.(0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [counts.total]);

  // The body is the fetched rows in view order, then the queued new rows at the
  // bottom - a new row is not part of `rows`, so it is keyed by its own id.
  const body: BodyRow[] = useMemo(
    () => [
      ...view.map(index => ({ key: `u${index}`, index, fresh: null })),
      ...queue.inserts.map(fresh => ({ key: `n${fresh.id}`, index: -1, fresh })),
    ],
    [view, queue.inserts],
  );

  const targetName = editSource
    ? [editSource.database, editSource.schema, editSource.table].filter(Boolean).join('.')
    : '';

  // Which columns count as the key is the table's business, not the result's, so
  // it is read from the catalog once per source instead of guessed from names.
  const sourceKey = editSource
    ? `${editSource.connId}|${editSource.database}|${editSource.schema}|${editSource.table}`
    : '';
  useEffect(() => {
    if (!editSource) {
      dispatchFk({ type: 'constraintsSet', foreignKeys: [] });
      return;
    }
    let current = true;
    GetColumns(editSource.connId, editSource.database, editSource.schema, editSource.table).then(
      list => {
        if (current) setKeyNames(list.filter(c => c.isPrimaryKey).map(c => c.name));
      },
      () => {
        if (current) setKeyNames([]);
      },
    );
    // A source of its own is a catalog of its own: values cached for the previous
    // table would be another table's keys.
    dispatchFk({ type: 'fkReset' });
    GetTableForeignKeys(editSource).then(
      list => {
        if (current) dispatchFk({ type: 'constraintsSet', foreignKeys: list });
      },
      () => {
        if (current) dispatchFk({ type: 'constraintsSet', foreignKeys: [] });
      },
    );
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceKey]);

  // Which result columns hold a single-column reference. Composite ones are left
  // out by foreignKeyFor, so their cells stay plain text boxes.
  const referencedColumns = useMemo(() => {
    const found: Record<string, boolean> = {};
    for (const col of columns) {
      if (foreignKeyFor(foreignKeys, col.name)) found[col.name.toLowerCase()] = true;
    }
    return found;
  }, [columns, foreignKeys]);

  // A new result - a reloaded card or a fresh query - has nothing in common with
  // the rows that were queued, so pending changes cannot carry over. The
  // selection is a position too, so it goes: without dropping it, 复制行/删除行
  // would act on whichever row moved into that slot.
  useEffect(() => {
    dispatchEditor({ type: 'editReset' });
    dispatchColumns({ type: 'columnReset' });
    dispatchFk({ type: 'fkReset' });
    // Sorting and filtering are views over the columns, not the rows, so a
    // reload keeps them; a name that no longer exists simply matches nothing.
  }, [result]);

  // The menu is anchored to the header cell, so it has to go away as soon as
  // the click or scroll that would detach it happens.
  useEffect(() => {
    if (!menu) return;
    const close = () => dispatchColumns({ type: 'menuClose' });
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close();
    };
    window.addEventListener('pointerdown', close);
    window.addEventListener('blur', close);
    window.addEventListener('resize', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('pointerdown', close);
      window.removeEventListener('blur', close);
      window.removeEventListener('resize', close);
      window.removeEventListener('keydown', onKey);
    };
  }, [menu]);

  // Same for the export menu: Escape and a click anywhere else close it, while
  // Tab walks straight into the items because they sit next to the button.
  useEffect(() => {
    if (!exportMenu) return;
    const close = () => dispatchColumns({ type: 'exportMenuClose' });
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close();
    };
    window.addEventListener('pointerdown', close);
    window.addEventListener('blur', close);
    window.addEventListener('resize', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('pointerdown', close);
      window.removeEventListener('blur', close);
      window.removeEventListener('resize', close);
      window.removeEventListener('keydown', onKey);
    };
  }, [exportMenu]);

  const changeSet = (): ChangeSet | null => {
    if (!editSource) return null;
    return {
      connId: editSource.connId,
      database: editSource.database,
      schema: editSource.schema,
      table: editSource.table,
      columns: columns.map(c => c.name),
      changes: toRowChanges(queue, rows),
    };
  };

  // The preview is rebuilt from the payload the apply would send, so the text on
  // screen is the text that will run - the server never takes SQL from here.
  useEffect(() => {
    if (!editSource || counts.total === 0) {
      dispatchEditor({ type: 'preview', statements: [], error: '' });
      return;
    }
    const set = changeSet();
    if (!set) return;
    let current = true;
    BuildChanges(set).then(
      list => {
        if (!current) return;
        dispatchEditor({ type: 'preview', statements: list, error: '' });
      },
      error => {
        if (!current) return;
        dispatchEditor({ type: 'preview', statements: [], error: (error as Error).message });
      },
    );
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queue, editSource, counts.total, result]);

  const commitEdit = (target: BodyRow, column: string, value: string) => {
    dispatchFk({ type: 'pickerClose' });
    if (target.fresh) {
      dispatchEditor({ type: 'editCommitFresh', id: target.fresh.id, column, value });
      return;
    }
    // The edit is taken against the original cell text, not the display text: a
    // value that reads the same but means something else must not be dropped.
    const index = columns.findIndex(c => c.name === column);
    dispatchEditor({
      type: 'editCommitFetched',
      index: target.index,
      column,
      value,
      original: index >= 0 ? cellText(rows[target.index]?.[index]) : '',
    });
  };

  const startEdit = (target: BodyRow, column: string, current: string) => {
    if (!editSource) return;
    dispatchEditor({ type: 'editBegin', key: target.key, column, value: current });
  };

  // Read the referenced table once per column and keep the answer, so reopening a
  // cell costs nothing. A failure lands on the cell, not the grid: the editor is
  // still usable as a plain text box.
  const readCandidates = (rowKey: string, column: string) => {
    if (!editSource || fkLists[column.toLowerCase()]) return;
    const cacheKey = column.toLowerCase();
    GetForeignKeyValues(editSource, column).then(
      list => dispatchFk({ type: 'listSet', key: cacheKey, value: list }),
      error => dispatchFk({ type: 'pickerError', rowKey, column, error: (error as Error).message }),
    );
  };

  // The arrow is the whole affordance: it opens the editor with the list next to
  // it, and the cell stays a text box, so a value outside the list can be typed.
  const togglePicker = (
    target: BodyRow,
    column: string,
    value: string,
    anchor: HTMLElement,
  ) => {
    if (!editSource) return;
    if (picker && picker.rowKey === target.key && picker.column === column) {
      dispatchFk({ type: 'pickerClose' });
      return;
    }
    // The arrow takes the click without moving focus, so an editor left open in
    // another cell has to be committed here - otherwise what was typed in it is
    // dropped when it is replaced.
    const movesCell = !editing || editing.key !== target.key || editing.column !== column;
    if (editing && movesCell) {
      const previous = body.find(row => row.key === editing.key);
      if (previous) commitEdit(previous, editing.column, draft);
    }
    if (movesCell) startEdit(target, column, value);
    const cell = (anchor.closest('td') ?? anchor) as HTMLElement;
    const rect = cell.getBoundingClientRect();
    const width = Math.max(rect.width, 180);
    dispatchFk({
      type: 'pickerOpen',
      picker: {
        rowKey: target.key,
        column,
        left: Math.max(4, Math.min(rect.left, window.innerWidth - width - 4)),
        top:
          window.innerHeight - rect.bottom - 8 < PICKER_HEIGHT
            ? Math.max(4, rect.top - PICKER_HEIGHT - 2)
            : rect.bottom + 2,
        width,
        error: '',
      },
    });
    readCandidates(target.key, column);
  };

  // The list as it stands for what has been typed. One definition feeds the panel
  // and the cell's arrow keys, so the highlight can never disagree with the rows
  // on screen.
  const pickerState = () => {
    const read = picker ? fkLists[picker.column.toLowerCase()] : undefined;
    const list = pickerList(fk);
    // A list opened on a cell that already holds a key starts on that row, so the
    // arrow keys go to a neighbour instead of jumping back onto the same value.
    const own = list.findIndex(candidate => candidate.value === draft);
    return {
      read,
      list,
      active: list.length ? (activeOption < 0 ? Math.max(own, 0) : highlightedIndex(activeOption, list.length)) : -1,
      loading: !!picker && !read && !picker.error,
    };
  };
  const pickerNow = picker ? pickerState() : null;

  // The list is anchored to the cell, so scrolling would leave it hanging over
  // other rows; it closes instead, the same way the header menu does.
  useEffect(() => {
    if (!picker) return;
    const scroller = scrollerRef.current;
    const close = () => dispatchFk({ type: 'pickerClose' });
    scroller?.addEventListener('scroll', close, { passive: true });
    return () => scroller?.removeEventListener('scroll', close);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picker]);

  // Arrow keys move the highlight past the edges of the visible part of the list.
  useEffect(() => {
    if (!picker) return;
    const active = pickerState().active;
    if (active >= 0) {
      document.getElementById(optionId(active))?.scrollIntoView({ block: 'nearest' });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picker, activeOption, fkQuery]);

  // Queued rows sit below the fetched ones, so the grid has to grow before the
  // newest is in view.
  const revealTail = () => {
    requestAnimationFrame(() => {
      const scroller = scrollerRef.current;
      scroller?.scrollTo({ top: scroller.scrollHeight, behavior: scrollBehavior() });
    });
  };

  // A queued row is blank on purpose: the columns left empty are the ones the
  // database fills in itself.
  const appendRow = () => {
    const id = rowSeq.current++;
    dispatchEditor({ type: 'rowAppend', id, column: columns[0]?.name ?? '' });
    revealTail();
  };

  // Copying a row is an insert seeded with its values, minus the key, so the
  // duplicate gets an identity of its own instead of colliding with the source.
  // What is on screen is what gets copied, an unapplied cell edit included.
  const duplicateRow = () => {
    if (selected === null) return;
    const id = rowSeq.current++;
    const shown = columns.map((col, i) => queue.updates[selected]?.[col.name] ?? rows[selected]?.[i] ?? null);
    dispatchEditor({ type: 'rowDuplicate', id, values: copyValues(shown, columns, keyNames) });
    revealTail();
  };

  // The delete mark is a toggle, so the same button undoes itself.
  const toggleSelectedDelete = () => {
    if (selected === null) return;
    dispatchEditor({ type: 'deleteToggle', index: selected });
  };

  const applyNow = async () => {
    const set = changeSet();
    if (!set) return;
    dispatchEditor({ type: 'applyStart' });
    try {
      await ApplyChanges(set);
      dispatchEditor({ type: 'applyResult', ok: true, error: '' });
      onApplied?.();
    } catch (error) {
      dispatchEditor({ type: 'applyResult', ok: false, error: (error as Error).message });
    }
  };

  const matches = useMemo(() => {
    const needle = locate.trim().toLowerCase();
    if (!needle) return [];
    return columns
      .map((_, i) => i)
      .filter(i => {
        const col = columns[i];
        return (
          col.name.toLowerCase().includes(needle) ||
          (comments?.[col.name.toLowerCase()] ?? '').toLowerCase().includes(needle)
        );
      });
  }, [locate, columns, comments]);

  const activeColumn = matches.length ? matches[Math.min(activeMatch, matches.length - 1)] : -1;

  // Typing a column name walks the grid to it instead of dragging the user
  // through a horizontal scroll.
  useEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller || activeColumn < 0) return;
    const start = ROW_NUMBER_WIDTH + widths.slice(0, activeColumn).reduce((sum, w) => sum + w, 0);
    const end = start + widths[activeColumn];
    if (start < scroller.scrollLeft || end > scroller.scrollLeft + scroller.clientWidth) {
      scroller.scrollTo({ left: Math.max(0, start - 12), behavior: scrollBehavior() });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeColumn]);

  if (result.error) {
    return <ErrorBanner message={result.error} onDismiss={onDismissError} />;
  }

  const setWidth = (index: number, name: string, next: number) =>
    dispatchColumns({
      type: 'widthSet',
      name,
      width: Math.min(MANUAL_MAX_WIDTH, Math.max(MANUAL_MIN_WIDTH, next)),
    });

  const resetWidth = (name: string) => dispatchColumns({ type: 'widthReset', name });

  const startColumnResize = (e: React.PointerEvent, index: number, name: string) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startWidth = widths[index];
    const move = (event: PointerEvent) => {
      const next = Math.min(
        MANUAL_MAX_WIDTH,
        Math.max(MANUAL_MIN_WIDTH, startWidth + event.clientX - startX),
      );
      dispatchColumns({ type: 'widthSet', name, width: next });
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const exportAs = async (format: ExportFormat) => {
    dispatchColumns({ type: 'exportMenuClose' });
    const stem = (exportName || 'result').replace(/[\\/:*?"<>|]+/g, '_');
    // What is on screen is what gets exported, sorting and filter included.
    const visible = view.map(i => rows[i]);
    if (format === 'csv') {
      download(`${stem}.csv`, 'text/csv;charset=utf-8', toCSV(columns, visible));
      return;
    }
    if (format === 'json') {
      download(`${stem}.json`, 'application/json', toJSON(columns, visible));
      return;
    }
    if (format === 'markdown') {
      download(`${stem}.md`, 'text/markdown;charset=utf-8', toMarkdown(columns, visible));
      return;
    }
    if (format === 'xlsx') {
      try {
        // The column types came from the database, which is what lets the file
        // hold real numbers and dates instead of text like CSV does.
        const workbook = await ExportWorkbook(columns, visible);
        downloadBlob(`${stem}.xlsx`, workbook);
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      }
      return;
    }
    if (!editSource) return;
    try {
      const script = await ExportInsertStatements(
        editSource,
        columns.map(c => c.name),
        visible,
      );
      download(`${stem}.sql`, 'application/sql;charset=utf-8', script);
    } catch (error) {
      toast.error((error as Error).message, { duration: 8000 });
    }
  };

  // The same script, straight to the clipboard - which is how an INSERT usually
  // gets into another window without a file in between.
  const copyInserts = async () => {
    dispatchColumns({ type: 'exportMenuClose' });
    if (!editSource) return;
    try {
      const script = await ExportInsertStatements(
        editSource,
        columns.map(c => c.name),
        view.map(i => rows[i]),
      );
      if (await copyText(script)) toast.success('INSERT 语句已复制到剪贴板');
      else toast.error('复制失败，请改用导出 SQL 文件');
    } catch (error) {
      toast.error((error as Error).message, { duration: 8000 });
    }
  };

  const openMenu = (e: React.MouseEvent, column: string) => {
    e.preventDefault();
    dispatchColumns({ type: 'menuOpen', x: e.clientX, y: e.clientY, column });
  };

  const setColumnFilter = (column: string, filter: ColumnFilter | null) => {
    dispatchColumns({ type: 'filterSet', column, filter });
  };

  const pickFilter = (column: string, op: FilterOp) => {
    dispatchColumns({ type: 'menuPickOp', op });
  };

  const applyFilterDraft = (column: string, op: FilterOp) => {
    setColumnFilter(column, { op, value: filterDraft });
    dispatchColumns({ type: 'menuClose' });
  };

  const toggleSort = (column: string, dir: 'asc' | 'desc') => {
    dispatchColumns({ type: 'sortToggle', column, dir });
    dispatchColumns({ type: 'menuClose' });
  };

  const clearViews = () => dispatchColumns({ type: 'viewsClear' });

  const filterCount = Object.keys(filters).length;
  const isFiltered = filterCount > 0 || !!sort;

  // The queue holds three kinds of change, and they read differently in a
  // summary, so spell out only the parts that are actually waiting.
  const pendingLabel = [
    counts.updates ? `${counts.updates} 处改动` : '',
    counts.inserts ? `${counts.inserts} 行新增` : '',
    counts.deletes ? `${counts.deletes} 行删除` : '',
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <div className="flex-1 flex flex-col min-h-0 overflow-hidden bg-canvas">
      <div className="flex items-center gap-2 px-2 py-1 bg-panel border-b border-line shrink-0">
        <div className="relative">
          <FiSearch
            size={11}
            className="absolute left-2 top-1/2 -translate-y-1/2 text-muted pointer-events-none"
          />
          <input
            value={locate}
            onChange={e => dispatchColumns({ type: 'locateType', value: e.target.value })}
            onKeyDown={e => {
              if (e.key === 'Escape') dispatchColumns({ type: 'locateClear' });
              if (e.key === 'Enter' && matches.length) {
                dispatchColumns({ type: 'locateNext', total: matches.length });
              }
            }}
            placeholder="定位列"
            aria-label="定位到指定列"
            className="w-44 bg-canvas border border-line rounded pl-6 pr-6 py-0.5 text-2xs text-ink placeholder-muted focus:outline-none focus:border-accent"
          />
          {locate && (
            <button
              className="absolute right-1 top-1/2 -translate-y-1/2 flex items-center justify-center w-5 h-5 text-muted hover:text-ink"
              onClick={() => dispatchColumns({ type: 'locateClear' })}
              title="清空"
              aria-label="清空定位列的搜索词"
            >
              <FiX size={11} />
            </button>
          )}
        </div>
        {locate && (
          <span className="text-2xs text-muted">
            {matches.length ? `${Math.min(activeMatch + 1, matches.length)} / ${matches.length}` : '无匹配列'}
          </span>
        )}

        {isFiltered && (
          <button
            className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs bg-raised text-secondary hover:text-ink"
            onClick={clearViews}
            title="清除排序与所有列筛选"
          >
            <FiXCircle size={11} />
            {sort ? `${sort.column} ${sort.dir === 'asc' ? '升序' : '降序'}` : ''}
            {sort && filterCount ? ' · ' : ''}
            {filterCount ? `${filterCount} 个筛选` : ''}
          </button>
        )}

        {editSource && (
          <div className="flex items-center gap-1 pl-2 border-l border-line">
            <button
              className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
              onClick={appendRow}
              title="新增一行：只有填过的列会写入，其余列交给数据库默认值"
            >
              <FiPlus size={11} />
              新增行
            </button>
            <button
              className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised disabled:opacity-40 disabled:cursor-not-allowed"
              onClick={duplicateRow}
              disabled={selected === null}
              title="复制选中的行：值一起带过来，主键留空交给数据库"
            >
              <FiCopy size={11} />
              复制行
            </button>
            <button
              className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs disabled:opacity-40 disabled:cursor-not-allowed ${
                selected !== null && queue.deletes.includes(selected)
                  ? 'text-warning hover:text-ink hover:bg-raised'
                  : 'text-secondary hover:text-danger hover:bg-raised'
              }`}
              onClick={toggleSelectedDelete}
              disabled={selected === null}
              title={
                selected !== null && queue.deletes.includes(selected)
                  ? '撤销这条删除标记'
                  : '标记删除选中的行，应用前可以撤销'
              }
            >
              <FiTrash2 size={11} />
              {selected !== null && queue.deletes.includes(selected) ? '撤销删除' : '删除行'}
            </button>
          </div>
        )}

        <div className="ml-auto relative">
          <button
            className="flex items-center gap-1 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
            aria-haspopup="true"
            aria-expanded={!!exportMenu}
            title="导出当前视图：CSV / JSON / Markdown / SQL"
            onPointerDown={e => e.stopPropagation()}
            onClick={e => {
              const rect = e.currentTarget.getBoundingClientRect();
              if (exportMenu) dispatchColumns({ type: 'exportMenuClose' });
              else dispatchColumns({ type: 'exportMenuOpen', left: rect.left, top: rect.bottom + 2 });
            }}
          >
            <FiDownload size={11} />
            导出
            <FiChevronDown size={10} aria-hidden />
          </button>
          {exportMenu && (
            <div
              className="fixed z-50 w-[190px] bg-panel border border-line-strong rounded-md shadow-xl py-1 text-[0.75rem]"
              aria-label="导出格式"
              style={{
                left: Math.max(4, Math.min(exportMenu.left, window.innerWidth - 198)),
                top: Math.max(4, Math.min(exportMenu.top, window.innerHeight - 240)),
              }}
              onPointerDown={e => e.stopPropagation()}
            >
              <MenuItem label="CSV" onClick={() => exportAs('csv')} />
              <MenuItem label="JSON" onClick={() => exportAs('json')} />
              <MenuItem label="Markdown 表格" onClick={() => exportAs('markdown')} />
              <MenuItem label="Excel（XLSX）" onClick={() => exportAs('xlsx')} />
              <div className="my-1 border-t border-line-soft" />
              <MenuItem label="SQL（INSERT 文件）" disabled={!editSource} onClick={() => exportAs('sql')} />
              <MenuItem label="复制为 INSERT" disabled={!editSource} onClick={copyInserts} />
              {!editSource && (
                <div className="px-2.5 pt-1 text-2xs text-muted leading-snug">
                  当前结果没有来源表（视图或自定义查询），生成不了 INSERT
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      <div ref={scrollerRef} className="flex-1 min-h-0 overflow-auto">
        <table className="border-separate border-spacing-0 text-[0.8125rem]">
          <thead>
            <tr>
              <th
                className="sticky top-0 left-0 z-30 bg-panel border-b border-r border-line px-2 py-1 text-right text-2xs font-normal text-muted select-none"
                style={{ width: ROW_NUMBER_WIDTH, minWidth: ROW_NUMBER_WIDTH }}
              >
                #
              </th>
              {columns.map((col, i) => {
                const comment = showComments ? comments?.[col.name.toLowerCase()] : undefined;
                const highlight =
                  i === activeColumn ? 'bg-accent/40' : matches.includes(i) ? 'bg-accent/15' : 'bg-panel';
                const sorted = sort?.column === col.name ? sort.dir : null;
                const filtered = filters[col.name];
                const hint = [
                  `${col.name} · ${col.type}`,
                  comment,
                  sorted ? `${sorted === 'asc' ? '升序' : '降序'}排序中` : '',
                  filtered
                    ? `筛选：${col.name} ${filterLabels[filtered.op]}${
                        opNeedsValue(filtered.op) ? ` ${filtered.value}` : ''
                      }`
                    : '',
                  '右键打开排序/筛选菜单',
                ]
                  .filter(Boolean)
                  .join('\n');
                return (
                  <th
                    key={i}
                    title={hint}
                    aria-sort={sorted ? (sorted === 'asc' ? 'ascending' : 'descending') : 'none'}
                    onContextMenu={e => openMenu(e, col.name)}
                    className={`sticky top-0 z-20 border-b border-r border-line px-2.5 py-1 text-left font-normal align-top ${highlight}`}
                    style={{ width: widths[i], minWidth: widths[i], maxWidth: widths[i] }}
                  >
                    <div className="flex items-center gap-1.5 truncate">
                      <span className="text-ink font-medium min-w-[2ch] truncate">{col.name}</span>
                      {sorted &&
                        (sorted === 'asc' ? (
                          <FiArrowUp size={11} aria-hidden className="shrink-0 text-accent" />
                        ) : (
                          <FiArrowDown size={11} aria-hidden className="shrink-0 text-accent" />
                        ))}
                      {filtered && <FiFilter size={10} aria-hidden className="shrink-0 text-warning" />}
                      <span className="text-2xs text-muted min-w-0 truncate">{col.type}</span>
                    </div>
                    {comment && (
                      <div className="text-2xs leading-tight text-muted truncate whitespace-pre">
                        {comment}
                      </div>
                    )}
                    <div
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={`${col.name} 列宽`}
                      aria-valuenow={widths[i]}
                      aria-valuemin={MANUAL_MIN_WIDTH}
                      aria-valuemax={MANUAL_MAX_WIDTH}
                      tabIndex={0}
                      onKeyDown={e => {
                        // Keyboard sizing: the pointer handle is only 6px wide,
                        // so arrow keys are the reliable way to reach a width.
                        const step = e.shiftKey ? 32 : 8;
                        if (e.key === 'ArrowLeft') setWidth(i, col.name, widths[i] - step);
                        else if (e.key === 'ArrowRight') setWidth(i, col.name, widths[i] + step);
                        else if (e.key === 'Backspace' || e.key === 'Delete') resetWidth(col.name);
                        else return;
                        e.preventDefault();
                        e.stopPropagation();
                      }}
                      onPointerDown={e => startColumnResize(e, i, col.name)}
                      onDoubleClick={() => resetWidth(col.name)}
                      className="absolute top-0 right-0 h-full w-1.5 cursor-col-resize hover:bg-accent/70 focus-visible:bg-accent/70"
                      title="拖动调整列宽，双击恢复自动宽度；聚焦后方向键微调"
                    />
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {body.map((target, pos) => {
              const { index, fresh } = target;
              const row = fresh ? [] : rows[index] ?? [];
              const deleting = !fresh && queue.deletes.includes(index);
              const isSelected = !fresh && selected === index;
              const rowBg = deleting
                ? 'bg-danger/15'
                : fresh
                  ? 'bg-success/10'
                  : isSelected
                    ? 'bg-accent/25'
                    : pos % 2
                      ? 'bg-row-alt'
                      : 'bg-canvas';
              return (
                <tr
                  key={target.key}
                  className={`${rowBg} hover:bg-raised/40 cursor-default ${
                    deleting ? 'line-through opacity-70' : ''
                  }`}
                  onClick={e => {
                    if (!fresh) dispatchEditor({ type: 'editSelect', index });
                  }}
                >
                  <td
                    className={`${rowBg} sticky left-0 z-10 border-b border-r border-line-soft px-2 py-0.5 text-right text-2xs ${
                      deleting ? 'text-danger' : 'text-muted'
                    } tabular-nums select-none`}
                    style={{ width: ROW_NUMBER_WIDTH, minWidth: ROW_NUMBER_WIDTH }}
                  >
                    {fresh ? (
                      <span className="flex items-center justify-end gap-1 leading-none">
                        <span className="text-2xs font-normal text-success">新增</span>
                        <button
                          className="flex items-center justify-center w-5 h-5 text-danger hover:text-ink"
                          title="撤销这条新增，不写回数据库"
                          aria-label="撤销这条新增"
                          onClick={e => {
                            e.stopPropagation();
                            dispatchEditor({ type: 'rowDrop', id: fresh.id });
                          }}
                        >
                          <FiX size={11} />
                        </button>
                      </span>
                    ) : deleting ? (
                      <span className="flex items-center justify-end gap-1 leading-none">
                        <span className="text-2xs font-normal text-danger">删除</span>
                        {pos + 1}
                      </span>
                    ) : (
                      pos + 1
                    )}
                  </td>
                  {columns.map((col, c) => {
                    const value = fresh ? fresh.values[col.name] ?? null : row[c];
                    const text = cellText(value);
                    const isNull = value === null || value === undefined;
                    const startValue = isNull ? '' : text;
                    const pending = fresh ? undefined : queue.updates[index]?.[col.name];
                    // A cell with an unapplied edit shows the new value, highlighted;
                    // the original stays in the tooltip so nothing is hidden.
                    const { text: shown, isNull: shownNull } = displayCell(value, pending);
                    // A queued row sends only the columns it holds, so an empty cell
                    // there means "say nothing" - not NULL.
                    const omitted = fresh && isNull;
                    const isEditing = editing?.key === target.key && editing?.column === col.name;
                    const editable = !!editSource && !deleting;
                    // A cell that holds a reference gets an arrow next to its value:
                    // the text box stays, so a value outside the list can be typed.
                    const hasFk = editable && referencedColumns[col.name.toLowerCase()] === true;
                    const listIsHere =
                      !!pickerNow && picker!.rowKey === target.key && picker!.column === col.name;
                    return (
                      <td
                        key={c}
                        className={`group/cell relative border-b border-r border-line-soft px-2.5 py-0.5 text-ink ${
                          pending !== undefined ? 'bg-warning/20' : ''
                        }`}
                        style={{ width: widths[c], minWidth: widths[c], maxWidth: widths[c] }}
                        onClick={() => {
                          // A queued row is still being filled in, so one click
                          // is enough to start typing in it.
                          if (fresh && editable) startEdit(target, col.name, startValue);
                        }}
                        onDoubleClick={() => {
                          if (!fresh && editable) startEdit(target, col.name, pending ?? startValue);
                        }}
                        title={
                          fresh
                            ? pending === undefined && isNull
                              ? `新增行 · ${col.name} · 留空则用数据库默认值`
                              : `改动未应用 · ${col.name}`
                            : deleting
                              ? '已标记删除 · 用工具条的“撤销删除”恢复'
                              : pending !== undefined
                                ? `改动未应用 · 原值：${isNull ? 'NULL' : text || '（空）'}`
                                : editSource
                                  ? `双击编辑${hasFk ? ' · 右侧箭头列出引用表的值' : ''} · ${text || '（空）'}`
                                  : text
                        }
                      >
                        {pending !== undefined && (
                          // Shape as well as colour: an unapplied edit has to
                          // read as edited without relying on the tint.
                          <span aria-hidden className="absolute right-0 top-0 text-[7px] leading-none text-warning">
                            ▲
                          </span>
                        )}
                        {isEditing ? (
                          <input
                            autoFocus
                            aria-label={`编辑 ${col.name} 列的值`}
                            aria-expanded={hasFk ? listIsHere : undefined}
                            aria-activedescendant={
                              listIsHere && pickerNow!.active >= 0
                                ? optionId(pickerNow!.active)
                                : undefined
                            }
                            value={draft}
                            onChange={e => {
                              dispatchEditor({ type: 'editDraft', value: e.target.value });
                              // Typing narrows the list and puts the highlight back
                              // on its top row.
                              dispatchFk({ type: 'queryType', value: e.target.value });
                            }}
                            onBlur={() => commitEdit(target, col.name, draft)}
                            onKeyDown={e => {
                              if (listIsHere) {
                                const open = pickerState();
                                if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                                  e.preventDefault();
                                  e.stopPropagation();
                                  if (open.list.length) {
                                    dispatchFk({
                                      type: 'activeMove',
                                      active: cycleActive(
                                        open.active,
                                        e.key === 'ArrowDown' ? 1 : -1,
                                        open.list.length,
                                      ),
                                    });
                                  }
                                  return;
                                }
                                if (e.key === 'Enter' && open.active >= 0) {
                                  // Enter takes the highlighted key rather than the
                                  // text typed, which is the point of the list.
                                  e.preventDefault();
                                  commitEdit(target, col.name, open.list[open.active].value);
                                  return;
                                }
                                if (e.key === 'Escape') {
                                  // The list closes first; a second Escape cancels
                                  // the edit itself.
                                  e.preventDefault();
                                  e.stopPropagation();
                                  dispatchFk({ type: 'pickerClose' });
                                  return;
                                }
                              }
                              if (e.key === 'Enter') {
                                e.preventDefault();
                                commitEdit(target, col.name, draft);
                              }
                              if (e.key === 'Escape') {
                                // Cancel this edit only: a value already pending
                                // stays pending rather than being wiped to the original.
                                e.preventDefault();
                                dispatchEditor({ type: 'editClose' });
                                dispatchFk({ type: 'pickerClose' });
                              }
                            }}
                            className={`w-full bg-canvas border border-accent rounded py-0 px-1 text-[0.8125rem] text-ink focus:outline-none ${
                              hasFk ? 'pr-5' : ''
                            }`}
                          />
                        ) : (
                          <div
                            className={`truncate whitespace-pre ${
                              shownNull && !omitted ? 'italic text-muted' : ''
                            } ${typeof value === 'number' ? 'tabular-nums' : ''} ${
                              hasFk ? 'pr-4' : ''
                            }`}
                          >
                            {omitted ? '' : shownNull ? 'NULL' : shown}
                          </div>
                        )}
                        {hasFk && (
                          <button
                            aria-label={`选择 ${col.name} 的引用值`}
                            aria-expanded={listIsHere}
                            title={isEditing ? '显示或隐藏候选值' : '打开编辑器并列出引用表的值'}
                            className={`absolute right-1 top-1/2 -translate-y-1/2 flex items-center justify-center w-5 h-5 rounded text-muted hover:text-ink hover:bg-raised focus-visible:text-ink focus-visible:bg-raised ${
                              listIsHere
                                ? 'opacity-100'
                                : 'opacity-0 group-hover/cell:opacity-100 focus-visible:opacity-100'
                            }`}
                            onMouseDown={e => e.preventDefault()}
                            onClick={e => {
                              e.stopPropagation();
                              togglePicker(
                                target,
                                col.name,
                                isEditing ? draft : pending ?? startValue,
                                e.currentTarget,
                              );
                            }}
                          >
                            <FiChevronDown size={11} aria-hidden />
                          </button>
                        )}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>

        {body.length === 0 && rows.length === 0 && (
          <div className="flex items-center justify-center h-24 text-muted text-sm">
            没有返回数据
          </div>
        )}
        {body.length === 0 && rows.length > 0 && view.length === 0 && (
          <div className="flex items-center justify-center h-24 text-muted text-sm">
            筛选后没有匹配的行
          </div>
        )}
      </div>

      {picker && pickerNow && (
        <FKPickerPanel
          left={picker.left}
          top={picker.top}
          width={picker.width}
          column={picker.column}
          error={picker.error}
          pickerState={pickerNow}
          onPick={(value) => {
            const row = body.find(target => target.key === picker.rowKey);
            row && commitEdit(row, picker.column, value);
          }}
        />
      )}

      {menu && (
        <FilterMenu
          x={menu.x}
          y={menu.y}
          column={menu.column}
          op={menu.op}
          filter={filters[menu.column]}
          sorted={sort?.column === menu.column ? sort.dir : null}
          filterDraft={filterDraft}
          onFilterDraftChange={(value) => dispatchColumns({ type: 'draftType', value })}
          onApplyFilter={(op) => applyFilterDraft(menu.column, op)}
          onSort={(dir) => toggleSort(menu.column, dir)}
          onClearSort={() => {
            dispatchColumns({ type: 'sortClear' });
            dispatchColumns({ type: 'menuClose' });
          }}
          onPickFilter={(op) => pickFilter(menu.column, op)}
          onClearFilter={() => {
            setColumnFilter(menu.column, null);
            dispatchColumns({ type: 'menuClose' });
          }}
          onClose={() => dispatchColumns({ type: 'menuClose' })}
        />
      )}

      {counts.total > 0 && (
        <div className="flex items-center gap-2 px-3 py-1 bg-warning/12 border-t border-warning/45 text-2xs shrink-0">
          <span className="text-ink whitespace-nowrap">{pendingLabel}未应用</span>
          {planError ? (
            <span className="text-danger truncate" title={planError}>
              {planError}
            </span>
          ) : (
            <span className="text-muted">{statements.length} 条语句待执行</span>
          )}
          <div className="ml-auto flex items-center gap-1">
            <button
              className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
              onClick={() => dispatchEditor({ type: 'queueClear' })}
              title="丢弃全部改动，不写回数据库"
            >
              放弃
            </button>
            <button
              className="px-2 py-0.5 rounded text-2xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50 disabled:cursor-not-allowed"
              onClick={() => dispatchEditor({ type: 'reviewOpen' })}
              disabled={!!planError || statements.length === 0}
              title="先看清要执行的语句，再确认应用"
            >
              应用更改
            </button>
          </div>
        </div>
      )}

      {review && (
        <ReviewDialog
          targetName={targetName}
          pendingLabel={pendingLabel}
          statements={statements}
          counts={counts}
          applying={applying}
          applyError={applyError}
          onApply={applyNow}
          onClose={() => dispatchEditor({ type: 'reviewClose' })}
        />
      )}

      <div className="flex items-center gap-3 px-3 py-1 bg-panel border-t border-line text-2xs text-muted shrink-0">
        <span>
          {isFiltered ? `显示 ${view.length} / ${result.rowCount} 行` : `${result.rowCount} 行`}
          {result.truncated && (
            <span className="text-warning" title="结果达到行数上限，后面还有数据。调高设置里的行数上限可看到更多">
              {' '}· 已截断（还有更多行）
            </span>
          )}
          {counts.inserts > 0 && <span className="text-success"> + {counts.inserts} 新增</span>}
          {counts.deletes > 0 && <span className="text-danger"> − {counts.deletes} 删除</span>}
        </span>
        {result.affectedRows > 0 && <span>影响 {result.affectedRows} 行</span>}
        <span>{result.executionTime.toFixed(3)}s</span>
        {body.length > 0 &&
          (editSource ? (
            <span title={`改动会写回 ${targetName}`}>
              可编辑 · 双击改单元格，工具条可新增 / 复制 / 删除行
            </span>
          ) : (
            <span title="只有 select * 取自单个表的结果能改回数据库">只读</span>
          ))}
        {selected !== null && view.includes(selected) && (
          <span className="ml-auto truncate">
            第 {view.indexOf(selected) + 1} 行 ·{' '}
            {columns.map((c, i) => `${c.name}=${cellText(rows[selected][i]) || 'NULL'}`).join('  ')}
          </span>
        )}
      </div>
    </div>
  );
};

export default DataGrid;
