import { useEffect, useRef, useState } from "react";
import { api, apiUpload } from "../api";
import { Card, ErrorState, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconDownload } from "../icons";

type Item = { platform: string; label: string; body: string; edited: boolean };
type Resp = { items: Item[]; version: string; placeholders: string[] };

export default function Instructions() {
  const list = useAsync<Resp>(() => api("/instructions"), []);
  const [active, setActive] = useState("");
  const [body, setBody] = useState("");
  const [dirty, setDirty] = useState(false);
  const [html, setHtml] = useState("");
  const [busy, setBusy] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const toast = useToast();

  // First platform once, then whatever the operator picks.
  useEffect(() => {
    if (!active && list.data?.items.length) {
      setActive(list.data.items[0].platform);
      setBody(list.data.items[0].body);
    }
  }, [list.data, active]);

  // Preview follows the text with a short pause, so typing stays smooth.
  useEffect(() => {
    if (!body) { setHtml(""); return; }
    const t = setTimeout(async () => {
      try {
        const r = await api<{ html: string }>("/instructions/preview", { method: "POST", body: { body } });
        setHtml(r.html);
      } catch { /* превью не критично */ }
    }, 350);
    return () => clearTimeout(t);
  }, [body]);

  const pick = (p: Item) => {
    if (dirty && !confirm("Несохранённые правки пропадут. Переключить платформу?")) return;
    setActive(p.platform); setBody(p.body); setDirty(false);
  };

  const save = async () => {
    setBusy(true);
    try {
      await api(`/instructions/${active}`, { method: "PUT", body: { body } });
      setDirty(false);
      toast({ kind: "ok", title: "Инструкция сохранена", body: "Пользователи увидят её сразу — перезапуск не нужен." });
      list.reload();
    } catch (e) { toast({ kind: "bad", title: "Не удалось сохранить", body: errText(e) }); }
    finally { setBusy(false); }
  };

  const insert = (text: string) => {
    const el = ta.current;
    if (!el) { setBody((b) => b + text); setDirty(true); return; }
    const s = el.selectionStart, e = el.selectionEnd;
    setBody((b) => b.slice(0, s) + text + b.slice(e));
    setDirty(true);
    requestAnimationFrame(() => { el.focus(); el.selectionStart = el.selectionEnd = s + text.length; });
  };

  const upload = async (file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    try {
      const r = await apiUpload<{ markdown: string }>("/instruction-assets", fd);
      insert("\n\n" + r.markdown + "\n\n");
      toast({ kind: "ok", title: "Картинка добавлена" });
    } catch (e) { toast({ kind: "bad", title: "Не удалось загрузить", body: errText(e) }); }
  };

  if (list.loading) return <Spinner />;
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">доступ</div><h1>Инструкции</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={save} disabled={busy || !dirty}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </div>

      <Notice kind="info" title="Это то, что читает пользователь на своей странице">
        Текст общий, а адреса подставляются персонально для каждого устройства. Правки
        видны сразу — перезапускать ничего не нужно.
      </Notice>

      <div className="seg" style={{ marginBottom: 12 }}>
        {list.data!.items.map((p) => (
          <button key={p.platform} type="button"
            className={`seg-btn${active === p.platform ? " sel" : ""}`}
            onClick={() => pick(p)}>
            {p.label}{!p.edited && <span className="muted"> ·&nbsp;по умолчанию</span>}
          </button>
        ))}
      </div>

      <div className="grid g2">
        <Card title="Текст" eyebrow="markdown">
          <div className="row" style={{ gap: 6, marginBottom: 8, flexWrap: "wrap" }}>
            {list.data!.placeholders.map((ph) => (
              <button key={ph} className="btn sm ghost mono" onClick={() => insert(ph)} title="Вставить подстановку">
                {ph}
              </button>
            ))}
            <label className="btn sm ghost" style={{ cursor: "pointer" }}>
              <IconDownload />Картинка
              <input type="file" accept="image/*" hidden
                onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = ""; }} />
            </label>
          </div>
          <textarea ref={ta} className="textarea mono" style={{ minHeight: 420 }} value={body}
            onChange={(e) => { setBody(e.target.value); setDirty(true); }}
            onDrop={(e) => {
              const f = e.dataTransfer.files?.[0];
              if (f && f.type.startsWith("image/")) { e.preventDefault(); upload(f); }
            }} />
          <p className="muted small" style={{ marginTop: 8 }}>
            Подстановки заменяются на личные значения устройства. Картинку можно перетащить прямо сюда.
          </p>
        </Card>

        <Card title="Предпросмотр" eyebrow="как это увидит человек">
          <div className="howto-preview" dangerouslySetInnerHTML={{ __html: html }} />
        </Card>
      </div>
    </>
  );
}
