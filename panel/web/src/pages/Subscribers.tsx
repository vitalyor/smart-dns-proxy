import { useState } from "react";
import { api, ago, timeTitle } from "../api";
import {
  Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Spinner,
  errText, useAsync, useToast,
} from "../ui";
import { IconPlus, IconTrash, IconRotate, IconKey } from "../icons";
import type { Me } from "../App";

type Sub = {
  id: string; name: string; note: string; short_id: string;
  enabled: boolean; expires_at: string | null;
  device_limit: number | null; query_limit: number | null;
  query_period: string; queries_used: number;
  created_at: string;
  url: string; device_count: number; effective_device_limit: number;
};
type ListResp = { items: Sub[]; device_limit_default: number; page_url_configured: boolean };

const expired = (s: Sub) => s.expires_at != null && new Date(s.expires_at) < new Date();
const overQuota = (s: Sub) => s.query_limit != null && s.queries_used >= s.query_limit;
const active = (s: Sub) => s.enabled && !expired(s) && !overQuota(s);

export default function Subscribers({ me }: { me: Me }) {
  const list = useAsync<ListResp>(() => api("/subscribers"), []);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Sub | null>(null);
  const [rotating, setRotating] = useState<Sub | null>(null);
  const [removing, setRemoving] = useState<Sub | null>(null);
  const toast = useToast();

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">доступ</div><h1>Подписчики</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Добавить</button>
      </div>

      <Notice kind="info" title="Как это работает">
        Каждому человеку выдаётся личная ссылка. По ней он открывает свою страницу, где видит
        устройства, добавляет новые и скачивает настройки. Регистрации нет — подписчиков заводите вы.
      </Notice>

      {list.data && !list.data.page_url_configured && (
        <Notice kind="warn" title="Не задан адрес страницы подписки">
          Ссылки не собираются, пока в Настройках пуст параметр <code className="mono">subscription_page_url</code>.
          Укажите там адрес, по которому будет доступна публичная страница.
        </Notice>
      )}

      {list.loading ? <Spinner />
        : list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.data!.items.length === 0 ? (
          <Card tight>
            <div className="empty">
              <h3>Подписчиков пока нет</h3>
              <p className="muted small">Заведите первого — панель выдаст ссылку, которую можно отправить человеку.</p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating(true)}>
                <IconPlus />Добавить
              </button>
            </div>
          </Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>Подписчик</th><th style={{ width: 90 }}>Статус</th>
                    <th style={{ width: 110 }}>Устройства</th><th style={{ width: 150 }}>Запросы</th>
                    <th style={{ width: 130 }}>Срок</th><th />
                  </tr>
                </thead>
                <tbody>
                  {list.data!.items.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{s.name}</div>
                        {s.note && <div className="tiny dim">{s.note}</div>}
                        {s.url
                          ? <div className="row" style={{ gap: 6, marginTop: 4 }}>
                              <span className="mono tiny dim" style={{ wordBreak: "break-all" }}>{s.url}</span>
                              <Copyable value={s.url} label="Копировать" />
                            </div>
                          : <div className="tiny dim mono">{s.short_id}</div>}
                      </td>
                      <td>
                        {active(s) ? <span className="badge ok">активен</span>
                          : !s.enabled ? <span className="badge">выключен</span>
                          : expired(s) ? <span className="badge warn">истёк</span>
                          : <span className="badge warn">лимит</span>}
                      </td>
                      <td className="num small">
                        {s.device_count} / {s.effective_device_limit}
                        {s.device_limit == null && <div className="tiny dim">общий лимит</div>}
                      </td>
                      <td className="num small">
                        {s.queries_used.toLocaleString("ru-RU")}
                        <div className="tiny dim">
                          {s.query_limit == null ? "безлимит" : `из ${s.query_limit.toLocaleString("ru-RU")} · ${periodLabel(s.query_period)}`}
                        </div>
                      </td>
                      <td className="small dim" title={timeTitle(s.expires_at)}>
                        {s.expires_at ? ago(s.expires_at) : "бессрочно"}
                      </td>
                      <td className="actions">
                        <button className="btn sm ghost" onClick={() => setEditing(s)}>Изменить</button>
                        <button className="btn sm ghost" onClick={() => setRotating(s)} title="Сменить ссылку">
                          <IconRotate />
                        </button>
                        <button className="btn sm ghost danger" aria-label={`Удалить ${s.name}`}
                          onClick={() => setRemoving(s)}><IconTrash /></button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

      {me.user.role === "owner" && <APIKeys />}

      {creating && <SubForm onClose={() => setCreating(false)}
        onDone={() => { setCreating(false); list.reload(); }} />}
      {editing && <SubForm sub={editing} onClose={() => setEditing(null)}
        onDone={() => { setEditing(null); list.reload(); }} />}
      {rotating && <RotateModal sub={rotating} onClose={() => setRotating(null)}
        onDone={() => { setRotating(null); list.reload(); }} />}
      {removing && (
        <Confirm title={`Удалить ${removing.name}?`} danger confirmLabel="Удалить"
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/subscribers/${removing.id}`, { method: "DELETE" });
              setRemoving(null); list.reload();
            } catch (e) { toast({ kind: "bad", title: "Не удалось удалить", body: errText(e) }); }
          }}
          body={<>Ссылка перестанет работать, все устройства подписчика и их доступ будут удалены. Отменить нельзя.</>} />
      )}
    </>
  );
}

function periodLabel(p: string) {
  return p === "day" ? "в сутки" : p === "month" ? "в месяц" : "всего";
}

function SubForm({ sub, onClose, onDone }: { sub?: Sub; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(sub?.name ?? "");
  const [note, setNote] = useState(sub?.note ?? "");
  const [enabled, setEnabled] = useState(sub?.enabled ?? true);
  const [devLimit, setDevLimit] = useState(sub?.device_limit?.toString() ?? "");
  const [qLimit, setQLimit] = useState(sub?.query_limit?.toString() ?? "");
  const [period, setPeriod] = useState(sub?.query_period ?? "month");
  const [expires, setExpires] = useState(sub?.expires_at ? sub.expires_at.slice(0, 10) : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    setBusy(true); setError("");
    const body = {
      name, note, enabled,
      device_limit: devLimit === "" ? null : Number(devLimit),
      query_limit: qLimit === "" ? null : Number(qLimit),
      query_period: period,
      expires_at: expires === "" ? "" : new Date(expires + "T23:59:59Z").toISOString(),
    };
    try {
      if (sub) await api(`/subscribers/${sub.id}`, { method: "PATCH", body });
      else await api("/subscribers", { method: "POST", body });
      onDone();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={sub ? `Изменить ${sub.name}` : "Новый подписчик"} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy || !name.trim()} onClick={save}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </>
    }>
      <Field label="Имя" hint="Как вы узнаете этого человека в списке.">
        <input className="input" autoFocus value={name} placeholder="Иван, племянник"
          onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Заметка" hint="Необязательно.">
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
      <div className="grid g2">
        <Field label="Лимит устройств" hint="Пусто — общий лимит из Настроек.">
          <input className="input num" type="number" min={1} value={devLimit} placeholder="общий"
            onChange={(e) => setDevLimit(e.target.value)} />
        </Field>
        <Field label="Действует до" hint="Пусто — бессрочно.">
          <input className="input" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </Field>
      </div>
      <div className="grid g2">
        <Field label="Лимит запросов" hint="Пусто — без ограничения. Телефон делает тысячи запросов в сутки.">
          <input className="input num" type="number" min={1} value={qLimit} placeholder="безлимит"
            onChange={(e) => setQLimit(e.target.value)} />
        </Field>
        <Field label="Период сброса">
          <select className="select" value={period} onChange={(e) => setPeriod(e.target.value)}>
            <option value="month">в месяц</option>
            <option value="day">в сутки</option>
            <option value="never">не сбрасывать</option>
          </select>
        </Field>
      </div>
      {sub && (
        <label className="check">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Подписка включена
        </label>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

// RotateModal offers the two recoveries separately, because they cost different
// things: a new link stops further access, re-minting configs also kills what was
// already downloaded — at the price of reinstalling every profile.
function RotateModal({ sub, onClose, onDone }: { sub: Sub; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ url: string; devices_rotated: number } | null>(null);
  const toast = useToast();

  const run = async (reset: boolean) => {
    setBusy(true);
    try {
      const r = await api<{ url: string; devices_rotated: number }>(
        `/subscribers/${sub.id}/rotate`, { method: "POST", body: { reset_devices: reset } });
      setResult(r);
    } catch (e) { toast({ kind: "bad", title: "Не удалось", body: errText(e) }); }
    finally { setBusy(false); }
  };

  if (result) {
    return (
      <Modal title="Ссылка сменена" onClose={() => { onDone(); }}
        footer={<button className="btn primary" onClick={onDone}>Готово</button>}>
        <Notice kind="info" title="Новый адрес страницы">
          Отправьте его подписчику — старый больше не работает.
          {result.devices_rotated > 0 && <> Перевыпущено конфигов: {result.devices_rotated}; их придётся переустановить на устройствах.</>}
        </Notice>
        <div className="codeblock mono">{result.url}</div>
        <Copyable value={result.url} />
      </Modal>
    );
  }

  return (
    <Modal title={`Сменить ссылку — ${sub.name}`} onClose={onClose}
      footer={<button className="btn" onClick={onClose}>Отмена</button>}>
      <Notice kind="warn" title="Смена ссылки не отзывает то, что уже скачали">
        Если конфиги успели утечь, новый адрес закроет доступ к странице, но установленные профили
        продолжат работать. Чтобы убить и их, нужен второй вариант.
      </Notice>
      <div className="col" style={{ gap: 12, marginTop: 14 }}>
        <div>
          <button className="btn" disabled={busy} onClick={() => run(false)}>Только ссылка</button>
          <p className="muted small" style={{ marginTop: 6 }}>
            Адрес страницы меняется, устройства продолжают работать. Для случая «ссылка засветилась».
          </p>
        </div>
        <div>
          <button className="btn danger" disabled={busy} onClick={() => run(true)}>Ссылка и все конфиги</button>
          <p className="muted small" style={{ marginTop: 6 }}>
            Плюс перевыпуск токенов всех устройств: утекшее умирает, но профили придётся
            переустановить везде. Для случая «доступом поделились».
          </p>
        </div>
      </div>
    </Modal>
  );
}

type Key = { id: string; name: string; scopes: string[]; last_used_at: string | null; revoked_at: string | null };

function APIKeys() {
  const list = useAsync<{ items: Key[]; scopes: string[] }>(() => api("/api-keys"), []);
  const [open, setOpen] = useState(false);
  const [issued, setIssued] = useState<string | null>(null);
  const toast = useToast();

  return (
    <Card title="Ключи доступа" eyebrow="для сервиса страницы подписки">
      <Notice kind="info" title="Ключ ограничен правами">
        Сервису страницы выдаётся ровно то, что ему нужно: читать подписчика и управлять его
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
  const [picked, setPicked] = useState<string[]>(scopes.filter((s) => s !== "sub:instructions"));
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
    case "sub:read": return "читать подписчика и его устройства";
    case "sub:devices": return "добавлять и удалять устройства";
    case "sub:instructions": return "читать тексты инструкций";
    default: return "";
  }
}
