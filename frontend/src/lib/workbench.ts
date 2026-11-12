// W-T2: the workbench HTTP wrappers. The terminal panel is the only consumer;
// the shapes mirror the Go types exactly (SessionInfo / BroadcastResult), which
// is why the broadcast's withheld shape carries the warning text the UI shows.

import { post } from '../api';

export interface WorkbenchNode {
  tag: string;
  label?: string;
}

export interface WorkbenchChannel {
  tag: string;
}

export interface WorkbenchState {
  pluginId?: string;
  providerUid?: string;
  active: boolean;
  navNodes: WorkbenchNode[];
  channels: WorkbenchChannel[];
  safeModeOn: boolean;
}

export async function GetWorkbenchState(): Promise<WorkbenchState> {
  return post('/api/workbench/state', {});
}

export interface ChannelOpenParams {
  host: string;
  port: number;
  user: string;
  password: string;
  cols?: number;
  rows?: number;
}

export async function OpenWorkbenchChannel(params: ChannelOpenParams): Promise<{ channelId: string }> {
  return post('/api/workbench/channel', { action: 'open', ...params });
}

export async function WriteWorkbenchChannel(
  channelId: string,
  input: string,
  maxBytes = 8192,
): Promise<{ channelId: string; output: string; ended: boolean }> {
  return post('/api/workbench/channel', { action: 'data', channelId, input, maxBytes });
}

export async function ResizeWorkbenchChannel(channelId: string, cols: number, rows: number): Promise<void> {
  await post('/api/workbench/channel', { action: 'resize', channelId, cols, rows });
}

export async function CloseWorkbenchChannel(channelId: string): Promise<void> {
  await post('/api/workbench/channel', { action: 'close', channelId });
}

// BroadcastWorkbench is the WITHHELD call: when the state's safeModeOn is true,
// the response is { withheld: true, channelIds, input, warning } and NOTHING ran.
// The UI shows the warning and re-posts through BroadcastConfirmed.
export interface BroadcastResponse {
  withheld?: boolean;
  channelIds?: string[];
  input?: string;
  warning?: string;
  results?: Record<string, string>;
  errors?: Record<string, string>;
}

export async function BroadcastWorkbench(channelIds: string[], input: string): Promise<BroadcastResponse> {
  return post('/api/workbench/broadcast', { channelIds, input });
}

export async function BroadcastConfirmed(channelIds: string[], input: string): Promise<BroadcastResponse> {
  return post('/api/workbench/broadcast-confirmed', { channelIds, input });
}

// W-T3: session recording (asciinema v3 casts) + the shortcut library.

export async function StartRecording(channelId: string, title: string): Promise<{ path: string }> {
  return post('/api/workbench/record', { action: 'start', channelId, title });
}

export async function StopRecording(channelId: string): Promise<{ path: string }> {
  return post('/api/workbench/record', { action: 'stop', channelId });
}

export interface WorkbenchShortcut {
  name: string;
  command: string;
  description?: string;
  updated?: string;
}

export async function ListShortcuts(): Promise<WorkbenchShortcut[]> {
  return post('/api/workbench/shortcuts', { action: 'list' });
}

export async function SaveShortcut(sc: { name: string; command: string; description?: string }): Promise<void> {
  await post('/api/workbench/shortcuts', { action: 'save', ...sc });
}

export async function DeleteShortcut(name: string): Promise<void> {
  await post('/api/workbench/shortcuts', { action: 'delete', name });
}

// RunShortcut is withhold-aware, exactly like BroadcastWorkbench: Safe Mode ON
// returns { withheld: true } and the UI re-posts through the confirmed run.
export async function RunShortcut(
  name: string,
  channelIds: string[],
  confirmed = false,
): Promise<BroadcastResponse> {
  return post('/api/workbench/shortcuts', { action: 'run', name, channelIds, confirmed });
}

// W-F1: files over the plugin's files.session. The realm (allowed roots) is
// declared at open; upload is a single call that the host runs as the two-step
// acquisition (stage → commit) with both inverses tracked.

export async function OpenFilesSession(
  params: { host: string; port: number; user: string; password: string; roots?: string[] },
): Promise<{ sessionId: string }> {
  return post('/api/workbench/files', { action: 'open', ...params });
}

export interface FileEntry {
  name: string;
  size: number;
  mode: string;
  modTime: string;
  isDir: boolean;
}

export async function ListFiles(session: string, dir: string): Promise<{ entries: FileEntry[] }> {
  return post('/api/workbench/files', { action: 'list', session, path: dir });
}

export async function MkdirFiles(session: string, path: string): Promise<void> {
  await post('/api/workbench/files', { action: 'mkdir', session, path });
}

export async function RenameFiles(session: string, path: string, to: string): Promise<void> {
  await post('/api/workbench/files', { action: 'rename', session, path, to });
}

export async function RemoveFiles(session: string, path: string): Promise<void> {
  await post('/api/workbench/files', { action: 'remove', session, path });
}

// UploadFile posts the LOCAL PATH the browser staged (via a hidden file input
// the host reads directly — the desktop app shares a filesystem with the page).
export async function UploadFile(
  session: string,
  localPath: string,
  remotePath: string,
): Promise<{ path: string; size: number }> {
  return post('/api/workbench/files', { action: 'upload', session, localPath, path: remotePath });
}

export async function DownloadFile(
  session: string,
  path: string,
  localTarget: string,
): Promise<{ path: string; size: number }> {
  return post('/api/workbench/files', { action: 'download', session, path, localTarget });
}

export async function CloseFilesSession(session: string): Promise<void> {
  await post('/api/workbench/files', { action: 'close', session });
}
