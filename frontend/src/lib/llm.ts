import { post } from '../api.ts';

// The AI assistant's provider settings. The API key never leaves the machine
// except to the provider it is addressed to; it is stored in its own file with
// owner-only permissions, not in the shared settings.
export interface LlmConfig {
  provider: string;
  baseURL: string;
  apiKey: string;
  model: string;
  enabled: boolean;
  // llmAllowProposals lets the assistant propose statements that change data.
  // The proposal waits for the user; it is never run by the assistant.
  llmAllowProposals?: boolean;
}

export interface LlmProvider {
  ID: string;
  Label: string;
  RequiresKey: boolean;
  DefaultBaseURL: string;
  DefaultModel: string;
}

export interface LlmMessage {
  role: 'user' | 'assistant';
  content: string;
}

// A statement the model proposed. It is NOT running: the user decides.
export interface LlmProposal {
  id: string;
  connection: string;
  database: string;
  sql: string;
  reason: string;
}

export const defaultLlmConfig: LlmConfig = {
  provider: 'openai',
  baseURL: '',
  apiKey: '',
  model: '',
  enabled: false,
};

// LoadLlmConfig reads the provider settings and the list of presets.
export async function LoadLlmConfig(): Promise<{ config: LlmConfig; providers: LlmProvider[] }> {
  const res = await post('/api/llm/config', {});
  return {
    config: { ...defaultLlmConfig, ...(res?.config ?? {}) },
    providers: (res?.providers ?? []) as LlmProvider[],
  };
}

// SaveLlmConfig persists the provider settings.
export async function SaveLlmConfig(config: LlmConfig): Promise<void> {
  await post('/api/llm/config', { config, save: true });
}

// AskLlm asks the assistant and returns its answer.
export async function AskLlm(question: string): Promise<string> {
  const res = await post('/api/llm/ask', { question });
  return (res?.answer ?? '') as string;
}

// LoadLlmProposals lists the statements waiting for the user.
export async function LoadLlmProposals(): Promise<LlmProposal[]> {
  const res = await post('/api/llm/proposals', {});
  return (res?.proposals ?? []) as LlmProposal[];
}

// AcceptLlmProposal runs a proposed statement the user approved.
//
// The result is the ordinary QueryResult, so a statement Safe Mode refused comes
// back as its error rather than as a failed request: the user refused it, not the
// call.
export async function AcceptLlmProposal(id: string): Promise<{ error?: string; rowCount?: number }> {
  const res = await post('/api/llm/proposal/accept', { id });
  const result = res?.result ?? {};
  const error = typeof result.Error === 'string' && result.Error ? result.Error : undefined;
  return { error, rowCount: result.RowCount };
}

// RejectLlmProposal drops a proposed statement without running it.
export async function RejectLlmProposal(id: string): Promise<void> {
  await post('/api/llm/proposal/reject', { id });
}

// LoadLlmHistory reads the remembered conversation.
export async function LoadLlmHistory(): Promise<LlmMessage[]> {
  const res = await post('/api/llm/history', {});
  return (res?.history ?? []) as LlmMessage[];
}

// ClearLlmHistory forgets the conversation.
export async function ClearLlmHistory(): Promise<void> {
  await post('/api/llm/clear', {});
}
