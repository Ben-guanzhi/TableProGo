import React, { useCallback, useEffect, useState } from 'react';
import { FiFileText, FiPlus, FiSave, FiTrash2, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { DeleteNote, ListNotes, Note, SaveNote } from '../lib/workbench';

// W-N1: the host-local note surface. Notes are the operator's scratch space next
// to the data: one JSON file the HOST owns (no plugin, no process boundary), and a
// nav node the host registers itself — the plan's "宿主本地文档面，挂 nav.nodes".
//
// The editor is deliberately plain: a title and a Markdown body, saved
// explicitly (Save), with an unsaved marker. No autosave — a note surface that
// silently writes while the operator is mid-thought is a surprise, and this is
// local text, not a database.

const NotesPanel: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const [notes, setNotes] = useState<Note[]>([]);
  const [sel, setSel] = useState<string>('');
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const [dirty, setDirty] = useState(false);

  const load = useCallback(async () => {
    try {
      const list = await ListNotes();
      setNotes(list);
      return list;
    } catch (e) {
      toast.error((e as Error).message);
      return [];
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const select = (n: Note) => {
    if (dirty && !window.confirm('当前笔记有未保存的修改，丢弃？')) return;
    setSel(n.id);
    setTitle(n.title);
    setBody(n.body);
    setDirty(false);
  };

  const newNote = () => {
    if (dirty && !window.confirm('当前笔记有未保存的修改，丢弃？')) return;
    setSel('');
    setTitle('');
    setBody('');
    setDirty(false);
  };

  const save = async () => {
    try {
      const n = await SaveNote({ id: sel || undefined, title, body });
      setSel(n.id);
      setTitle(n.title);
      setDirty(false);
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const remove = async (n: Note) => {
    if (!window.confirm(`删除笔记「${n.title}」？`)) return;
    try {
      await DeleteNote(n.id);
      if (sel === n.id) {
        setSel('');
        setTitle('');
        setBody('');
      }
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="笔记"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-3xl h-[80vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiFileText size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">
            笔记 {dirty && <span className="text-2xs text-warning">（未保存）</span>}
          </span>
          <button className="p-1 text-secondary hover:text-ink" onClick={() => void newNote()} aria-label="新建笔记">
            <FiPlus size={13} />
          </button>
          <button className="p-1 text-secondary hover:text-ink" onClick={() => void save()} aria-label="保存笔记">
            <FiSave size={13} />
          </button>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="flex-1 min-h-0 flex">
          <div className="w-52 shrink-0 border-r border-line overflow-auto">
            {notes.length === 0 && <div className="p-2 text-2xs text-muted">还没有笔记。</div>}
            {notes.map(n => (
              <div
                key={n.id}
                className={`group flex items-center gap-1 px-2 py-1.5 text-2xs cursor-pointer border-b border-line ${
                  sel === n.id ? 'bg-raised text-ink' : 'text-secondary hover:bg-raised'
                }`}
                onClick={() => select(n)}
              >
                <span className="flex-1 truncate">{n.title}</span>
                <button
                  className="opacity-0 group-hover:opacity-100 text-secondary hover:text-danger"
                  onClick={e => {
                    e.stopPropagation();
                    void remove(n);
                  }}
                  aria-label={`删除笔记 ${n.title}`}
                >
                  <FiTrash2 size={10} />
                </button>
              </div>
            ))}
          </div>

          <div className="flex-1 min-w-0 flex flex-col">
            <input
              className="px-3 py-2 bg-panel border-b border-line text-sm text-ink font-medium focus:outline-none"
              placeholder="标题（留空则取正文首行）"
              value={title}
              onChange={e => {
                setTitle(e.target.value);
                setDirty(true);
              }}
              aria-label="笔记标题"
            />
            <textarea
              className="flex-1 min-h-0 px-3 py-2 bg-panel text-2xs text-ink font-mono resize-none focus:outline-none"
              placeholder="正文（Markdown）"
              value={body}
              onChange={e => {
                setBody(e.target.value);
                setDirty(true);
              }}
              aria-label="笔记正文"
            />
          </div>
        </div>
      </div>
    </div>
  );
};

export default NotesPanel;
