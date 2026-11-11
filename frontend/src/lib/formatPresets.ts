// W2-c: the format presets the editor offers. The layouts themselves live in
// the Go formatter (internal/sqlformat), which owns the only tokenizer; this
// module is the frontend's label/order table plus the one rule that decides
// which preset an unknown stored value means.
//
// Keeping the list here (and not inline in the toolbar) is what lets a test
// assert that every offered preset is one the backend recognises — a dropdown
// entry the server would silently map to "default" is a lie in the UI.

export type FormatPreset = 'default' | 'compact' | 'expanded';

export interface FormatPresetOption {
  id: FormatPreset;
  label: string;
  hint: string;
}

// FORMAT_PRESETS is the order the dropdown shows: the historical layout first,
// then the two variations.
export const FORMAT_PRESETS: readonly FormatPresetOption[] = [
  {
    id: 'default',
    label: '默认',
    hint: '每个主子句一行，SELECT 列表逐项换行',
  },
  {
    id: 'compact',
    label: '紧凑',
    hint: '只在 FROM/WHERE/GROUP/ORDER 前换行，SELECT 列表保持一行',
  },
  {
    id: 'expanded',
    label: '展开',
    hint: '与默认相同的换行，缩进加宽到四空格',
  },
];

// normalizePreset maps a stored or untrusted name to a known preset. An empty
// or unknown value means "default", matching the backend's ParsePreset so the
// two sides can never disagree about what an empty setting formats as.
export const normalizePreset = (name: string | undefined | null): FormatPreset => {
  const folded = (name ?? '').trim().toLowerCase();
  const found = FORMAT_PRESETS.find(p => p.id === folded);
  return found ? found.id : 'default';
};

// presetLabel is the human name for a preset id (the toolbar's current-value
// display); an unknown id renders as the default's label, never as raw text.
export const presetLabel = (name: string | undefined | null): string =>
  FORMAT_PRESETS.find(p => p.id === normalizePreset(name))!.label;

// presetHint is the dropdown's one-line explanation of what a preset does.
export const presetHint = (name: string | undefined | null): string =>
  FORMAT_PRESETS.find(p => p.id === normalizePreset(name))!.hint;
