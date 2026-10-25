import React, { useEffect, useMemo, useRef, useState } from 'react';
import { FiDownload, FiMaximize2, FiZoomIn, FiZoomOut } from 'react-icons/fi';
import { CancelQuery, GetERDiagram } from '../api';
import type { Theme } from '../api';
import type { ERSchema } from '../types';
import {
  type ERLayout,
  type ERNode,
  columnRowY,
  computeLayout,
  HEADER_H,
  NODE_W,
} from '../datagrid/erLayout';

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
      line: v('--line', dark ? '#34374a' : '#dee0e6'),
      accent: v('--accent', dark ? '#4c8df6' : '#2562e0'),
      raised: v('--raised', dark ? '#34374a' : '#e3e5ea'),
      muted: v('--muted', dark ? '#8c929d' : '#646a75'),
    };
  }, [theme]);
}

// renderContent draws the whole diagram (background, edges, nodes) in absolute
// layout coordinates. Both the on-screen (pan/zoom-transformed) and the off-screen
// export SVG share it, so what you see is what you export.
function renderContent(layout: ERLayout, colors: Colors) {
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
        const d = `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`;
        return (
          <path
            key={`e${i}`}
            d={d}
            fill="none"
            stroke={colors.accent}
            strokeWidth={1.5}
            markerEnd="url(#er-arrow)"
            opacity={0.85}
          >
            <title>{`${e.from}.${e.columns.join(', ')} → ${e.to}.${e.refColumns.join(', ')}  (${e.name})`}</title>
          </path>
        );
      })}
      {layout.nodes.map(n => (
        <ERNodeView key={n.table.name} node={n} colors={colors} />
      ))}
    </>
  );
}

const ERNodeView: React.FC<{ node: ERNode; colors: Colors }> = ({ node, colors }) => {
  const { table: t, x, y, width, height } = node;
  return (
    <g>
      <rect x={x} y={y} width={width} height={height} rx={6} fill={colors.raised} stroke={colors.line} strokeWidth={1} />
      <rect x={x} y={y} width={width} height={HEADER_H} rx={6} fill={colors.canvas} stroke={colors.line} strokeWidth={1} />
      <text x={x + 10} y={y + HEADER_H / 2 + 4} fill={colors.ink} fontSize={13} fontWeight={600}>
        {t.name}
      </text>
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

const ERDiagram: React.FC<Props> = ({ connId, database, schema, title, theme }) => {
  const colors = useThemeColors(theme);
  const [model, setModel] = useState<ERSchema | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>();
  const [scale, setScale] = useState(1);
  const [tx, setTx] = useState(0);
  const [ty, setTy] = useState(0);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const exportRef = useRef<SVGSVGElement | null>(null);
  const drag = useRef<{ x: number; y: number; tx: number; ty: number } | null>(null);

  const layout = useMemo(() => (model ? computeLayout(model) : null), [model]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(undefined);
    GetERDiagram(connId, database, schema)
      .then(m => {
        if (!cancelled) {
          setModel(m);
          setScale(1);
          setTx(0);
          setTy(0);
        }
      })
      .catch(e => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [connId, database, schema]);

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
          className="p-1 text-secondary hover:text-ink disabled:opacity-40"
          title="放大"
          aria-label="放大"
          onClick={() => setScale(s => clamp(s * 1.1))}
        >
          <FiZoomIn size={14} />
        </button>
        <button
          className="p-1 text-secondary hover:text-ink disabled:opacity-40"
          title="缩小"
          aria-label="缩小"
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
            onClick={() => CancelQuery(connId).catch(() => {})}
          >
            取消
          </button>
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
            {layout && renderContent(layout, colors)}
          </g>
        </svg>
      </div>

      {/* Off-screen copy at full size for export. It shares renderContent, so the
          serialized SVG carries the same inline colours and survives without a
          document stylesheet. */}
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
