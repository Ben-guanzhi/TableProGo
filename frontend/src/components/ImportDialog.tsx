import { useEffect, useRef, useState } from 'react';
import {
  CancelQuery,
  GetSchemas,
  ImportFile,
  ImportOptions,
  ImportPreview,
  ImportRequest,
  ImportResult,
  PreviewImport,
} from '../api';
import { TableRef } from '../types';
import { FiUpload, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { useDialog } from '../dialog';
import { applyMapping, parseMappingText, validateMapping } from '../lib/importMapping';

interface ImportDialogProps {
  target: TableRef;
  onClose: () => void;
  onImported: () => void;
}

type Format = ImportOptions['format'];

// The extension the import should be read as. An empty string means "let the
// backend infer it from the path" — the same behaviour the single-path dialog had.
function detectFormat(path: string): Format {
  const match = path.trim().match(/\.([A-Za-z0-9]+)$/);
  if (!match) return '';
  const ext = match[1].toLowerCase();
  if (ext === 'csv') return 'csv';
  if (ext === 'json' || ext === 'ndjson') return 'json';
  if (ext === 'xlsx' || ext === 'xlsm') return ext;
  if (ext === 'sql') return 'sql';
  return '';
}

// ImportDialog asks for a file path and the handful of options the backend
// supports (import_db.go). The path is typed rather than picked because the
// HTTP API runs in the same process but the browser file input only yields a
// sandboxed name, not a path the backend could open.
//
// A multi-file import lists one path per line; a multi-sheet workbook gets one
// target-table row per worksheet. The preview button resolves the request and
// renders the SQL without writing, and its pairing can be edited into an
// authoritative column map before the real import runs.
const ImportDialog: React.FC<ImportDialogProps> = ({ target, onClose, onImported }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  // An import in flight must not be closed into a void: the guard keeps
  // Escape and the close button inert until the run answers.
  const [running, setRunning] = useState(false);
  useDialog(panelRef, () => {
    if (!running) onClose();
  });
  const [paths, setPaths] = useState<string[]>([]);
  const [pathDraft, setPathDraft] = useState('');
  const [options, setOptions] = useState<ImportOptions>({
    format: '',
    hasHeader: true,
    delimiter: ',',
    createTable: true,
    inferTypes: true,
    skipErrors: false,
    mode: 'append',
  });
  // Conflict keys stay a comma-separated string in the input; the API wants a list.
  const [upsertKeys, setUpsertKeys] = useState('');
  // Engines with a schema layer answer the namespace list; the rest return
  // nothing, and the import then keeps writing unqualified names.
  const [schemas, setSchemas] = useState<string[]>([]);
  const [schema, setSchema] = useState(target.schema);

  // One selected worksheet per target table, filled in from the first preview.
  const [sheets, setSheets] = useState<{ sheet: string; table: string; use: boolean }[]>([]);
  const [sheetsKnown, setSheetsKnown] = useState(false);
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [previewError, setPreviewError] = useState('');
  const [previewing, setPreviewing] = useState(false);

  // The column map is edited as text (`source => target`, a bare source skips).
  const [mappingText, setMappingText] = useState('');

  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    GetSchemas(target.connId, target.database)
      .then((list) => {
        if (cancelled) return;
        const names = list ?? [];
        setSchemas(names);
        // A target without a schema name falls back to the connection's first
        // namespace, so the select is never blank while the list is known.
        setSchema((s) => (s ? s : names[0] ?? ''));
      })
      .catch(() => { if (!cancelled) setSchemas([]); });
    return () => { cancelled = true; };
  }, [target.connId, target.database]);

  const firstPath = paths[0] ?? '';
  const format = options.format || detectFormat(firstPath);
  const isSQL = format === 'sql';
  const isXLSX = format === 'xlsx' || format === 'xlsm';
  // The sheet selector only makes sense for one workbook: with several files the
  // backend imports every worksheet of every workbook, which cannot be expressed
  // as one selection.
  const singleWorkbook = isXLSX && paths.length === 1;

  // Selecting sheets only when the same workbook stays selected.
  useEffect(() => {
    setSheets([]);
    setSheetsKnown(false);
    setPreview(null);
    setPreviewError('');
  }, [firstPath]);

  // The map is validated against the first previewed work unit: a multi-sheet
  // map applies to every worksheet, and this is the pairing the editor shows.
  const mappingUnit = preview?.tables[0];
  const mappingFileCols = mappingUnit?.columns ?? [];
  const mappingTargetCols = mappingUnit?.targetColumns ?? [];
  const mappingProblems = parseMappingText(mappingText).length > 0
    ? validateMapping(parseMappingText(mappingText), mappingFileCols)
    : [];

  const buildRequest = (): ImportRequest => {
    const req: ImportRequest = {
      connId: target.connId,
      database: target.database,
      schema,
      table: target.table,
      path: firstPath,
      options: {
        ...options,
        upsertKeys: options.mode === 'upsert'
          ? upsertKeys.split(',').map(k => k.trim()).filter(Boolean)
          : undefined,
      },
    };
    if (paths.length > 1) req.paths = paths;
    if (singleWorkbook && sheetsKnown && sheets.some((s) => s.use)) {
      req.sheets = sheets
        .filter((s) => s.use)
        .map((s) => ({ sheet: s.sheet, table: s.table }));
    }
    const entries = parseMappingText(mappingText);
    if (entries.length > 0) {
      const applied = applyMapping(entries, mappingFileCols.length > 0 ? mappingFileCols : entries.map((e) => e.source));
      if (Object.keys(applied.columnMap).length > 0) {
        req.columnMap = applied.columnMap;
      }
      if (applied.skipColumns.length > 0) req.skipColumns = applied.skipColumns;
    }
    return req;
  };

  const addPath = () => {
    const next = pathDraft.trim();
    if (!next) return;
    if (paths.includes(next)) {
      toast.error('该文件已在列表中');
      return;
    }
    setPaths([...paths, next]);
    setPathDraft('');
  };

  const removePath = (index: number) => {
    setPaths(paths.filter((_, i) => i !== index));
  };

  const runPreview = async () => {
    if (paths.length === 0) {
      toast.error('请输入文件路径');
      return;
    }
    setPreviewing(true);
    setPreviewError('');
    setError('');
    try {
      // The first preview of a workbook also reports its worksheets, so the
      // selector can offer one row per sheet with the backend's default names.
      if (singleWorkbook && !sheetsKnown) {
        const discovered = await PreviewImport(buildRequest());
        setSheets(discovered.tables.map((t) => ({ sheet: t.sheet ?? '', table: t.table, use: true })));
        setSheetsKnown(true);
        setPreview(discovered);
        return;
      }
      const pv = await PreviewImport(buildRequest());
      setPreview(pv);
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setPreviewError(message);
    } finally {
      setPreviewing(false);
    }
  };

  const autoFillMapping = () => {
    const pairs = (preview?.tables ?? [])
      .flatMap((t) => (t.mapped ?? []).map((m) => (m.table ? `${m.file} => ${m.table}` : m.file)));
    setMappingText(pairs.join('\n'));
  };

  const run = async () => {
    if (paths.length === 0) {
      toast.error('请输入文件路径');
      return;
    }
    setRunning(true);
    setError('');
    setResult(null);
    try {
      const res = await ImportFile(buildRequest());
      setResult(res);
      toast.success(`已导入 ${res.rows} 行${res.skipped ? `，跳过 ${res.skipped}` : ''}`);
      onImported();
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setError(message);
      toast.error(`导入失败：${message}`, { duration: 8000 });
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="import-dialog-title" className="bg-panel rounded-lg w-full max-w-2xl shadow-xl max-h-[92vh] flex flex-col">
        <div className="flex items-center justify-between px-6 py-4 border-b border-line shrink-0">
          <h2 id="import-dialog-title" className="text-lg font-semibold">
            导入数据 → {target.table}
          </h2>
          <button className="text-secondary hover:text-ink" onClick={() => { if (!running) onClose(); }} aria-label="关闭对话框">
            <FiX size={20} />
          </button>
        </div>

        <div className="p-6 space-y-4 overflow-y-auto">
          <div>
            <label htmlFor="import-path" className="block text-sm text-secondary mb-1">
              文件绝对路径{paths.length > 1 ? `（已添加 ${paths.length} 个文件）` : ''}
            </label>
            <div className="flex gap-2">
              <input
                id="import-path"
                autoFocus
                type="text"
                className="flex-1 px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={pathDraft}
                onChange={(e) => setPathDraft(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addPath(); } }}
                placeholder="C:\\data\\orders.csv"
              />
              <button
                type="button"
                className="px-3 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink"
                onClick={addPath}
              >
                添加
              </button>
            </div>
            {paths.length > 0 && (
              <ul className="mt-2 space-y-1">
                {paths.map((p, i) => (
                  <li key={p + i} className="flex items-center gap-2 text-sm">
                    <span className="text-ink">{p}</span>
                    <button
                      type="button"
                      className="text-secondary hover:text-danger"
                      onClick={() => removePath(i)}
                      aria-label={`移除 ${p}`}
                    >
                      <FiX size={14} />
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <p className="text-2xs text-muted mt-1">
              按扩展名区分 .csv / .json / .xlsx / .xlsm / .sql；可一次添加多个文件。
              .sql 会按语句顺序整脚本执行（忽略下方表相关选项）。
            </p>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label htmlFor="import-format" className="block text-sm text-secondary mb-1">文件格式</label>
              <select
                id="import-format"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={options.format}
                onChange={(e) => setOptions({ ...options, format: e.target.value as Format })}
              >
                <option value="">自动（按扩展名）</option>
                <option value="csv">CSV</option>
                <option value="json">JSON</option>
                <option value="xlsx">XLSX</option>
                <option value="xlsm">XLSM</option>
                <option value="sql">SQL 脚本</option>
              </select>
            </div>
            {schemas.length > 0 && (
              <div>
                <label htmlFor="import-schema" className="block text-sm text-secondary mb-1">目标 schema</label>
                <select
                  id="import-schema"
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                  value={schema}
                  onChange={(e) => setSchema(e.target.value)}
                >
                  {schemas.map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
              </div>
            )}
          </div>

          {!isSQL && (
            <>
              {isXLSX && singleWorkbook && sheetsKnown && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <label className="block text-sm text-secondary">工作表 → 目标表</label>
                    <span className="text-2xs text-muted">取消勾选可跳过该工作表</span>
                  </div>
                  <div className="space-y-2 max-h-56 overflow-y-auto rounded border border-line-strong bg-raised px-3 py-2">
                    {sheets.map((s) => (
                      <div key={s.sheet} className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          checked={s.use}
                          aria-label={`导入工作表 ${s.sheet}`}
                          onChange={(e) => setSheets(sheets.map((x) => (x.sheet === s.sheet ? { ...x, use: e.target.checked } : x)))}
                        />
                        <span className="text-sm text-secondary w-32 shrink-0 truncate" title={s.sheet}>{s.sheet}</span>
                        <input
                          type="text"
                          aria-label={`工作表 ${s.sheet} 的目标表名`}
                          className="flex-1 px-2 py-1 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                          value={s.table}
                          onChange={(e) => setSheets(sheets.map((x) => (x.sheet === s.sheet ? { ...x, table: e.target.value } : x)))}
                        />
                      </div>
                    ))}
                  </div>
                  <p className="text-2xs text-muted mt-1">
                    多工作表会分别导入到各自独立的事务；某个工作表失败不影响其余工作表。
                  </p>
                </div>
              )}
              {isXLSX && !singleWorkbook && (
                <p className="text-2xs text-muted">
                  一次导入多个 Excel 文件时，每个文件的全部工作表都会各自导入到以工作表名命名的表。
                </p>
              )}

              <div className="grid grid-cols-2 gap-3">
                <label className="flex items-center gap-2 text-sm text-secondary">
                  <input
                    type="checkbox"
                    checked={options.hasHeader}
                    disabled={isXLSX}
                    onChange={(e) => setOptions({ ...options, hasHeader: e.target.checked })}
                  />
                  首行是列名
                  {isXLSX && <span className="text-2xs text-muted">（Excel 首行始终视为列名）</span>}
                </label>
                <label className="flex items-center gap-2 text-sm text-secondary">
                  <input type="checkbox" checked={options.createTable} onChange={(e) => setOptions({ ...options, createTable: e.target.checked })} />
                  表不存在时自动建表
                </label>
                <label className="flex items-center gap-2 text-sm text-secondary">
                  <input type="checkbox" checked={options.inferTypes} onChange={(e) => setOptions({ ...options, inferTypes: e.target.checked })} />
                  推断列类型（INTEGER/REAL/TEXT）
                </label>
                <label className="flex items-center gap-2 text-sm text-secondary">
                  <input type="checkbox" checked={options.skipErrors} onChange={(e) => setOptions({ ...options, skipErrors: e.target.checked })} />
                  跳过失败行（否则整批回滚）
                </label>
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label htmlFor="import-delimiter" className="block text-sm text-secondary mb-1">CSV 分隔符</label>
                  <input
                    id="import-delimiter"
                    type="text"
                    maxLength={1}
                    disabled={isXLSX}
                    className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none disabled:opacity-40"
                    value={options.delimiter}
                    onChange={(e) => setOptions({ ...options, delimiter: e.target.value || ',' })}
                  />
                </div>
                <div>
                  <label htmlFor="import-mode" className="block text-sm text-secondary mb-1">写入模式</label>
                  <select
                    id="import-mode"
                    className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                    value={options.mode}
                    onChange={(e) => setOptions({ ...options, mode: e.target.value as ImportOptions['mode'] })}
                  >
                    <option value="append">追加</option>
                    <option value="truncate">先清空表</option>
                    <option value="upsert">存在则更新（upsert）</option>
                  </select>
                </div>
              </div>

              {options.mode === 'upsert' && (
                <div>
                  <label htmlFor="import-upsert-keys" className="block text-sm text-secondary mb-1">
                    冲突键（逗号分隔，留空 = 表的主键）
                  </label>
                  <input
                    id="import-upsert-keys"
                    type="text"
                    className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                    value={upsertKeys}
                    onChange={(e) => setUpsertKeys(e.target.value)}
                    placeholder="id"
                  />
                  <p className="text-2xs text-muted mt-1">
                    PostgreSQL / SQLite 生成 ON CONFLICT，MySQL 生成 ON DUPLICATE KEY UPDATE；SQL Server / Oracle 会明确报错，请改用追加。
                  </p>
                </div>
              )}

              <div>
                <div className="flex items-center justify-between mb-1">
                  <label htmlFor="import-mapping" className="block text-sm text-secondary">
                    列映射{mappingUnit ? `（${mappingUnit.table}）` : ''}
                  </label>
                  <div className="flex gap-2">
                    <button
                      type="button"
                      className="px-2 py-0.5 rounded text-2xs bg-raised hover:bg-emphasis text-ink border border-line-strong"
                      onClick={autoFillMapping}
                      disabled={!preview}
                    >
                      按预览自动匹配
                    </button>
                    <button
                      type="button"
                      className="px-2 py-0.5 rounded text-2xs bg-raised hover:bg-emphasis text-ink border border-line-strong"
                      onClick={() => setMappingText('')}
                    >
                      清空
                    </button>
                  </div>
                </div>
                <textarea
                  id="import-mapping"
                  rows={3}
                  className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none font-mono text-xs"
                  value={mappingText}
                  onChange={(e) => setMappingText(e.target.value)}
                  placeholder={'每行一条，source => target；只写 source 表示跳过该列\n例如：\nid => person_id\nname'}
                />
                <p className="text-2xs text-muted mt-1">
                  填写后即为权威映射：未列出的列会被跳过，不会自动匹配。留空则按列名自动匹配。
                  {mappingUnit && mappingUnit.exists
                    ? `当前表列：${mappingTargetCols.join('、') || '（无）'}`
                    : '目标表不存在时可自由命名目标列（建表用）。'}
                </p>
                {mappingProblems.length > 0 && (
                  <ul className="mt-1 space-y-0.5">
                    {mappingProblems.map((p) => (
                      <li key={p} className="text-2xs text-danger">{p}</li>
                    ))}
                  </ul>
                )}
              </div>
            </>
          )}

          {(previewError || error) && (
            <div className="text-sm text-danger bg-danger/10 border border-danger/30 rounded px-3 py-2 whitespace-pre-wrap">
              {previewError || error}
            </div>
          )}

          {preview && !error && (
            <div className="rounded border border-line bg-raised">
              <div className="px-3 py-2 border-b border-line-soft flex items-center justify-between">
                <span className="text-sm font-medium">预览</span>
                <span className="text-2xs text-muted">仅解析并生成 SQL，不写入数据库</span>
              </div>
              <div className="p-3 space-y-3 max-h-72 overflow-y-auto">
                {(preview.notes ?? []).map((n) => (
                  <p key={n} className="text-2xs text-muted">{n}</p>
                ))}
                {preview.tables.map((t, i) => (
                  <div key={`${t.table}-${i}`} className="border border-line-soft rounded p-2">
                    <div className="flex items-center gap-2 text-sm">
                      <span className="font-medium">{t.table}</span>
                      {t.sheet && <span className="text-2xs text-muted">← 工作表 {t.sheet}</span>}
                      <span className={`text-2xs rounded px-1.5 py-0.5 ${t.exists ? 'bg-warning/15 text-warning' : 'bg-accent/15 text-on-accent'}`}>
                        {t.exists ? '目标表已存在' : '将建表'}
                      </span>
                    </div>
                    <p className="text-2xs text-muted mt-1">
                      文件列：{t.columns.join('、') || '（空）'}
                    </p>
                    {(t.notes ?? []).map((n) => (
                      <p key={n} className="text-2xs text-warning mt-1">{n}</p>
                    ))}
                    {(t.mapped ?? []).some((m) => !m.table && m.suggested) && (
                      <ul className="mt-1 space-y-0.5">
                        {(t.mapped ?? []).filter((m) => !m.table && m.suggested).map((m) => (
                          <li key={m.file} className="text-2xs text-warning">
                            {m.file} <span className="text-muted">→ 可能是</span> {m.suggested}
                          </li>
                        ))}
                      </ul>
                    )}
                    {(t.ddl || (t.sample && t.sample.length > 0)) && (
                      <pre className="mt-1 p-2 bg-panel rounded border border-line-soft text-2xs overflow-x-auto whitespace-pre">
{`${t.ddl ?? ''}${t.ddl && t.sample && t.sample.length > 0 ? '\n' : ''}${(t.sample ?? []).join('\n')}`}
                      </pre>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {result && !error && (
            <div className="text-sm text-secondary bg-raised border border-line rounded px-3 py-2">
              已写入 {result.rows} 行，跳过 {result.skipped} 行，执行 {result.statements} 条语句
              {result.createdTable ? '，并创建了表' : ''}。
              {result.firstError && <span className="text-warning"> 首个错误：{result.firstError}</span>}
              {(result.mapped ?? []).some(m => !m.table && m.suggested) && (
                <div className="mt-2 pt-2 border-t border-line-soft text-2xs">
                  <p className="text-muted">以下文件列没有对应表列，未写入；名称相近的候选列供人工核对（本次未自动采用）：</p>
                  <ul className="mt-1 space-y-0.5">
                    {(result.mapped ?? [])
                      .filter(m => !m.table && m.suggested)
                      .map(m => (
                        <li key={m.file} className="text-warning">
                          {m.file} <span className="text-muted">→ 可能是</span> {m.suggested}
                        </li>
                      ))}
                  </ul>
                </div>
              )}
              {(result.mapped ?? []).some(m => !m.table && !m.suggested) && (
                <div className="mt-1 text-2xs text-muted">
                  未写入且无相近候选：
                  {(result.mapped ?? [])
                    .filter(m => !m.table && !m.suggested)
                    .map(m => m.file)
                    .join('、')}
                </div>
              )}
              {(result.tables ?? []).length > 1 && (
                <div className="mt-2 pt-2 border-t border-line-soft text-2xs">
                  <p className="text-muted">各工作表/文件结果：</p>
                  <ul className="mt-1 space-y-0.5">
                    {(result.tables ?? []).map((t, i) => (
                      <li key={`${t.table}-${i}`} className={t.error ? 'text-danger' : 'text-secondary'}>
                        {t.sheet ? `${t.sheet} → ` : ''}{t.table}：写入 {t.rows} 行{t.error ? `，失败（${t.error}）` : ''}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="flex flex-wrap justify-end gap-3 px-6 py-4 border-t border-line shrink-0">
          <button
            type="button"
            className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink disabled:opacity-40"
            onClick={onClose}
            disabled={running}
          >
            关闭
          </button>
          {!isSQL && (
            <button
              type="button"
              className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink disabled:opacity-40"
              onClick={runPreview}
              disabled={previewing || running}
            >
              {previewing ? '预览中…' : '预览'}
            </button>
          )}
          <button
            type="button"
            className={`flex items-center px-4 py-2 rounded text-sm ${
              running ? 'bg-emphasis text-secondary' : 'bg-accent hover:bg-accent-hover text-on-accent'
            }`}
            onClick={run}
            disabled={running}
          >
            <FiUpload size={14} className="mr-2" />
            {running ? '导入中…' : '开始导入'}
          </button>
          {running && (
            <button
              type="button"
              className="px-4 py-2 rounded text-sm border border-line text-secondary hover:text-ink hover:bg-raised"
              onClick={() => CancelQuery(target.connId, "import").catch(() => {})}
            >
              取消
            </button>
          )}
        </div>
      </div>
    </div>
  );
};

export default ImportDialog;
