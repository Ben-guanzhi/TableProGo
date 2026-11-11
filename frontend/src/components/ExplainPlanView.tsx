import React, { useEffect, useMemo, useRef, useState } from 'react';
import { FiAlertTriangle, FiMaximize2, FiMinus, FiPlus, FiX, FiDownload } from 'react-icons/fi';
import type { ExplainPlanData, PlanNode } from '../api';
import {
  NODE_HEIGHT,
  NODE_WIDTH,
  connectorPath,
  fitScale,
  layoutPlan,
  nodeTone,
  truncateDetail,
  truncateLabel,
} from '../datagrid/planLayout';

interface ExplainPlanViewProps {
  plan: ExplainPlanData;
  onClose: () => void;
}

// W11: 可视化执行计划.
//
// Drawn as inline SVG rather than with a graph library: the workspace already
// renders its ER diagram this way, and one plan is tens of nodes, not thousands —
// a dependency would buy nothing. The layout math lives in datagrid/planLayout.ts
// so it is testable without a DOM.
const ExplainPlanView: React.FC<ExplainPlanViewProps> = ({ plan, onClose }) => {
  const hostRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const [zoom, setZoom] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const drag = useRef<{ x: number; y: number; panX: number; panY: number } | null>(null);
  const [showRaw, setShowRaw] = useState(false);

  const layout = useMemo(
    () => (plan.root ? layoutPlan(plan.root) : null),
    [plan.root],
  );

  // Fit on first paint, so a wide plan is visible without hunting for it.
  useEffect(() => {
    const host = hostRef.current;
    if (!host || !layout) return;
    setZoom(fitScale(layout, host.clientWidth - 24, host.clientHeight - 24));
    setPan({ x: 0, y: 0 });
  }, [layout]);

  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button !== 0) return;
    drag.current = { x: e.clientX, y: e.clientY, panX: pan.x, panY: pan.y };
    (e.currentTarget as Element).setPointerCapture(e.pointerId);
  };
  const onPointerMove = (e: React.PointerEvent) => {
    const d = drag.current;
    if (!d) return;
    setPan({ x: d.panX + (e.clientX - d.x), y: d.panY + (e.clientY - d.y) });
  };
  const onPointerUp = (e: React.PointerEvent) => {
    drag.current = null;
    try {
      (e.currentTarget as Element).releasePointerCapture(e.pointerId);
    } catch {
      /* the pointer may already be released */
    }
  };

  const exportSVG = () => {
    const svg = svgRef.current;
    if (!svg) return;
    const blob = new Blob([new XMLSerializer().serializeToString(svg)], {
      type: 'image/svg+xml',
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'execution-plan.svg';
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 2000);
  };

  const root = plan.root ?? null;
  const tone = (n: PlanNode, isRoot: boolean) => {
    switch (nodeTone(n, isRoot)) {
      case 'warn':
        return { fill: 'rgb(var(--danger) / 0.14)', stroke: 'rgb(var(--danger) / 0.55)' };
      case 'root':
        return { fill: 'rgb(var(--accent) / 0.16)', stroke: 'rgb(var(--accent) / 0.6)' };
      default:
        return { fill: 'rgb(var(--panel))', stroke: 'rgb(var(--line-strong))' };
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-scrim/50 flex items-center justify-center p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="plan-view-title"
        className="bg-panel rounded-lg w-full max-w-5xl h-[85vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-4 py-2.5 border-b border-line shrink-0">
          <h2 id="plan-view-title" className="text-sm font-semibold">
            执行计划
          </h2>
          <span className="ml-2 text-2xs text-muted">
            {plan.nodes} 个节点 · {layout ? `${layout.depth} 层` : '文本计划'}
          </span>
          <div className="ml-auto flex items-center gap-1">
            <button
              className="p-1 text-secondary hover:text-ink"
              onClick={() => setZoom(z => Math.max(0.15, z / 1.2))}
              aria-label="缩小"
              title="缩小"
            >
              <FiMinus size={13} />
            </button>
            <span className="text-2xs text-muted w-10 text-center">{Math.round(zoom * 100)}%</span>
            <button
              className="p-1 text-secondary hover:text-ink"
              onClick={() => setZoom(z => Math.min(3, z * 1.2))}
              aria-label="放大"
              title="放大"
            >
              <FiPlus size={13} />
            </button>
            <button
              className="p-1 text-secondary hover:text-ink"
              onClick={() => {
                const host = hostRef.current;
                if (host && layout) setZoom(fitScale(layout, host.clientWidth - 24, host.clientHeight - 24));
                setPan({ x: 0, y: 0 });
              }}
              aria-label="适应窗口"
              title="适应窗口"
            >
              <FiMaximize2 size={13} />
            </button>
            <button
              className="p-1 text-secondary hover:text-ink"
              onClick={() => setShowRaw(v => !v)}
              aria-pressed={showRaw}
              title="显示原始计划"
              aria-label="显示原始计划"
            >
              <FiAlertTriangle size={13} className={showRaw ? 'text-accent' : ''} />
            </button>
            <button
              className="p-1 text-secondary hover:text-ink"
              onClick={exportSVG}
              title="导出 SVG"
              aria-label="导出 SVG"
            >
              <FiDownload size={13} />
            </button>
            <button className="p-1 text-secondary hover:text-ink ml-1" onClick={onClose} aria-label="关闭执行计划">
              <FiX size={15} />
            </button>
          </div>
        </div>

        <div
          ref={hostRef}
          className="flex-1 min-h-0 overflow-hidden relative bg-canvas cursor-grab active:cursor-grabbing"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
        >
          {layout && root ? (
            <div
              style={{ transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`, transformOrigin: '0 0' }}
              className="absolute top-0 left-0"
            >
              <svg
                ref={svgRef}
                width={layout.width}
                height={layout.height}
                className="block"
                role="img"
                aria-label="执行计划树"
              >
                {/* Connectors first: a line must sit behind the box it points at. */}
                {layout.nodes.map((p, i) =>
                  p.parent >= 0
                    ? (
                        <path
                          key={`c-${i}`}
                          d={connectorPath(layout.nodes[p.parent], p)}
                          fill="none"
                          stroke="rgb(var(--line-strong))"
                          strokeWidth={1.2}
                        />
                      )
                    : null,
                )}
                {layout.nodes.map((p, i) => {
                  const t = tone(p.node, p.parent < 0);
                  return (
                    <g key={`n-${i}`}>
                      <rect
                        x={p.x}
                        y={p.y}
                        width={NODE_WIDTH}
                        height={NODE_HEIGHT}
                        rx={6}
                        fill={t.fill}
                        stroke={t.stroke}
                        strokeWidth={1}
                      >
                        <title>{`${p.node.label}\n${p.node.detail ?? ''}`}</title>
                      </rect>
                      <text
                        x={p.x + 8}
                        y={p.y + 17}
                        fontSize={11}
                        fill="rgb(var(--ink))"
                        className="select-none"
                      >
                        {truncateLabel(p.node.label)}
                      </text>
                      <text
                        x={p.x + 8}
                        y={p.y + 32}
                        fontSize={9}
                        fill="rgb(var(--muted))"
                        className="select-none"
                      >
                        {p.node.warn ? `⚠ ${p.node.warn} · ` : ''}
                        {truncateDetail(p.node.detail ?? '')}
                      </text>
                    </g>
                  );
                })}
              </svg>
            </div>
          ) : (
            <div className="h-full overflow-auto p-4">
              <p className="text-xs text-warning mb-2">{plan.note || '该引擎只提供文本计划'}</p>
              <pre className="text-2xs font-mono text-ink whitespace-pre-wrap break-words">{plan.raw}</pre>
            </div>
          )}
        </div>

        {showRaw && layout && (
          <div className="shrink-0 border-t border-line max-h-[30vh] overflow-auto">
            <pre className="p-3 text-2xs font-mono text-secondary whitespace-pre-wrap break-words">
              {plan.raw}
            </pre>
          </div>
        )}
      </div>
    </div>
  );
};

export default ExplainPlanView;
