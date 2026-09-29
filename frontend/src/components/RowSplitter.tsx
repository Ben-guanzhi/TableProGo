import React from 'react';

// The editor can be as short as a couple of lines but must leave the grid
// usable, so both the drag and the keyboard path clamp through here.
export const EDITOR_MIN_HEIGHT = 60;
export const editorMaxHeight = () => Math.max(EDITOR_MIN_HEIGHT, window.innerHeight - 220);
export const clampEditorHeight = (height: number) =>
  Math.min(Math.max(height, EDITOR_MIN_HEIGHT), editorMaxHeight());

// Dragging is the fast path, but a pointer-only handle is invisible to the
// keyboard, so the same splitter takes arrow keys too and leaves the clamping
// to whoever owns the height.
const RowSplitter: React.FC<{
  onPointerDown: (event: React.PointerEvent) => void;
  onStep: (delta: number) => void;
  label: string;
  valueNow: number;
  valueMin: number;
  valueMax: number;
}> = ({ onPointerDown, onStep, label, valueNow, valueMin, valueMax }) => (
  <div
    role="separator"
    aria-orientation="horizontal"
    aria-label={label}
    aria-valuenow={valueNow}
    aria-valuemin={valueMin}
    aria-valuemax={valueMax}
    tabIndex={0}
    onPointerDown={onPointerDown}
    onKeyDown={e => {
      const step = e.shiftKey ? 24 : 6;
      if (e.key === 'ArrowUp') onStep(-step);
      else if (e.key === 'ArrowDown') onStep(step);
      else return;
      e.preventDefault();
      e.stopPropagation();
    }}
    className="h-1 shrink-0 bg-raised hover:bg-accent focus-visible:bg-accent cursor-row-resize"
    title="拖动调整编辑器高度；聚焦后方向键微调"
  />
);

export default RowSplitter;
