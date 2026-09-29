import React, { useEffect, useMemo, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import {
  FiArrowDown,
  FiArrowUp,
  FiCheck,
  FiChevronDown,
  FiCopy,
  FiDownload,
  FiFilter,
  FiLoader,
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
  ForeignKeyCandidates,
  GetColumns,
  GetForeignKeyValues,
  GetTableForeignKeys,
} from '../api';
import { copyText } from '../clipboard';
import { scrollBehavior } from '../motion';
import { ChangeSet, ForeignKeyInfo, QueryResult, TableRef } from '../types';
import {
  addRow,
  buildView,
  cellText,
  ChangeQueue,
  ColumnFilter,
  copyValues,
  cycleActive,
  describeCandidate,
  displayCell,
  dropRow,
  emptyQueue,
  filterCandidates,
  FilterOp,
  foreignKeyFor,
  NewRow,
  queueCounts,
  setRowValue,
  SortState,
  toCSV,
  toJSON,
  toMarkdown,
  toRowChanges,
  toggleDelete,
  withEdit,
} from '../gridView';
import ErrorBanner from './ErrorBanner';

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

// Narrow enough for dense tables, wide enough that a short column name and its
// type label both stay readable in the header.
const MIN_WIDTH = 88;
const MAX_WIDTH = 360;
const MANUAL_MIN_WIDTH = 40;
const MANUAL_MAX_WIDTH = 1600;
const ROW_NUMBER_WIDTH = 64;

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

// Tall enough to scan a few dozen keys, short enough that the cell it belongs to
// stays on screen.
const PICKER_HEIGHT = 264;

// Only one list is ever open, so its rows can be numbered from the position; the
// editor points at the highlighted one with aria-activedescendant.
const optionId = (index: number) => `fk-option-${index}`;

// Sorting and filtering work on the rows already on screen; the grid never
// re-queries, so what is ordered here is exactly what was read (see gridView.ts).
const filterLabels: Record<FilterOp, string> = {
  eq: '等于',
  ne: '不等于',
  contains: '包含',
  null: '为空',
  notNull: '不为空',
};

const opNeedsValue = (op: FilterOp) => op === 'eq' || op === 'ne' || op === 'contains';

// Widths are sampled rather than auto-laid-out so the grid keeps a stable,
// column-first look instead of stretching to the viewport.
const useColumnWidths = (result: QueryResult) =>
  useMemo(() => {
    const columns = result.columns ?? [];
    const rows = result.rows ?? [];
    return columns.map((col, i) => {
      let longest = col.name.length * 7 + 28;
      for (let r = 0; r < Math.min(rows.length, 50); r++) {
        const text = cellText(rows[r][i]);
        if (text) longest = Math.max(longest, Math.min(text.length, 46) * 7 + 24);
      }
      return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, longest));
    });
  }, [result]);

const downloadBlob = (name: string, blob: Blob) => {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
};

const download = (name: string, mime: string, text: string) =>
  downloadBlob(name, new Blob([text], { type: mime }));

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

const MenuItem: React.FC<{
  label: string;
  icon?: React.ReactNode;
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
}> = ({ label, icon, active, disabled, onClick }) => (
  <button
    className={`w-full flex items-center gap-2 px-2.5 py-1 text-left hover:bg-raised disabled:opacity-40 ${
      active ? 'text-accent' : 'text-ink'
    }`}
    onClick={onClick}
    disabled={disabled}
  >
    <span className="w-3.5 shrink-0 flex items-center justify-center">
      {active ? <FiCheck size={11} /> : icon}
    </span>
    <span className="truncate">{label}</span>
  </button>
);

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
  const [selected, setSelected] = useState<number | null>(null);
  const [overrides, setOverrides] = useState<Record<string, number>>({});
  const [locate, setLocate] = useState('');
  const [activeMatch, setActiveMatch] = useState(0);
  const scrollerRef = useRef<HTMLDivElement>(null);

  // Everything waiting for the database: edited cells, rows to insert, rows to
  // delete. Nothing reaches the server until the user applies the queue.
  const [queue, setQueue] = useState<ChangeQueue>(emptyQueue);
  // Ids for queued new rows, so typing in one cannot renumber another.
  const rowSeq = useRef(1);
  // Which cell holds the open editor: `u<row>` for a fetched row, `n<id>` for a
  // queued one, since the two are numbered independently.
  const [editing, setEditing] = useState<{ key: string; column: string } | null>(null);
  // The table's own key columns, read from the catalog: a duplicate leaves them
  // out, so the copy gets an identity instead of colliding with its source.
  const [keyNames, setKeyNames] = useState<string[]>([]);
  // Constraints of the edited table, so a cell knows it holds a reference and can
  // offer a list instead of a bare text box.
  const [foreignKeys, setForeignKeys] = useState<ForeignKeyInfo[]>([]);
  // Candidate values per column, kept after the first read so reopening a cell
  // costs nothing.
  const [fkLists, setFkLists] = useState<Record<string, ForeignKeyCandidates>>({});
  const [picker, setPicker] = useState<FKPicker | null>(null);
  // What has been typed since the list opened. The cell's own value stays in the
  // editor, so the list starts full and narrows only once typing starts.
  const [fkQuery, setFkQuery] = useState('');
  // -1 until the arrow keys or the list itself place the highlight somewhere.
  const [activeOption, setActiveOption] = useState(-1);
  const [draft, setDraft] = useState('');
  const [statements, setStatements] = useState<string[]>([]);
  const [planError, setPlanError] = useState('');
  const [review, setReview] = useState(false);
  const [applying, setApplying] = useState(false);
  const [applyError, setApplyError] = useState('');

  const [sort, setSort] = useState<SortState | null>(null);
  const [filters, setFilters] = useState<Record<string, ColumnFilter>>({});
  const [menu, setMenu] = useState<{ x: number; y: number; column: string; op: FilterOp | null } | null>(
    null,
  );
  const [filterDraft, setFilterDraft] = useState('');
  // Where the export menu hangs from the toolbar button. It is rendered next to
  // the button, so Tab walks straight into the items.
  const [exportMenu, setExportMenu] = useState<{ left: number; top: number } | null>(null);

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
      setKeyNames([]);
      setForeignKeys([]);
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
    setFkLists({});
    setForeignKeys([]);
    GetTableForeignKeys(editSource).then(
      list => {
        if (current) setForeignKeys(list);
      },
      () => {
        if (current) setForeignKeys([]);
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
    setQueue(emptyQueue());
    setSelected(null);
    setEditing(null);
    setReview(false);
    setApplyError('');
    setMenu(null);
    setPicker(null);
    setExportMenu(null);
    // Sorting and filtering are views over the columns, not the rows, so a
    // reload keeps them; a name that no longer exists simply matches nothing.
  }, [result]);

  // The menu is anchored to the header cell, so it has to go away as soon as
  // the click or scroll that would detach it happens.
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
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
    const close = () => setExportMenu(null);
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
      setStatements([]);
      setPlanError('');
      return;
    }
    const set = changeSet();
    if (!set) return;
    let current = true;
    BuildChanges(set).then(
      list => {
        if (!current) return;
        setStatements(list);
        setPlanError('');
      },
      error => {
        if (!current) return;
        setStatements([]);
        setPlanError((error as Error).message);
      },
    );
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queue, editSource, counts.total, result]);

  const commitEdit = (target: BodyRow, column: string, value: string) => {
    setEditing(null);
    setPicker(null);
    if (target.fresh) {
      const fresh = target.fresh;
      setQueue(current => setRowValue(current, fresh.id, column, value));
      return;
    }
    const index = columns.findIndex(c => c.name === column);
    const original = index >= 0 ? cellText(rows[target.index]?.[index]) : '';
    setQueue(current => ({
      ...current,
      updates: withEdit(current.updates, target.index, column, original, value),
    }));
  };

  const startEdit = (target: BodyRow, column: string, current: string) => {
    if (!editSource) return;
    setEditing({ key: target.key, column });
    setDraft(current);
  };

  // The rows the list shows for the text typed into the cell.
  const shownCandidates = (candidate: ForeignKeyCandidates | undefined) =>
    candidate ? filterCandidates(candidate.values, fkQuery) : [];

  // Which row that leaves highlighted, clamped so narrowing the list cannot keep
  // a highlight on a row that has disappeared - Enter would take an unseen value.
  const highlightedRow = (length: number) =>
    length ? Math.max(0, Math.min(activeOption, length - 1)) : -1;

  // Read the referenced table once per column and keep the answer, so reopening a
  // cell costs nothing. A failure lands on the cell, not the grid: the editor is
  // still usable as a plain text box.
  const readCandidates = (rowKey: string, column: string) => {
    if (!editSource || fkLists[column.toLowerCase()]) return;
    const cacheKey = column.toLowerCase();
    GetForeignKeyValues(editSource, column).then(
      list =>
        setFkLists(current =>
          current[cacheKey] ? current : { ...current, [cacheKey]: list },
        ),
      error =>
        setPicker(current =>
          current && current.rowKey === rowKey && current.column === column
            ? { ...current, error: (error as Error).message }
            : current,
        ),
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
      setPicker(null);
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
    setPicker({
      rowKey: target.key,
      column,
      left: Math.max(4, Math.min(rect.left, window.innerWidth - width - 4)),
      top:
        window.innerHeight - rect.bottom - 8 < PICKER_HEIGHT
          ? Math.max(4, rect.top - PICKER_HEIGHT - 2)
          : rect.bottom + 2,
      width,
      error: '',
    });
    setActiveOption(-1);
    setFkQuery('');
    readCandidates(target.key, column);
  };

  // The list as it stands for what has been typed. One definition feeds the panel
  // and the cell's arrow keys, so the highlight can never disagree with the rows
  // on screen.
  const pickerState = () => {
    const read = picker ? fkLists[picker.column.toLowerCase()] : undefined;
    const list = shownCandidates(read);
    // A list opened on a cell that already holds a key starts on that row, so the
    // arrow keys go to a neighbour instead of jumping back onto the same value.
    const own = list.findIndex(candidate => candidate.value === draft);
    return {
      read,
      list,
      active: list.length ? (activeOption < 0 ? Math.max(own, 0) : highlightedRow(list.length)) : -1,
      loading: !!picker && !read && !picker.error,
    };
  };
  const pickerNow = picker ? pickerState() : null;

  // The list is anchored to the cell, so scrolling would leave it hanging over
  // other rows; it closes instead, the same way the header menu does.
  useEffect(() => {
    if (!picker) return;
    const scroller = scrollerRef.current;
    const close = () => setPicker(null);
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
    setQueue(current => addRow(current, id));
    const first = columns[0]?.name ?? '';
    setEditing({ key: `n${id}`, column: first });
    setDraft('');
    revealTail();
  };

  // Copying a row is an insert seeded with its values, minus the key, so the
  // duplicate gets an identity of its own instead of colliding with the source.
  // What is on screen is what gets copied, an unapplied cell edit included.
  const duplicateRow = () => {
    if (selected === null) return;
    const id = rowSeq.current++;
    const shown = columns.map((col, i) => queue.updates[selected]?.[col.name] ?? rows[selected]?.[i] ?? null);
    setQueue(current => addRow(current, id, copyValues(shown, columns, keyNames)));
    revealTail();
  };

  // The delete mark is a toggle, so the same button undoes itself.
  const toggleSelectedDelete = () => {
    if (selected === null) return;
    setQueue(current => toggleDelete(current, selected));
    setEditing(null);
  };

  const applyNow = async () => {
    const set = changeSet();
    if (!set) return;
    setApplying(true);
    setApplyError('');
    try {
      await ApplyChanges(set);
      setApplying(false);
      setReview(false);
      setQueue(emptyQueue());
      setStatements([]);
      onApplied?.();
    } catch (error) {
      setApplying(false);
      setApplyError((error as Error).message);
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
    setOverrides(current => ({
      ...current,
      [name]: Math.min(MANUAL_MAX_WIDTH, Math.max(MANUAL_MIN_WIDTH, next)),
    }));

  const resetWidth = (name: string) =>
    setOverrides(current => {
      const next = { ...current };
      delete next[name];
      return next;
    });

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
      setOverrides(current => ({ ...current, [name]: next }));
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const exportAs = async (format: ExportFormat) => {
    setExportMenu(null);
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
    setExportMenu(null);
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
    setFilterDraft(filters[column]?.value ?? '');
    setMenu({ x: e.clientX, y: e.clientY, column, op: null });
  };

  const setColumnFilter = (column: string, filter: ColumnFilter | null) => {
    setFilters(current => {
      const next = { ...current };
      if (filter) next[column] = filter;
      else delete next[column];
      return next;
    });
  };

  const pickFilter = (column: string, op: FilterOp) => {
    if (opNeedsValue(op)) {
      setMenu(current => (current ? { ...current, op } : current));
      return;
    }
    setColumnFilter(column, { op, value: '' });
    setMenu(null);
  };

  const applyFilterDraft = (column: string, op: FilterOp) => {
    setColumnFilter(column, { op, value: filterDraft });
    setMenu(null);
  };

  const toggleSort = (column: string, dir: 'asc' | 'desc') => {
    setSort(current => (current?.column === column && current.dir === dir ? null : { column, dir }));
    setMenu(null);
  };

  const clearViews = () => {
    setSort(null);
    setFilters({});
  };

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
            onChange={e => {
              setLocate(e.target.value);
              setActiveMatch(0);
            }}
            onKeyDown={e => {
              if (e.key === 'Escape') setLocate('');
              if (e.key === 'Enter' && matches.length) {
                setActiveMatch(m => (m + 1) % matches.length);
              }
            }}
            placeholder="定位列"
            aria-label="定位到指定列"
            className="w-44 bg-canvas border border-line rounded pl-6 pr-6 py-0.5 text-2xs text-ink placeholder-muted focus:outline-none focus:border-accent"
          />
          {locate && (
            <button
              className="absolute right-1 top-1/2 -translate-y-1/2 flex items-center justify-center w-5 h-5 text-muted hover:text-ink"
              onClick={() => setLocate('')}
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
              setExportMenu(current =>
                current ? null : { left: rect.left, top: rect.bottom + 2 },
              );
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
                  onClick={() => {
                    if (!fresh) setSelected(index);
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
                            setQueue(current => dropRow(current, fresh.id));
                            setEditing(ed => (ed?.key === target.key ? null : ed));
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
                              setDraft(e.target.value);
                              // Typing narrows the list and puts the highlight back
                              // on its top row.
                              setFkQuery(e.target.value);
                              setActiveOption(0);
                            }}
                            onBlur={() => commitEdit(target, col.name, draft)}
                            onKeyDown={e => {
                              if (listIsHere) {
                                const open = pickerState();
                                if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                                  e.preventDefault();
                                  e.stopPropagation();
                                  if (open.list.length) {
                                    setActiveOption(
                                      cycleActive(
                                        open.active,
                                        e.key === 'ArrowDown' ? 1 : -1,
                                        open.list.length,
                                      ),
                                    );
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
                                  setPicker(null);
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
                                setEditing(null);
                                setPicker(null);
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

      {picker &&
        pickerNow &&
        (() => {
          // The list belongs to a cell, and picking a value is that cell's edit.
          const row = body.find(target => target.key === picker.rowKey);
          const { read, list, active, loading } = pickerNow;
          return (
            <div
              className="fixed z-50 flex flex-col bg-panel border border-line-strong rounded-md shadow-xl text-[0.75rem] text-ink overflow-hidden"
              style={{
                left: picker.left,
                top: picker.top,
                width: picker.width,
                height: PICKER_HEIGHT,
              }}
              onMouseDown={e => {
                // Focus has to stay in the cell editor: losing it would commit the
                // edit before the click on a row lands. Cancelling mousedown does
                // that, and - unlike pointerdown - leaves the click itself alone.
                e.preventDefault();
                e.stopPropagation();
              }}
            >
              <div className="shrink-0 px-2 py-1 text-2xs text-muted border-b border-line-soft truncate">
                {read ? `引用 ${read.refTable}.${read.refColumn}` : picker.column}
                {read?.labelColumn ? ` · ${read.labelColumn}` : ''}
              </div>
              <div
                role="listbox"
                aria-label={`${picker.column} 的候选值`}
                className="flex-1 overflow-y-auto py-0.5"
              >
                {loading && (
                  <div className="flex items-center gap-1.5 px-2 py-1 text-muted">
                    <FiLoader size={11} aria-hidden className="animate-spin" />
                    读取候选值…
                  </div>
                )}
                {picker.error && <div className="px-2 py-1 text-danger break-all">{picker.error}</div>}
                {!loading && !picker.error && list.length === 0 && (
                  <div className="px-2 py-1 text-muted">没有匹配的值，可直接输入</div>
                )}
                {list.map((candidate, i) => (
                  <div
                    key={candidate.value}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === active}
                    className={`px-2 py-0.5 cursor-pointer truncate ${
                      i === active ? 'bg-accent/25' : 'hover:bg-raised'
                    }`}
                    title={describeCandidate(candidate)}
                    onClick={() => row && commitEdit(row, picker.column, candidate.value)}
                  >
                    {describeCandidate(candidate)}
                  </div>
                ))}
              </div>
              {read?.truncated && (
                <div className="shrink-0 px-2 py-1 text-2xs text-muted border-t border-line-soft">
                  引用表只读了前 {read.values.length} 条，其它值可直接输入
                </div>
              )}
            </div>
          );
        })()}

      {menu &&
        (() => {
          const { column, op } = menu;
          const sorted = sort?.column === column ? sort.dir : null;
          const current = filters[column];
          const valueEntry = op && opNeedsValue(op) ? op : null;
          return (
            <div
              className="fixed z-50 w-[210px] bg-panel border border-line-strong rounded-md shadow-xl py-1 text-[0.75rem]"
              style={{
                left: Math.max(4, Math.min(menu.x, window.innerWidth - 220)),
                top: Math.max(4, Math.min(menu.y, window.innerHeight - 260)),
              }}
              onPointerDown={e => e.stopPropagation()}
            >
              <div className="px-2.5 pb-1 text-2xs text-muted truncate border-b border-line-soft">
                {column}
              </div>

              {valueEntry ? (
                <div className="px-2.5 py-2">
                  <div className="text-2xs text-secondary mb-1">
                    {column} {filterLabels[valueEntry]} …
                  </div>
                  <input
                    autoFocus
                    aria-label={`筛选 ${column} 的值`}
                    value={filterDraft}
                    onChange={e => setFilterDraft(e.target.value)}
                    onKeyDown={e => {
                      if (e.key === 'Enter') applyFilterDraft(column, valueEntry);
                    }}
                    placeholder="用于筛选的值"
                    className="w-full bg-canvas border border-accent rounded px-1.5 py-0.5 text-[0.75rem] text-ink focus:outline-none"
                  />
                  <div className="flex items-center justify-end gap-1 mt-1.5">
                    <button
                      className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
                      onClick={() => setMenu(null)}
                    >
                      取消
                    </button>
                    <button
                      className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover"
                      onClick={() => applyFilterDraft(column, valueEntry)}
                    >
                      筛选
                    </button>
                  </div>
                </div>
              ) : (
                <>
                  <MenuItem
                    label="升序排序"
                    icon={<FiArrowUp size={11} />}
                    active={sorted === 'asc'}
                    onClick={() => toggleSort(column, 'asc')}
                  />
                  <MenuItem
                    label="降序排序"
                    icon={<FiArrowDown size={11} />}
                    active={sorted === 'desc'}
                    onClick={() => toggleSort(column, 'desc')}
                  />
                  <MenuItem
                    label="清除排序"
                    disabled={!sorted}
                    onClick={() => {
                      setSort(null);
                      setMenu(null);
                    }}
                  />
                  <div className="my-1 border-t border-line-soft" />
                  {(['eq', 'ne', 'contains', 'null', 'notNull'] as FilterOp[]).map(op => (
                    <MenuItem
                      key={op}
                      label={`筛选：${filterLabels[op]}`}
                      icon={<FiFilter size={10} />}
                      active={current?.op === op}
                      onClick={() => pickFilter(column, op)}
                    />
                  ))}
                  <MenuItem
                    label="清除该列筛选"
                    disabled={!current}
                    onClick={() => {
                      setColumnFilter(column, null);
                      setMenu(null);
                    }}
                  />
                </>
              )}
            </div>
          );
        })()}

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
              onClick={() => setQueue(emptyQueue())}
              title="丢弃全部改动，不写回数据库"
            >
              放弃
            </button>
            <button
              className="px-2 py-0.5 rounded text-2xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50 disabled:cursor-not-allowed"
              onClick={() => {
                setApplyError('');
                setReview(true);
              }}
              disabled={!!planError || statements.length === 0}
              title="先看清要执行的语句，再确认应用"
            >
              应用更改
            </button>
          </div>
        </div>
      )}

      {review && (
        <div
          className="fixed inset-0 z-50 bg-scrim/50 flex items-center justify-center p-6"
          onClick={() => {
            if (!applying) setReview(false);
          }}
        >
          <div
            className="bg-panel border border-line-strong rounded-lg w-full max-w-2xl shadow-xl"
            onClick={e => e.stopPropagation()}
          >
            <header className="flex items-center px-4 py-2.5 border-b border-line">
              <h2 className="text-sm font-semibold">应用 {pendingLabel}</h2>
              <button
                className="ml-auto flex items-center justify-center w-6 h-6 text-secondary hover:text-ink"
                onClick={() => setReview(false)}
                aria-label="关闭改动确认对话框"
                disabled={applying}
              >
                <FiX size={16} />
              </button>
            </header>

            <div className="px-4 py-3 space-y-2">
              <p className="text-xs text-secondary leading-relaxed">
                将在 <span className="font-mono text-ink">{targetName}</span> 上按顺序执行{' '}
                {statements.length} 条语句，整体放在一个事务里：任何一条失败就全部回滚，不会留下改到一半的数据。
              </p>
              {counts.deletes > 0 && (
                <p className="text-xs text-danger leading-relaxed">
                  其中 {counts.deletes} 行会被删除。删除按主键定位，主键在读取后被改过就整批回滚。
                </p>
              )}
              {counts.inserts > 0 && (
                <p className="text-xs text-secondary leading-relaxed">
                  新增行只写入你填过的列，其余列交给数据库的默认值或自增。
                </p>
              )}
              <ol className="max-h-[45vh] overflow-auto bg-canvas border border-line-strong rounded p-2 space-y-1">
                {statements.map((statement, i) => (
                  <li
                    key={i}
                    className="font-mono text-[0.75rem] leading-relaxed text-ink whitespace-pre-wrap break-all"
                  >
                    {i + 1}. {statement}
                  </li>
                ))}
              </ol>
              {applyError && (
                <p className="text-xs text-danger">
                  未提交任何改动：{applyError}
                </p>
              )}
            </div>

            <footer className="flex items-center justify-end gap-2 px-4 py-3 border-t border-line">
              <button
                className="px-3 py-1 rounded text-xs bg-raised hover:bg-emphasis disabled:opacity-50"
                onClick={() => setReview(false)}
                disabled={applying}
              >
                取消
              </button>
              <button
                className="flex items-center px-3 py-1 rounded text-xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50"
                onClick={applyNow}
                disabled={applying || statements.length === 0}
                title="写入数据库，不可撤销"
              >
                {applying ? (
                  <FiLoader size={12} className="mr-1 animate-spin" />
                ) : (
                  <FiCheck size={12} className="mr-1" />
                )}
                {applying ? '应用中…' : '确认应用'}
              </button>
            </footer>
          </div>
        </div>
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
