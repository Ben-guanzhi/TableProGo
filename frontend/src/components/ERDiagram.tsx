import React, { useEffect, useMemo, useRef, useState } from 'react';
import { FiDownload, FiMaximize2, FiPlus, FiX, FiZoomIn, FiZoomOut } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  CancelQuery,
  GetERDiagram,
  PreviewERFKEdit,
  ApplyERFKEdit,
  ApplyERFKEditConfirmed,
  type ERFKEditResult,
  type Theme,
} from '../api';
import type { ERFK, ERSchema } from '../types';
import {
  type ERLayout,
  type ERNode,
  columnRowY,
  computeLayout,
  HEADER_H,
  NODE_W,
} from '../datagrid/erLayout';
import {
  FK_ACTIONS,
  buildFKAddPayload,
  buildFKDropPayload,
  buildFKRollbackPayload,
  fkLabel,
  normalizeRefColumns,
  type FKEditPayload,
  type FKEditTarget,
} from '../lib/erFk';

interface Props {
  connId: string;
  database: string;
  schema: string;
  title: string;
  theme: Theme;
}

interface Colors {
  canvas: string;
  ink: string;
  secondary: string;
  line: string;
  accent: string;
  raised: string;
  muted: string;
}

// Resolve the same RGB triplets the CSS variables expose, with hard fallbacks so
// the SVG (and its PNG export, which serializes inline colours) never depends on
// a document stylesheet.
function useThemeColors(theme: Theme): Colors {
  return useMemo(() => {
    const resolved =
      theme === 'system'
        ? window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
          ? 'dark'
          : 'light'
        : theme;
    const dark = resolved === 'dark';
    const v = (name: string, fb: string) => {
      const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
      return raw ? `rgb(${raw})` : fb;
    };
    return {
      canvas: v('--canvas', dark ? '#1b1d23' : '#ffffff'),
      ink: v('--ink', dark ? '#e6e8ec' : '#1e2229'),
      secondary: v('--secondary', dark ? '#a6abb5' : '#505661'),
      line: v('--line', dark ? '#343741' : '#ded0e6'),
      accent: v('--accent', dark ? '#4c8df6' : '#2562e0'),
      raised: v('--raised', dark ? '#343741' : '#e3e5ea'),
      muted: v('--muted', dark ? '#8c929d' : '#646a75'),
    };
  }, [theme]);
}

// renderContent draws the whole diagram (background, edges, nodes) in absolute
// layout coordinates. Both the on-screen (pan/zoom-transformed) and the off-screen
// export SVG share it, so what you see is what you export. The edit callbacks are
// only wired on the on-screen copy; the export stays a static picture.
function renderContent(
  layout: ERLayout,
  colors: Colors,
  opts: {
    interactive?: boolean;
    onAdd?: (tableName: string) => void;
    onDeleteEdge?: (index: number) => void;
    confirmingEdge?: number;
  } = {},
) {
  const interactive = opts.interactive === true;
  return (
    <>
      <defs>
        <marker
          id="er-arrow"
          viewBox="0 0 10 10"
          refX={9}
          refY={5}
          markerWidth={7}
          markerHeight={7}
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill={colors.accent} />
        </marker>
      </defs>
      <rect x={0} y={0} width={layout.width} height={layout.height} fill={colors.canvas} />
      {layout.edges.map((e, i) => {
        const from = layout.byName.get(e.from);
        const to = layout.byName.get(e.to);
        if (!from || !to) return null;
        const fIdx = Math.max(0, from.table.columns.findIndex(c => c.name === e.columns[0]));
        const tIdx = Math.max(0, to.table.columns.findIndex(c => c.name === e.refColumns[0]));
        const y1 = columnRowY(fIdx) + from.y;
        const y2 = columnRowY(tIdx) + to.y;
        const x1 = from.x + from.width;
        const x2 = to.x;
        const mx = (x1 + x2) / 2;
        const midY = (y1 + y2) / 2;
        const d = `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`;
        const confirming = interactive && opts.confirmingEdge === i;
        return (
          <g key={`e${i}`}>
            <path
              d={d}
              fill="none"
              stroke={colors.accent}
              strokeWidth={1.5}
              markerEnd="url(#er-arrow)"
              opacity={0.85}
            >
              <title>{`${e.from}.${e.columns.join(', ')} → ${e.to}.${e.refColumns.join(', ')}  (${e.name})`}</title>
            </path>
            {interactive && opts.onDeleteEdge && (
              <g
                onMouseDown={ev => ev.stopPropagation()}
                role="button"
                aria-label={`删除外键 ${e.name}`}
                onClick={() => opts.onDeleteEdge!(i)}
              >
                <title>{confirming ? '再次点击确认删除' : `删除关系 ${e.name}`}</title>
                <circle cx={mx} cy={midY} r={11} fill={colors.canvas} stroke={colors.accent} strokeWidth={1} opacity={0.95} />
                <line x1={mx - 6} y1={midY} x2={mx + 6} y2={midY} stroke={confirming ? '#ef4444' : colors.accent} strokeWidth={2} />
                {confirming && <text x={mx} y={midY + 3.5} textAnchor="middle" fontSize={9} fill="#ef4444">+</text>}
              </g>
            )}
          </g>
        );
      })}
      {layout.nodes.map(n => (
        <ERNodeView
          key={n.table.name}
          node={n}
          colors={colors}
          interactive={interactive}
          onAdd={interactive ? opts.onAdd : undefined}
        />
      ))}
    </>
  );
}

const ERNodeView: React.FC<{
  node: ERNode;
  colors: Colors;
  interactive?: boolean;
  onAdd?: (tableName: string) => void;
}> = ({ node, colors, interactive, onAdd }) => {
  const { table: t, x, y, width, height } = node;
  return (
    <g>
      <rect x={x} y={y} width={width} height={height} rx={6} fill={colors.raised} stroke={colors.line} strokeWidth={1} />
      <rect x={x} y={y} width={width} height={HEADER_H} rx={6} fill={colors.canvas} stroke={colors.line} strokeWidth={1} />
      <text x={x + 10} y={y + HEADER_H / 2 + 4} fill={colors.ink} fontSize={13} fontWeight={600}>
        {t.name}
      </text>
      {interactive && onAdd && (
        <g
          onMouseDown={ev => ev.stopPropagation()}
          role="button"
          aria-label={`为表 ${t.name} 添加外键`}
          onClick={() => onAdd(t.name)}
        >
          <title>{`为 ${t.name} 添加外键`}</title>
          <circle cx={x + width - 10} cy={y + HEADER_H / 2} r={8} fill={colors.canvas} stroke={colors.accent} strokeWidth={1} />
          <line x1={x + width - 13} y1={y + HEADER_H / 2} x2={x + width - 7} y2={y + HEADER_H / 2} stroke={colors.accent} strokeWidth={1.6} />
          <line x1={x + width - 10} y1={y + HEADER_H / 2 - 3} x2={x + width - 10} y2={y + HEADER_H / 2 + 3} stroke={colors.accent} strokeWidth={1.6} />
        </g>
      )}
      {t.columns.map((c, i) => {
        const ry = columnRowY(i) + y;
        const isPK = c.isPrimaryKey;
        return (
          <g key={c.name}>
            {isPK && <circle cx={x + 9} cy={ry - 2} r={3} fill={colors.accent} />}
            <text x={x + (isPK ? 18 : 10)} y={ry} fill={isPK ? colors.accent : colors.ink} fontSize={11}>
              {c.name}
            </text>
            <text x={x + width - 8} y={ry} textAnchor="end" fill={colors.secondary} fontSize={10}>
              {c.type}
            </text>
          </g>
        );
      })}
    </g>
  );
};

interface AppliedEdit {
  action: 'add' | 'drop';
  table: string;
  fkName: string;
  definition?: ERFK;
  result: ERFKEditResult;
}

const ERDiagram: React.FC<Props> = ({ connId, database, schema, title, theme }) => {
  const colors = useThemeColors(theme);
  const [model, setModel] = useState<ERSchema | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>();
  const [scale, setScale] = useState(1);
  const [tx, setTx] = useState(0);
  const [ty, setTy] = useState(0);

  // Edit state: the add form, the pending preview, the applied edit and its
  // rollback, plus the Safe Mode pause.
  const [adding, setAdding] = useState(false);
  const [sourceTable, setSourceTable] = useState('');
  const [fkName, setFkName] = useState('');
  const [sourceColumns, setSourceColumns] = useState<string[]>([]);
  const [refTable, setRefTable] = useState('');
  const [refColumns, setRefColumns] = useState<string[]>([]);
  const [onDelete, setOnDelete] = useState('NO ACTION');
  const [onUpdate, setOnUpdate] = useState('NO ACTION');

  const [preview, setPreview] = useState<ERFKEditResult | null>(null);
  const [previewKind, setPreviewKind] = useState<'add' | 'drop' | null>(null);
  const [previewTable, setPreviewTable] = useState('');
  const [previewFk, setPreviewFk] = useState<ERFK | null>(null);
  const [applied, setApplied] = useState<AppliedEdit | null>(null);
  const [confirmingEdge, setConfirmingEdge] = useState<number | null>(null);
  const [pendingReason, setPendingReason] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const exportRef = useRef<SVGSVGElement | null>(null);
  const drag = useRef<{ x: number; y: number; tx: number; ty: number } | null>(null);

  const layout = useMemo(() => (model ? computeLayout(model) : null), [model]);

  const load = () => {
    setLoading(true);
    setError(undefined);
    GetERDiagram(connId, database, schema)
      .then(m => {
        setModel(m);
        setScale(1);
        setTx(0);
        setTy(0);
      })
      .catch(e => setError((e as Error).message))
      .finally(() => setLoading(false));
  };

  useEffect(load, [connId, database, schema]);

  // Wheel zoom with a non-passive listener so preventDefault actually stops the
  // page from scrolling while zooming the diagram.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1;
      setScale(s => Math.min(3, Math.max(0.2, s * factor)));
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, []);

  const clamp = (s: number) => Math.min(3, Math.max(0.2, s));

  const fileName = `${database || 'db'}.${schema || 'schema'}.er`;

  const downloadBlob = (blob: Blob, name: string) => {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  const exportSVG = () => {
    if (!exportRef.current) return;
    const data = new XMLSerializer().serializeToString(exportRef.current);
    downloadBlob(new Blob([data], { type: 'image/svg+xml;charset=utf-8' }), `${fileName}.svg`);
  };

  const exportPNG = () => {
    if (!exportRef.current || !layout) return;
    const data = new XMLSerializer().serializeToString(exportRef.current);
    const svg64 = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(data)));
    const img = new Image();
    img.onload = () => {
      const factor = 2;
      const canvas = document.createElement('canvas');
      canvas.width = layout.width * factor;
      canvas.height = layout.height * factor;
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      ctx.scale(factor, factor);
      ctx.drawImage(img, 0, 0);
      canvas.toBlob(blob => {
        if (blob) downloadBlob(blob, `${fileName}.png`);
      }, 'image/png');
    };
    img.src = svg64;
  };

  const clearEdit = () => {
    setAdding(false);
    setPreview(null);
    setPreviewKind(null);
    setPreviewTable('');
    setPreviewFk(null);
    setConfirmingEdge(null);
  };

  // Open the add form for one node.
  const openAddForm = (tableName: string) => {
    clearEdit();
    setAdding(true);
    setSourceTable(tableName);
    setFkName('');
    setSourceColumns([]);
    setRefTable('');
    setRefColumns([]);
    setOnDelete('NO ACTION');
    setOnUpdate('NO ACTION');
    setPendingReason(null);
  };

  const sourceTableObj = model?.tables.find(t => t.name === sourceTable);

  // The referenced table choices exclude the source table.
  const refTableOptions = useMemo(() => {
    if (!model) return [];
    return model.tables.map(t => t.name).filter(n => n !== sourceTable);
  }, [model, sourceTable]);

  const toggleSource = (col: string) => {
    setSourceColumns(current => (current.includes(col) ? current.filter(c => c !== col) : [...current, col]));
  };

  const toggleRef = (col: string) => {
    setRefColumns(current => (current.includes(col) ? current.filter(c => c !== col) : [...current, col]));
  };

  // The reference list is aligned to the source count, so a short pick is padded
  // with the source column at the same position.
  const effectiveRefColumns = normalizeRefColumns(refColumns, sourceColumns);
  const refShort = refColumns.length !== sourceColumns.length && sourceColumns.length > 0;

  const buildAddPayload = (): FKEditPayload | null => {
    if (!sourceTable) {
      toast.error('请先选择来源表');
      return null;
    }
    try {
      return buildFKAddPayload(
        { connId, database, schema, table: sourceTable },
        {
          name: fkName,
          columns: sourceColumns,
          refSchema: schema,
          refTable,
          refColumns: effectiveRefColumns,
          onDelete,
          onUpdate,
        },
      );
    } catch (e) {
      toast.error((e as Error).message);
      return null;
    }
  };

  const runAddPreview = async () => {
    const payload = buildAddPayload();
    if (!payload) return;
    setBusy(true);
    setPendingReason(null);
    try {
      const res = await PreviewERFKEdit(payload);
      setPreview(res);
      setPreviewKind('add');
      setPreviewTable(sourceTable);
      setPreviewFk(null);
      if (res.statements.length === 0 && res.warnings.length === 0) toast.success('没有需要执行的改动');
    } catch (e) {
      toast.error((e as Error).message, { duration: 8000 });
    } finally {
      setBusy(false);
    }
  };

  // Start the delete flow for one edge; the second click confirms and shows the
  // preview of the DROP that would run.
  const requestDelete = (index: number) => {
    if (!layout) return;
    const edge = layout.edges[index];
    if (!edge) return;
    const from = layout.byName.get(edge.from);
    const fk = from?.table.foreignKeys.find(f => f.name === edge.name);
    if (confirmingEdge === index) {
      setConfirmingEdge(null);
      return;
    }
    setConfirmingEdge(index);
    if (!fk) return;
    setPendingReason(null);
    void (async () => {
      setBusy(true);
      try {
        const res = await PreviewERFKEdit(buildFKDropPayload({ connId, database, schema, table: edge.from }, edge.name));
        setPreview(res);
        setPreviewKind('drop');
        setPreviewTable(edge.from);
        setPreviewFk(fk);
      } catch (e) {
        toast.error((e as Error).message, { duration: 8000 });
      } finally {
        setBusy(false);
      }
    })();
  };

  const applyPayload = async (payload: FKEditPayload, confirmed: boolean, action: 'add' | 'drop', table: string, definition?: ERFK | null) => {
    setBusy(true);
    try {
      const res = await (confirmed ? ApplyERFKEditConfirmed(payload) : ApplyERFKEdit(payload));
      if ((res as unknown as { needsConfirmation?: boolean }).needsConfirmation) {
        const reason = (res as unknown as { reason: string }).reason;
        setPendingReason(reason);
        toast(reason, { duration: 10000, icon: '⚠️' });
        return;
      }
      setPendingReason(null);
      setApplied({ action, table, fkName: payload.fkName ?? payload.name ?? '', definition: definition ?? undefined, result: res });
      toast.success(`外键已更新（${res.statements.length} 条语句）`);
      clearEdit();
      load();
    } catch (e) {
      toast.error((e as Error).message, { duration: 10000 });
    } finally {
      setBusy(false);
    }
  };

  const runAddApply = async () => {
    const payload = buildAddPayload();
    if (!payload) return;
    await applyPayload(payload, pendingReason !== null, 'add', sourceTable, null);
  };

  const runDropApply = async () => {
    if (!previewFk) return;
    const payload = buildFKDropPayload({ connId, database, schema, table: previewTable }, previewFk.name);
    await applyPayload(payload, pendingReason !== null, 'drop', previewTable, previewFk);
  };

  // Undo the last applied edit.
  const runRollback = async () => {
    if (!applied) return;
    const payload = buildFKRollbackPayload(
      { connId, database, schema, table: applied.table },
      { fkName: applied.fkName, rollbackOf: applied.action, fk: applied.definition },
      false,
    );
    setBusy(true);
    try {
      const res = await ApplyERFKEdit(payload);
      if ((res as unknown as { needsConfirmation?: boolean }).needsConfirmation) {
        const reason = (res as unknown as { reason: string }).reason;
        setPendingReason(reason);
        toast(reason, { duration: 10000, icon: '⚠️' });
        return;
      }
      setPendingReason(null);
      setApplied(null);
      toast.success(`已撤销（${res.statements.length} 条语句）`);
      load();
    } catch (e) {
      toast.error((e as Error).message, { duration: 10000 });
    } finally {
      setBusy(false);
    }
  };

  const input =
    'w-full bg-canvas border border-line rounded px-1.5 py-1 font-mono text-ink focus:outline-none focus:border-accent';
  const btn = 'px-2.5 py-1 rounded text-xs disabled:opacity-40';

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line bg-panel">
        <span className="text-xs font-semibold text-ink truncate" title={title}>
          {title}
        </span>
        {layout && (
          <span className="text-2xs text-muted">
            {layout.nodes.length} 表 · {layout.edges.length} 关系
          </span>
        )}
        <div className="flex-1" />
        <button
          className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover"
          title="在图上为某张表添加外键（点击节点右上角 +）"
          onClick={() => layout && openAddForm(layout.nodes[0].table.name)}
        >
          <FiPlus size={10} /> 添加关系
        </button>
        <button
          className="p-1 text-secondary hover:text-ink disabled:opacity-40"
          title="放大"
          aria-label="放大"
          disabled={scale >= 3 - 1e-9}
          onClick={() => setScale(s => clamp(s * 1.1))}
        >
          <FiZoomIn size={14} />
        </button>
        <button
          className="p-1 text-secondary hover:text-ink disabled:opacity-40"
          title="缩小"
          aria-label="缩小"
          disabled={scale <= 0.2 + 1e-9}
          onClick={() => setScale(s => clamp(s / 1.1))}
        >
          <FiZoomOut size={14} />
        </button>
        <button
          className="p-1 text-secondary hover:text-ink"
          title="复位视图"
          aria-label="复位视图"
          onClick={() => {
            setScale(1);
            setTx(0);
            setTy(0);
          }}
        >
          <FiMaximize2 size={14} />
        </button>
        <div className="mx-1 w-px h-4 bg-line-soft" />
        <button
          className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
          title="导出 SVG"
          onClick={exportSVG}
        >
          SVG
        </button>
        <button
          className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised flex items-center gap-1"
          title="导出 PNG"
          onClick={exportPNG}
        >
          <FiDownload size={11} /> PNG
        </button>
      </div>

      {error && (
        <div className="m-3 px-3 py-2 rounded bg-danger/10 text-danger text-sm border border-danger/30">
          {error}
        </div>
      )}
      {loading && (
        <div className="p-4 flex items-center gap-3 text-secondary text-sm">
          读取结构关系…
          <button
            className="px-2 py-0.5 rounded border border-line text-2xs text-secondary hover:text-ink hover:bg-raised"
            onClick={() => CancelQuery(connId, "er").catch(() => {})}
          >
            取消
          </button>
        </div>
      )}

      {/* The applied-edit banner carries the rollback affordance. */}
      {applied && (
        <div className="mx-3 mt-2 px-3 py-2 rounded border border-success/40 bg-success/10 flex items-center gap-3">
          <span className="text-xs text-ink">
            已应用：{applied.action === 'add' ? '新增' : '删除'}外键 {applied.fkName || '(未命名)'}（{applied.result.statements.length} 条语句）
          </span>
          <div className="flex-1" />
          <button className="px-2 py-0.5 rounded text-xs bg-raised hover:bg-emphasis text-ink" onClick={() => void runRollback()} disabled={busy}>
            撤销
          </button>
          <button className="p-1 text-secondary hover:text-ink" onClick={() => setApplied(null)} aria-label="关闭提示">
            <FiX size={12} />
          </button>
        </div>
      )}

      {/* The pending preview: what will run, what undoes it. */}
      {preview && (
        <div className="mx-3 mt-2 px-3 py-2 rounded border border-line bg-panel">
          <div className="text-2xs text-secondary mb-1">
            {previewKind === 'drop'
              ? `确认删除外键 ${previewFk ? fkLabel(previewFk) : ''}`
              : `将新增外键 ${fkName}`}
            · {preview.statements.length} 条语句
          </div>
          <pre className="max-h-32 overflow-auto px-3 py-2 rounded bg-canvas border border-line text-2xs font-mono text-ink whitespace-pre-wrap break-all">
            {preview.statements.join(';\n\n')}
          </pre>
          {preview.rollback.length > 0 && (
            <div className="text-2xs text-muted mt-1">撤销：{preview.rollback.join('；')}</div>
          )}
          {preview.warnings.length > 0 && (
            <ul className="mt-1 px-3 py-1.5 rounded bg-warning/10 border border-warning/40 text-warning text-2xs list-disc list-inside">
              {preview.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          )}
          <div className="flex justify-end gap-2 mt-2">
            <button className={btn + ' bg-raised hover:bg-emphasis text-ink'} onClick={clearEdit}>
              取消
            </button>
            {previewKind === 'drop' ? (
              <button
                className={btn + ' bg-danger hover:bg-danger-hover text-on-danger'}
                onClick={() => void runDropApply()}
                disabled={busy}
              >
                {pendingReason !== null ? '再次点击确认删除' : '确认删除'}
              </button>
            ) : (
              <button
                className={btn + ' bg-accent hover:bg-accent-hover text-on-accent'}
                onClick={() => void runAddApply()}
                disabled={busy || preview.statements.length === 0}
              >
                {pendingReason !== null ? '确认执行' : '执行'}
              </button>
            )}
          </div>
          {pendingReason !== null && <div className="text-2xs text-warning mt-1">{pendingReason}</div>}
        </div>
      )}

      <div
        ref={containerRef}
        className="flex-1 min-h-0 overflow-hidden relative cursor-grab active:cursor-grabbing"
        style={{ backgroundColor: colors.canvas }}
        onMouseDown={e => {
          drag.current = { x: e.clientX, y: e.clientY, tx, ty };
        }}
        onMouseMove={e => {
          if (!drag.current) return;
          setTx(drag.current.tx + (e.clientX - drag.current.x));
          setTy(drag.current.ty + (e.clientY - drag.current.y));
        }}
        onMouseUp={() => {
          drag.current = null;
        }}
        onMouseLeave={() => {
          drag.current = null;
        }}
      >
        <svg width="100%" height="100%">
          <g transform={`translate(${tx} ${ty}) scale(${scale})`}>
            {layout &&
              renderContent(layout, colors, {
                interactive: true,
                onAdd: openAddForm,
                onDeleteEdge: requestDelete,
                confirmingEdge: confirmingEdge ?? undefined,
              })}
          </g>
        </svg>
      </div>

      {/* The add form: source table/columns, target table/columns, actions. */}
      {adding && sourceTable && (
        <div className="absolute bottom-3 right-3 w-80 rounded-lg border border-line bg-panel shadow-xl p-3 z-20 flex flex-col gap-2 max-h-[70vh] overflow-auto">
          <div className="flex items-center justify-between">
            <span className="text-xs font-semibold">添加外键 · {sourceTable}</span>
            <button className="text-secondary hover:text-ink" onClick={clearEdit} aria-label="关闭">
              <FiX size={13} />
            </button>
          </div>
          <label className="text-2xs text-secondary">本表列</label>
          <div className="flex flex-wrap gap-1 max-h-24 overflow-auto">
            {sourceTableObj?.columns.map(c => (
              <label key={c.name} className="text-2xs flex items-center gap-1 bg-raised rounded px-1.5 py-0.5">
                <input type="checkbox" checked={sourceColumns.includes(c.name)} onChange={() => toggleSource(c.name)} />
                {c.name}
              </label>
            ))}
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-2xs text-secondary">外键名</label>
              <input className={input} value={fkName} onChange={e => setFkName(e.target.value)} placeholder="fk_book_author" aria-label="外键名" />
            </div>
            <div>
              <label className="text-2xs text-secondary">引用表</label>
              <select className={input} value={refTable} onChange={e => setRefTable(e.target.value)} aria-label="引用表">
                <option value="">选择…</option>
                {refTableOptions.map(n => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </div>
          </div>
          {refTable && (
            <div>
              <label className="text-2xs text-secondary">
                引用列{refShort ? '（数量不足，将按本表列补齐）' : ''}
              </label>
              <div className="flex flex-wrap gap-1 max-h-24 overflow-auto">
                {model?.tables.find(t => t.name === refTable)?.columns.map(c => (
                  <label key={c.name} className="text-2xs flex items-center gap-1 bg-raised rounded px-1.5 py-0.5">
                    <input type="checkbox" checked={effectiveRefColumns.includes(c.name)} onChange={() => toggleRef(c.name)} />
                    {c.name}
                  </label>
                ))}
              </div>
            </div>
          )}
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-2xs text-secondary">ON DELETE</label>
              <select className={input} value={onDelete} onChange={e => setOnDelete(e.target.value)} aria-label="ON DELETE">
                {FK_ACTIONS.map(a => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-2xs text-secondary">ON UPDATE</label>
              <select className={input} value={onUpdate} onChange={e => setOnUpdate(e.target.value)} aria-label="ON UPDATE">
                {FK_ACTIONS.map(a => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <button className={btn + ' bg-raised hover:bg-emphasis text-ink'} onClick={clearEdit} disabled={busy}>
              关闭
            </button>
            <button className={btn + ' bg-raised hover:bg-emphasis text-ink'} onClick={() => void runAddPreview()} disabled={busy}>
              {busy ? '计算中…' : '预览'}
            </button>
            <button
              className={btn + ' bg-accent hover:bg-accent-hover text-on-accent'}
              onClick={() => void runAddApply()}
              disabled={busy || !preview || preview.statements.length === 0}
            >
              执行
            </button>
          </div>
        </div>
      )}

      {/* Off-screen copy at full size for export. It shares renderContent, so the
          serialized SVG carries the same inline colours and survives without a
          document stylesheet. Edit affordances are switched off for the export. */}
      {layout && (
        <svg
          ref={exportRef}
          width={layout.width}
          height={layout.height}
          style={{ position: 'absolute', left: -100000, top: 0 }}
          aria-hidden
        >
          <g>{renderContent(layout, colors)}</g>
        </svg>
      )}
    </div>
  );
};

export default ERDiagram;
