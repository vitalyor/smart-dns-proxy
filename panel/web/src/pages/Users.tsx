import { Fragment, useState } from "react";
import { api, ago, download, timeTitle } from "../api";
import {
  Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Spinner,
  errText, useAsync, useToast,
} from "../ui";
import { IconPlus, IconTrash, IconRotate, IconDownload, IconPhone } from "../icons";

type Sub = {
  id: string; name: string; note: string; short_id: string;
  enabled: boolean; expires_at: string | null;
  device_limit: number | null; query_limit: number | null;
  query_period: string; queries_used: number;
  created_at: string;
  url: string; device_count: number; effective_device_limit: number;
};
type ListResp = { items: Sub[]; device_limit_default: number; page_url_configured: boolean };

export const DEVICE_TYPES: { value: string; label: string }[] = [
  { value: "apple_doh", label: "iPhone, iPad или Mac — профиль DoH" },
  { value: "apple_dot", label: "Apple — профиль DoT" },
  { value: "windows_doh", label: "Windows 11 — DoH" },
  { value: "android_dot", label: "Android — Private DNS" },
  { value: "router", label: "Роутер / OpenWrt" },
  { value: "plain", label: "Обычный DNS, без шифрования" },
];
const typeLabel = (t: string) => DEVICE_TYPES.find((x) => x.value === t)?.label ?? t;

const expired = (s: Sub) => s.expires_at != null && new Date(s.expires_at) < new Date();
const overQuota = (s: Sub) => s.query_limit != null && s.queries_used >= s.query_limit;
const active = (s: Sub) => s.enabled && !expired(s) && !overQuota(s);

export default function Users() {
  const list = useAsync<ListResp>(() => api("/subscribers"), []);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Sub | null>(null);
  const [rotating, setRotating] = useState<Sub | null>(null);
  const [removing, setRemoving] = useState<Sub | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const toast = useToast();

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">доступ</div><h1>Пользователи</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Добавить</button>
      </div>

      <Notice kind="info" title="Как это работает">
        Каждому человеку выдаётся личная ссылка. По ней он открывает свою страницу, где видит
        устройства, добавляет новые и скачивает настройки. Регистрации нет — пользователей заводите вы.
        Устройства живут внутри пользователя: разверните строку, чтобы посмотреть или добавить.
      </Notice>

      {list.data && !list.data.page_url_configured && (
        <Notice kind="warn" title="Не задан адрес страницы подписки">
          Ссылки не собираются, пока он пуст. Укажите его на странице «Страница подписки».
        </Notice>
      )}

      {list.loading ? <Spinner />
        : list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.data!.items.length === 0 ? (
          <Card tight>
            <div className="empty">
              <h3>Пользователей пока нет</h3>
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
                    <th>Пользователь</th><th style={{ width: 90 }}>Статус</th>
                    <th style={{ width: 110 }}>Устройства</th><th style={{ width: 150 }}>Запросы</th>
                    <th style={{ width: 130 }}>Срок</th><th />
                  </tr>
                </thead>
                <tbody>
                  {list.data!.items.map((s) => (
                    <Fragment key={s.id}>
                      <tr>
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
                          <button className="btn sm ghost" onClick={() => setOpen(open === s.id ? null : s.id)}>
                            <IconPhone />{s.device_count} / {s.effective_device_limit}
                          </button>
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
                      {open === s.id && (
                        <tr>
                          <td colSpan={6} style={{ background: "var(--surface-2)" }}>
                            <Devices sub={s} onChanged={list.reload} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

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
          body={<>Ссылка перестанет работать, все устройства пользователя и их доступ будут удалены. Отменить нельзя.</>} />
      )}
    </>
  );
}

type Device = {
  id: string; name: string; type: string; created_at: string;
  last_seen_at: string | null; queries_total: number;
  doh_url?: string; dot_host?: string;
};

/**
 * Devices of one user, inline. The same list the person sees on their own page —
 * the operator gets it here so a device can be set up for someone who never
 * opens the link (their own phone, for one).
 */
function Devices({ sub, onChanged }: { sub: Sub; onChanged: () => void }) {
  const list = useAsync<{ items: Device[]; device_limit: number }>(
    () => api(`/subscribers/${sub.id}/devices`), [sub.id]);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Device | null>(null);
  const toast = useToast();

  if (list.loading) return <Spinner />;
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;

  return (
    <div style={{ padding: "4px 2px 10px" }}>
      {list.data!.items.length === 0 ? (
        <p className="muted small">Устройств нет. Человек добавит их сам по своей ссылке — или добавьте здесь.</p>
      ) : (
        <table className="table">
          <thead><tr><th>Устройство</th><th>Платформа</th><th style={{ width: 150 }}>Активность</th><th /></tr></thead>
          <tbody>
            {list.data!.items.map((d) => (
              <tr key={d.id}>
                <td>
                  <div style={{ fontWeight: 550 }}>{d.name}</div>
                  {(d.doh_url || d.dot_host) && (
                    <div className="tiny dim mono" style={{ wordBreak: "break-all" }}>
                      {d.doh_url || d.dot_host}
                    </div>
                  )}
                </td>
                <td className="small">{typeLabel(d.type)}</td>
                <td className="small dim" title={timeTitle(d.last_seen_at)}>
                  {d.last_seen_at ? ago(d.last_seen_at) : "не подключалось"}
                  <div className="tiny dim">{d.queries_total.toLocaleString("ru-RU")} запросов</div>
                </td>
                <td className="actions">
                  <button className="btn sm" onClick={() => download(`/subscribers/${sub.id}/devices/${d.id}/download`)}>
                    <IconDownload />Скачать
                  </button>
                  <button className="btn sm ghost danger" aria-label={`Удалить ${d.name}`}
                    onClick={() => setRemoving(d)}><IconTrash /></button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <button className="btn sm" style={{ marginTop: 10 }} onClick={() => setAdding(true)}>
        <IconPlus />Добавить устройство
      </button>

      {adding && (
        <AddDevice sub={sub} onClose={() => setAdding(false)}
          onDone={() => { setAdding(false); list.reload(); onChanged(); }} />
      )}
      {removing && (
        <Confirm title={`Удалить ${removing.name}?`} danger confirmLabel="Удалить"
          body="Устройство потеряет доступ к резолверу в течение минуты."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/subscribers/${sub.id}/devices/${removing.id}`, { method: "DELETE" });
              setRemoving(null); list.reload(); onChanged();
            } catch (e) { toast({ kind: "bad", title: "Не удалось удалить", body: errText(e) }); }
          }} />
      )}
    </div>
  );
}

function AddDevice({ sub, onClose, onDone }: { sub: Sub; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState(DEVICE_TYPES[0].value);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  return (
    <Modal title={`Новое устройство — ${sub.name}`} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy || !name.trim()} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api(`/subscribers/${sub.id}/devices`, { method: "POST", body: { name, type } });
            onDone();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Добавить</button>
      </>
    }>
      <Field label="Название" hint="Например: «iPhone Виталия».">
        <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Платформа" error={error}
        hint="Адрес получится персональным: у каждого устройства свой токен доступа.">
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          {DEVICE_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
      </Field>
      <Notice kind="info" title="Доступ включится в течение минуты">
        Токен уезжает на ноды отдельным каналом — пересобирать конфигурацию не нужно.
      </Notice>
    </Modal>
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
    <Modal title={sub ? `Изменить ${sub.name}` : "Новый пользователь"} onClose={onClose} footer={
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
        <Field label="Лимит устройств" hint="Пусто — общий лимит со страницы подписки.">
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
          Доступ включён
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
          Отправьте его человеку — старый больше не работает.
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
