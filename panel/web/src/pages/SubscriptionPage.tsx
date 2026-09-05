import { useEffect, useRef, useState } from "react";
import { api, apiUpload, ago, timeTitle } from "../api";
import {
  Card, Copyable, ErrorState, Field, Modal, Notice, Segmented, Spinner,
  errText, useAsync, useToast,
} from "../ui";
import { IconDownload, IconKey } from "../icons";
import type { Me } from "../App";

/**
 * Everything about the public page a person opens by their personal link: its
 * address and look, the key the page service authenticates with, and the texts
 * it shows. One page, because these are the same object seen from three sides —
 * splitting them made the operator hunt for "where do I change that".
 */
export default function SubscriptionPage({ me }: { me: Me }) {
  const [tab, setTab] = useState<"page" | "howto">("page");

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">доступ</div><h1>Страница подписки</h1></div>
      </div>

      <Segmented value={tab} onChange={setTab} options={[
        { value: "page", label: "Настройка страницы", hint: "Адрес, оформление, лимит и ключ доступа сервиса." },
        { value: "howto", label: "Инструкции", hint: "Тексты, которые человек читает у себя на странице." },
      ]} />

      <div style={{ marginTop: 14 }}>
        {tab === "page" ? <PageSettings owner={me.user.role === "owner"} /> : <Howtos />}
      </div>
    </>
  );
}

type SettingsResp = { settings: Record<string, any> };

function PageSettings({ owner }: { owner: boolean }) {
  const s = useAsync<SettingsResp>(() => api("/settings"), []);
  const [url, setUrl] = useState("");
  const [brand, setBrand] = useState("");
  const [support, setSupport] = useState("");
  const [limit, setLimit] = useState("3");
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => {
    if (!s.data) return;
    const g = s.data.settings;
    setUrl(g.subscription_page_url ?? "");
    setBrand(g.subscription_brand ?? "");
    setSupport(g.subscription_support ?? "");
    setLimit(String(g.device_limit_default ?? 3));
  }, [s.data]);

  if (s.loading) return <Spinner />;
  if (s.error) return <ErrorState message={s.error} onRetry={s.reload} />;

  const trimmed = url.trim().replace(/\/+$/, "");
  const urlError = trimmed === "" || /^https?:\/\/[^\s/]+/.test(trimmed)
    ? "" : "Нужен полный адрес со схемой, например https://my.example.com";
  const insecure = trimmed.startsWith("http://");
  const limitNum = Number(limit);
  const limitError = !Number.isInteger(limitNum) || limitNum < 1 ? "Целое число, минимум 1" : "";

  const save = async () => {
    setBusy(true);
    try {
      await api("/settings", {
        method: "PUT",
        body: {
          subscription_page_url: trimmed,
          subscription_brand: brand.trim(),
          subscription_support: support.trim(),
          device_limit_default: limitNum,
        },
      });
      toast({ kind: "ok", title: "Сохранено", body: "Страница подхватит изменения сразу — перезапуск не нужен." });
      s.reload();
    } catch (e) { toast({ kind: "bad", title: "Не удалось сохранить", body: errText(e) }); }
    finally { setBusy(false); }
  };

  return (
    <>
      <Card title="Адрес и оформление" eyebrow="что видит человек"
        actions={
          <button className="btn primary" onClick={save} disabled={busy || !!urlError || !!limitError}>
            {busy ? <span className="spin" /> : null}Сохранить
          </button>
        }>
        <Field label="Адрес страницы" error={urlError}
          hint="Туда, где запущен сервис страницы подписки. Личная ссылка собирается как адрес/идентификатор.">
          <input className="input mono" value={url} placeholder="https://my.nolim.online"
            onChange={(e) => setUrl(e.target.value)} />
        </Field>
        {insecure && (
          <Notice kind="warn" title="Адрес без TLS">
            По http ссылку и содержимое страницы видит любой провайдер на пути. Поставьте страницу за https.
          </Notice>
        )}
        {trimmed && !urlError && (
          <p className="muted small">Ссылка будет выглядеть так: <code className="mono">{trimmed}/Ab3xK9pQrS7tU2vW</code></p>
        )}
        <div className="grid g2" style={{ marginTop: 12 }}>
          <Field label="Имя сервиса" hint="Строка над заголовком и в заголовке вкладки. Пусто — «Мои устройства».">
            <input className="input" value={brand} placeholder="Nolim DNS"
              onChange={(e) => setBrand(e.target.value)} />
          </Field>
          <Field label="Поддержка" hint="Телеграм вида @nick или ссылка. Пусто — блок не показывается.">
            <input className="input" value={support} placeholder="@nolim_support"
              onChange={(e) => setSupport(e.target.value)} />
          </Field>
        </div>
        <Field label="Лимит устройств по умолчанию" error={limitError}
          hint="Действует на пользователей без личного лимита.">
          <input className="input num" type="number" min={1} value={limit}
            onChange={(e) => setLimit(e.target.value)} />
        </Field>
      </Card>

      {owner && <APIKeys />}
    </>
  );
}

type Key = { id: string; name: string; scopes: string[]; last_used_at: string | null; revoked_at: string | null };

function APIKeys() {
  const list = useAsync<{ items: Key[]; scopes: string[] }>(() => api("/api-keys"), []);
  const [open, setOpen] = useState(false);
  const [issued, setIssued] = useState<string | null>(null);
  const toast = useToast();

  return (
    <Card title="Ключи доступа" eyebrow="чем сервис страницы ходит в панель">
      <Notice kind="info" title="Ключ ограничен правами">
        Сервису страницы выдаётся ровно то, что ему нужно: читать пользователя и управлять его
        устройствами. До нод, сервисов и настроек он не дотянется.
      </Notice>
      {list.loading ? <Spinner /> : (
        <div className="table-wrap" style={{ marginTop: 12 }}>
          <table className="table">
            <thead><tr><th>Название</th><th>Права</th><th style={{ width: 140 }}>Использован</th><th /></tr></thead>
            <tbody>
              {list.data?.items.length ? list.data.items.map((k) => (
                <tr key={k.id}>
                  <td>{k.name}{k.revoked_at && <span className="badge" style={{ marginLeft: 8 }}>отозван</span>}</td>
                  <td className="tiny mono dim">{k.scopes.join(", ")}</td>
                  <td className="small dim" title={timeTitle(k.last_used_at)}>{k.last_used_at ? ago(k.last_used_at) : "ни разу"}</td>
                  <td className="actions">
                    {!k.revoked_at && (
                      <button className="btn sm ghost danger" onClick={async () => {
                        try { await api(`/api-keys/${k.id}`, { method: "DELETE" }); list.reload(); }
                        catch (e) { toast({ kind: "bad", title: "Не удалось отозвать", body: errText(e) }); }
                      }}>Отозвать</button>
                    )}
                  </td>
                </tr>
              )) : <tr><td colSpan={4} className="muted small">Ключей пока нет.</td></tr>}
            </tbody>
          </table>
        </div>
      )}
      <button className="btn" style={{ marginTop: 12 }} onClick={() => setOpen(true)}><IconKey />Выпустить ключ</button>

      {open && <NewKey scopes={list.data?.scopes ?? []} onClose={() => setOpen(false)}
        onDone={(secret) => { setOpen(false); setIssued(secret); list.reload(); }} />}
      {issued && (
        <Modal title="Ключ выпущен" onClose={() => setIssued(null)}
          footer={<button className="btn primary" onClick={() => setIssued(null)}>Я сохранил ключ</button>}>
          <Notice kind="warn" title="Показывается один раз">
            В базе хранится только хеш. Потеряете — выпустите новый.
          </Notice>
          <div className="codeblock mono" style={{ wordBreak: "break-all" }}>{issued}</div>
          <Copyable value={issued} />
        </Modal>
      )}
    </Card>
  );
}

function NewKey({ scopes, onClose, onDone }: { scopes: string[]; onClose: () => void; onDone: (secret: string) => void }) {
  const [name, setName] = useState("Страница подписки");
  const [picked, setPicked] = useState<string[]>(scopes);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const toggle = (s: string) =>
    setPicked((p) => (p.includes(s) ? p.filter((x) => x !== s) : [...p, s]));

  return (
    <Modal title="Новый ключ" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy || !name.trim() || picked.length === 0} onClick={async () => {
          setBusy(true); setError("");
          try {
            const r = await api<{ secret: string }>("/api-keys", { method: "POST", body: { name, scopes: picked } });
            onDone(r.secret);
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>Выпустить</button>
      </>
    }>
      <Field label="Название" hint="Чтобы понимать, кому он выдан.">
        <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Права" error={error}>
        <div className="col" style={{ gap: 8 }}>
          {scopes.map((s) => (
            <label className="check" key={s}>
              <input type="checkbox" checked={picked.includes(s)} onChange={() => toggle(s)} />
              <span className="mono">{s}</span>
              <span className="muted small" style={{ marginLeft: 8 }}>{scopeHint(s)}</span>
            </label>
          ))}
        </div>
      </Field>
    </Modal>
  );
}

function scopeHint(s: string) {
  switch (s) {
    case "sub:read": return "читать пользователя и его устройства";
    case "sub:devices": return "добавлять и удалять устройства";
    case "sub:instructions": return "читать тексты инструкций";
    default: return "";
  }
}

type Item = { platform: string; label: string; body: string; edited: boolean };
type Resp = { items: Item[]; version: string; placeholders: string[] };

function Howtos() {
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
      toast({ kind: "ok", title: "Инструкция сохранена", body: "Люди увидят её сразу — перезапуск не нужен." });
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
      <div className="row" style={{ marginBottom: 12 }}>
        <Notice kind="info" title="Текст общий, адреса — личные">
          Подстановки заменяются на значения конкретного устройства. Правки видны сразу.
        </Notice>
        <div className="spacer" />
        <button className="btn primary" onClick={save} disabled={busy || !dirty}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </div>

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
