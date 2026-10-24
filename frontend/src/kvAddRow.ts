// Sets and streams have no writable value column: SADD and XADD add a row.
// This turns the add-row dialog's draft into the row the backend appends, so
// the validation rules stay out of the component and can be tested directly.
export interface KVFieldDraft {
  name: string;
  value: string;
}

export interface KVAddRowDraft {
  member: string;
  id: string;
  fields: KVFieldDraft[];
}

export interface KVAddRow {
  member?: string;
  id?: string;
  fields?: KVFieldDraft[];
}

export function kvAddRow(
  type: string,
  draft: KVAddRowDraft,
): { error?: string; row?: KVAddRow } {
  if (type === 'set') {
    const member = draft.member.trim();
    if (!member) return { error: '成员不能为空' };
    return { row: { member } };
  }
  if (type === 'stream') {
    const fields: KVFieldDraft[] = [];
    const seen = new Set<string>();
    for (const field of draft.fields) {
      // A row the user never touched is ignored. A value without a name would
      // be stored under an empty field name, which XADD accepts but the grid
      // cannot show back as anything meaningful.
      if (!field.name.trim() && !field.value) continue;
      const name = field.name.trim();
      if (!name) return { error: '字段名不能为空' };
      if (seen.has(name)) return { error: `字段 ${name} 重复` };
      seen.add(name);
      fields.push({ name, value: field.value });
    }
    if (fields.length === 0) return { error: '至少需要一个字段' };
    // An empty id asks Redis to assign one.
    return { row: { id: draft.id.trim(), fields } };
  }
  return { error: '该类型不支持追加行' };
}
