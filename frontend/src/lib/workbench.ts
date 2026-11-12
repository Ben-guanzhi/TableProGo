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
