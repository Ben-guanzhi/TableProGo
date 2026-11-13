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

// W-C1: containers ride the ssh tunnel (tunnel.proxy). The consumer plugin
// (navop-containers) stays Inactive until navop-ssh is loaded — so "open" can
// legitimately refuse with "the container plugin fiber is not active", which the
// panel surfaces verbatim.

export interface ContainerRow {
  id: string;
  name: string;
  image: string;
  state: string;
}

export async function OpenContainerSession(params: {
  host: string;
  port: number;
  user: string;
  password: string;
  dockerAddr?: string;
}): Promise<{ sessionId: string }> {
  return post('/api/workbench/containers', { action: 'open', ...params });
}

export async function ListContainers(session: string): Promise<{ containers: ContainerRow[] }> {
  return post('/api/workbench/containers', { action: 'ps', session });
}

export async function KubectlRun(session: string, args: string): Promise<{ output: string }> {
  return post('/api/workbench/containers', { action: 'kubectl', session, args });
}

export async function ContainerExec(session: string, container: string): Promise<{ channelId: string }> {
  return post('/api/workbench/containers', { action: 'exec', session, container });
}

export async function ContainerExecData(
  channelId: string,
  data: string,
): Promise<{ output: string; ended: boolean }> {
  return post('/api/workbench/containers', { action: 'exec-data', channelId, data });
}

export async function ContainerExecClose(channelId: string): Promise<void> {
  await post('/api/workbench/containers', { action: 'exec-close', channelId });
}

export async function CloseContainerSession(session: string): Promise<void> {
  await post('/api/workbench/containers', { action: 'close', session });
}

// W-Q1: MQTT. Publish is an EMISSION held behind Safe Mode: the gated call answers
// {withheld:true} and the UI re-posts through `publish-confirmed` — the same
// separate-endpoint discipline as the terminal broadcast.

export interface MqttMessage {
  topic: string;
  payload: string;
  at: string;
}

export interface MqttPublishResult {
  withheld?: boolean;
  published?: boolean;
  topic?: string;
  payload?: string;
  bytes?: number;
  warning?: string;
}

export interface MqttSubscription {
  tag: string;
  topic?: string;
}

export async function OpenMqttSession(params: {
  host: string;
  port: number;
  user?: string;
  password?: string;
}): Promise<{ sessionId: string }> {
  return post('/api/workbench/mqtt', { action: 'open', ...params });
}

export async function SubscribeTopic(session: string, topic: string): Promise<{ subscriptions: MqttSubscription[] }> {
  return post('/api/workbench/mqtt', { action: 'subscribe', session, topic });
}

export async function UnsubscribeTopic(
  session: string,
  topic: string,
): Promise<{ subscriptions: MqttSubscription[] }> {
  return post('/api/workbench/mqtt', { action: 'unsubscribe', session, topic });
}

export async function ListMqttSubscriptions(session: string): Promise<{ subscriptions: MqttSubscription[] }> {
  return post('/api/workbench/mqtt', { action: 'subscriptions', session });
}

export async function PublishTopic(
  session: string,
  topic: string,
  payload: string,
  confirmed = false,
): Promise<MqttPublishResult> {
  return post('/api/workbench/mqtt', {
    action: confirmed ? 'publish-confirmed' : 'publish',
    session,
    topic,
    payload,
  });
}

export async function MqttMessages(
  session: string,
  since: number,
): Promise<{ messages: MqttMessage[]; next: number; total: number }> {
  return post('/api/workbench/mqtt', { action: 'messages', session, since });
}

export async function CloseMqttSession(session: string): Promise<void> {
  await post('/api/workbench/mqtt', { action: 'close', session });
}

// W-N1: notes are host-local documents (no plugin, no key surface beyond the tab).
export interface Note {
  id: string;
  title: string;
  body: string;
  updated: string;
}

export async function ListNotes(): Promise<Note[]> {
  return post('/api/workbench/notes', { action: 'list' });
}

export async function SaveNote(n: { id?: string; title?: string; body: string }): Promise<Note> {
  return post('/api/workbench/notes', { action: 'save', ...n });
}

export async function DeleteNote(id: string): Promise<void> {
  await post('/api/workbench/notes', { action: 'delete', id });
}

// W-D1: remote desktops. RDP start is an EMISSION — the response says so and there
// is no undo; the .rdp temp file is the host's tracked compensation (deleted on
// unload). VNC rides the channel with base64 payloads.

export interface RdpStart {
  file: string;
  session: string;
  launched: boolean;
  target: string;
  notice: string;
}

export interface DesktopRow {
  tag: string;
  kind?: string;
  target?: string;
  file?: string;
}

export async function StartRdp(host: string, port: number, user?: string): Promise<RdpStart> {
  return post('/api/workbench/desktop', { action: 'rdp', host, port, user });
}

export async function OpenVnc(host: string, port: number): Promise<{ session: string; target: string }> {
  return post('/api/workbench/desktop', { action: 'vnc', host, port });
}

export async function VncRelay(
  session: string,
  base64Data: string,
): Promise<{ data: string; ended: boolean; bytes: number }> {
  return post('/api/workbench/desktop', { action: 'relay', session, data: base64Data });
}

export async function CloseDesktopSession(session: string): Promise<void> {
  await post('/api/workbench/desktop', { action: 'close', session });
}

export async function DesktopState(): Promise<{ sessions: DesktopRow[] }> {
  return post('/api/workbench/desktop', { action: 'state' });
}
