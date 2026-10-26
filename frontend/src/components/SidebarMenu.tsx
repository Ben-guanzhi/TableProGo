import {
  FiClock,
  FiCode,
  FiCopy,
  FiDownload,
  FiEdit2,
  FiGitBranch,
  FiMinusCircle,
  FiPlus,
  FiRefreshCw,
  FiTag,
  FiTrash2,
} from 'react-icons/fi';

import type { ConfirmRequest } from './ConfirmDialog';
import type { TableRef } from '../types';
import type { KeyInfo, TreeNode } from '../datagrid/sidebarTree';
import { menuEditorFor, menuItemsFor, menuTitle } from '../datagrid/menuItems';

// The object tree's context menu: the item list for one node, plus the two
// inline editors (rename / TTL) it swaps in.
//
// Split out of Sidebar.tsx because the menu is pure presentation over a node: it
// reads the drafts and calls back with "rename this node", "drop this table". The
// tree keeps the state machine (datagrid/sidebarTree.ts); this file only renders
// what that state says.

export interface NewTableTarget {
  connId: string;
  dbType: string;
  database: string;
  schema: string;
}

// The node type is generic: the reducer's TreeNode leaves `connection` opaque, and
// the tree narrows it to the saved Connection. The menu only forwards the node
// back to the caller's own callbacks, so it never needs that payload itself.
export interface SidebarMenuProps<TNode extends TreeNode = TreeNode> {
  node: TNode;
  renameDraft: string | null;
  ttlDraft: string | null;
  keyInfo: KeyInfo | null;
  busy: boolean;
  onRenameDraft: (value: string) => void;
  onTtlDraft: (value: string) => void;
  onClose: () => void;
  // Commit actions receive the node they belong to, so the menu never has to
  // remember which one it was opened for.
  onCommitRename: (node: TNode) => void;
  onCommitKeyRename: (node: TNode) => void;
  onCommitKeyTTL: (node: TNode) => void;
  onRunAction: (node: TNode, action: 'drop' | 'truncate') => void;
  onRunKeyAction: (node: TNode, action: 'delete') => void;
  onRefresh: (node: TNode) => void;
  onRefreshKeyParent: (node: TNode) => void;
  onCopyDdl: (node: TNode) => void;
  onNewTable: (target: NewTableTarget) => void;
  onOpenER: (node: TNode) => void;
  onRoutines: (node: TNode) => void;
  onEnums: (node: TNode) => void;
  onBackup: (node: TNode) => void;
  onConfirm: (request: ConfirmRequest) => void;
}

export const MenuItem: React.FC<{
  label: string;
  icon?: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}> = ({ label, icon, danger, disabled, onClick }) => (
  <button
    className={`w-full flex items-center gap-2 px-2.5 py-1 text-left hover:bg-raised disabled:opacity-40 ${
      danger ? 'text-danger' : 'text-ink'
    }`}
    disabled={disabled}
    onClick={onClick}
  >
    <span className="w-3.5 shrink-0 flex items-center justify-center">{icon}</span>
    <span className="truncate">{label}</span>
  </button>
);

// An inline editor inside the menu: a label, a text field, and 取消 / confirm.
const DraftEditor: React.FC<{
  label: string;
  title: string;
  placeholder: string;
  value: string;
  confirmLabel: string;
  busy: boolean;
  numeric?: boolean;
  disabled?: boolean;
  hint?: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
  onEscape?: () => void;
}> = ({ label, title, placeholder, value, confirmLabel, busy, numeric, disabled, hint, onChange, onSubmit, onCancel, onEscape }) => (
  <div className="px-2.5 py-2">
    <div className="text-2xs text-secondary mb-1 truncate" title={title}>
      {label}
    </div>
    <input
      autoFocus
      aria-label={placeholder}
      inputMode={numeric ? 'numeric' : undefined}
      value={value}
      placeholder={placeholder}
      onChange={e => onChange(e.target.value)}
      onKeyDown={e => {
        if (e.key === 'Enter') onSubmit();
        if (e.key === 'Escape' && onEscape) onEscape();
      }}
      className="w-full bg-canvas border border-accent rounded px-1.5 py-0.5 text-[0.75rem] text-ink focus:outline-none"
    />
    {hint && <div className="text-2xs text-muted mt-1">{hint}</div>}
    <div className="flex items-center justify-end gap-1 mt-1.5">
      <button className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised" onClick={onCancel}>
        取消
      </button>
      <button
        className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
        disabled={disabled || busy}
        onClick={onSubmit}
      >
        {busy ? '执行中…' : confirmLabel}
      </button>
    </div>
  </div>
);

const SidebarMenu = <TNode extends TreeNode>(props: SidebarMenuProps<TNode>) => {
  const { node, renameDraft, ttlDraft, busy } = props;

  if (node.kind === 'key') return <KeyMenu {...props} />;

  const where = menuTitle(node);
  const items = new Set(menuItemsFor(node));

  // The editor actually showing is decided by menuEditorFor; a rename draft that
  // is present but for a key node never reaches this branch.
  const editor = menuEditorFor(node, renameDraft, ttlDraft);
  if (editor === 'rename' && renameDraft !== null) {
    return (
      <DraftEditor
        label={`${node.label} 改名为`}
        title={where}
        placeholder="新名字"
        value={renameDraft}
        confirmLabel="改名"
        busy={busy}
        disabled={!renameDraft.trim()}
        onChange={props.onRenameDraft}
        onSubmit={() => props.onCommitRename(node)}
        onCancel={props.onClose}
      />
    );
  }

  return (
    <>
      <div className="px-2.5 pb-1 text-2xs text-muted truncate border-b border-line-soft" title={where}>
        {where}
      </div>
      {items.has('newTable') && (
        <MenuItem
          label="新建表"
          icon={<FiPlus size={11} />}
          onClick={() => {
            props.onClose();
            props.onNewTable({
              connId: node.connId,
              dbType: node.dbType,
              database: node.database as string,
              schema: node.schema ?? '',
            });
          }}
        />
      )}
      {items.has('er') && (
        <MenuItem
          label="ER 图"
          icon={<FiGitBranch size={11} />}
          onClick={() => {
            props.onClose();
            props.onOpenER(node);
          }}
        />
      )}
      {items.has('routines') && (
        <MenuItem
          label="函数与过程…"
          icon={<FiCode size={11} />}
          onClick={() => {
            props.onClose();
            props.onRoutines(node);
          }}
        />
      )}
      {items.has('enums') && (
        <MenuItem
          label="枚举类型…"
          icon={<FiTag size={11} />}
          onClick={() => {
            props.onClose();
            props.onEnums(node);
          }}
        />
      )}
      {items.has('backup') && (
        <MenuItem
          label="备份数据库…"
          icon={<FiDownload size={11} />}
          onClick={() => {
            props.onClose();
            props.onBackup(node);
          }}
        />
      )}
      {items.has('copyDdl') && (
        <MenuItem label="复制 DDL" icon={<FiCopy size={11} />} onClick={() => props.onCopyDdl(node)} />
      )}
      {items.has('copyDdl') && (
        <MenuItem
          label="重命名…"
          icon={<FiEdit2 size={11} />}
          disabled={busy}
          onClick={() => props.onRenameDraft('')}
        />
      )}
      {items.has('truncate') && (
        <MenuItem
          label="清空数据"
          icon={<FiMinusCircle size={11} />}
          disabled={busy}
          onClick={() => {
            props.onClose();
            props.onConfirm({
              title: `清空 ${where}`,
              message: '表还在，行全部删除，这一步撤不回来。',
              confirmLabel: '清空数据',
              destructive: true,
              onConfirm: () => props.onRunAction(node, 'truncate'),
            });
          }}
        />
      )}
      {items.has('copyDdl') && (
        <MenuItem
          label="Drop"
          icon={<FiTrash2 size={11} />}
          danger
          disabled={busy}
          onClick={() => {
            props.onClose();
            props.onConfirm({
              title: `删除 ${where}`,
              message: '结构和数据一起消失，撤不回来。',
              confirmLabel: 'Drop',
              destructive: true,
              onConfirm: () => props.onRunAction(node, 'drop'),
            });
          }}
        />
      )}
      <div className="my-1 border-t border-line-soft" />
      <MenuItem
        label="刷新"
        icon={<FiRefreshCw size={11} />}
        onClick={() => {
          props.onClose();
          props.onRefresh(node);
        }}
      />
    </>
  );
};

// A Redis key's menu: type/TTL summary, then TTL / rename / delete.
const KeyMenu = <TNode extends TreeNode>(props: SidebarMenuProps<TNode>) => {
  const { node, renameDraft, ttlDraft, keyInfo, busy } = props;
  const where = menuTitle(node);

  if (renameDraft !== null) {
    return (
      <DraftEditor
        label={`键 ${node.label} 改名为`}
        title={where}
        placeholder="新键名"
        value={renameDraft}
        confirmLabel="改名"
        busy={busy}
        disabled={!renameDraft.trim()}
        onChange={props.onRenameDraft}
        onSubmit={() => props.onCommitKeyRename(node)}
        onCancel={props.onClose}
        onEscape={props.onClose}
      />
    );
  }

  if (ttlDraft !== null) {
    return (
      <DraftEditor
        label={`${node.label} 的 TTL（秒）`}
        title={where}
        placeholder="0 = 永不过期"
        value={ttlDraft}
        confirmLabel="设置"
        busy={busy}
        numeric
        hint="填 0 清除过期时间"
        onChange={props.onTtlDraft}
        onSubmit={() => props.onCommitKeyTTL(node)}
        onCancel={props.onClose}
        onEscape={props.onClose}
      />
    );
  }

  return (
    <>
      <div className="px-2.5 pb-1 text-2xs text-muted truncate border-b border-line-soft" title={where}>
        {where}
      </div>
      <div className="px-2.5 py-1 text-2xs text-muted">
        {keyInfo ? `${keyInfo.type} · TTL ${keyInfo.ttl}` : '读取键信息…'}
      </div>
      <MenuItem
        label="设置 TTL…"
        icon={<FiClock size={11} />}
        disabled={busy}
        onClick={() => props.onTtlDraft('')}
      />
      <MenuItem
        label="重命名…"
        icon={<FiEdit2 size={11} />}
        disabled={busy}
        onClick={() => props.onRenameDraft('')}
      />
      <MenuItem
        label="删除键"
        icon={<FiTrash2 size={11} />}
        danger
        disabled={busy}
        onClick={() => {
          props.onClose();
          props.onConfirm({
            title: `删除键 ${where}`,
            message: '键和它的值会立即删除，撤不回来。',
            confirmLabel: '删除键',
            destructive: true,
            onConfirm: () => props.onRunKeyAction(node, 'delete'),
          });
        }}
      />
      <div className="my-1 border-t border-line-soft" />
      <MenuItem
        label="刷新"
        icon={<FiRefreshCw size={11} />}
        onClick={() => {
          props.onClose();
          props.onRefreshKeyParent(node);
        }}
      />
    </>
  );
};

export default SidebarMenu;

// The table reference a node addresses; kept here because both the menu and the
// tree need the same shape.
export const tableRefOf = (node: TreeNode): TableRef => ({
  connId: node.connId,
  database: node.database as string,
  schema: node.schema ?? '',
  table: node.table as string,
});
