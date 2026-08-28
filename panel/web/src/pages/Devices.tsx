import { useState } from "react";
import { api, download, fmtTime } from "../api";
import { Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconDownload, IconPlus, IconTrash } from "../icons";

type Profile = { id: string; name: string; type: string; config: any; created_at: string };
type Defaults = {
  doh_hostname: string; dot_hostname: string; doh_path: string;
  ingress_ipv4: string[] | null; ingress_ipv6: string[] | null; access_mode: string;
};

const TYPES = [
  { value: "android_dot", label: "Android — Private DNS (DoT)",
    note: "Работает и в мобильной сети. Android не передаёт токен, поэтому резолвер защищён лимитами запросов." },
  { value: "apple_doh", label: "Apple — профиль DoH (.mobileconfig)",
    note: "Подписываемый профиль для iOS и macOS. Получает уникальный путь с токеном." },
  { value: "apple_dot", label: "Apple — профиль DoT (.mobileconfig)",
    note: "То же, но по DNS over TLS." },
  { value: "windows_doh", label: "Windows 11 — DoH",
    note: "Windows 10 не даёт задать произвольный шаблон DoH через интерфейс." },
  { value: "router", label: "Роутер / OpenWrt",
    note: "Настраивает весь дом сразу через https-dns-proxy или stubby." },
  { value: "plain", label: "Обычный DNS",
    note: "Без шифрования. Только для доверенной сети или устройств без поддержки DoH/DoT." },
];

export default function Devices() {
  const list = useAsync<{ items: Profile[]; defaults: Defaults }>(() => api("/device-profiles"), []);
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<{ profile: Profile; token: string } | null>(null);
  const [removing, setRemoving] = useState<Profile | null>(null);
  const toast = useToast();

  const d = list.data?.defaults;
  const notConfigured = d && !d.doh_hostname && !d.dot_hostname;

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Устройства</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Новый профиль</button>
      </div>

      {notConfigured && (
        <Notice kind="warn" title="Не заданы имена DoH и DoT">
          Профили будут неполными, пока в настройках не указаны публичные имена резолвера.
          Откройте «Настройки» и заполните поля DoH и DoT.
        </Notice>
      )}

      <Notice kind="info" title="Что нельзя обещать">
        HTTP/3 по умолчанию отключён, и браузер откатывается на TCP. Сервисы с обязательным
        Encrypted ClientHello не поддерживаются. Два DNS-адреса в настройках ОС не гарантируют
        переключение на резервный.
      </Notice>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading ? <Spinner />
        : list.data!.items.length === 0 ? (
          <Card><div className="empty">
            <h3>Профилей пока нет</h3>
            <p className="muted small">Создайте профиль под платформу — панель сформирует инструкцию или .mobileconfig.</p>
            <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating(true)}>
              <IconPlus />Новый профиль
            </button>
          </div></Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Профиль</th><th>Платформа</th><th>Создан</th><th /></tr></thead>
                <tbody>
                  {list.data!.items.map((p) => (
                    <tr key={p.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{p.name}</div>
                        {p.config?.warning && <div className="tiny dim" style={{ marginTop: 4, maxWidth: 520 }}>{p.config.warning}</div>}
                      </td>
                      <td className="small">{TYPES.find((t) => t.value === p.type)?.label ?? p.type}</td>
                      <td className="small dim">{fmtTime(p.created_at)}</td>
                      <td className="actions">
                        <button className="btn sm" onClick={() => download(`/device-profiles/${p.id}/download`)}>
                          <IconDownload />Скачать
                        </button>
                        <button className="btn sm ghost danger" aria-label={`Удалить ${p.name}`}
                          onClick={() => setRemoving(p)}><IconTrash /></button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

      {d && (
        <Card title="Адреса резолвера" eyebrow="что вписывать на устройстве">
          <dl className="kv">
            <dt>DoH</dt><dd>{d.doh_hostname ? `https://${d.doh_hostname}${d.doh_path}` : "не настроен"}</dd>
            <dt>DoT</dt><dd>{d.dot_hostname || "не настроен"}</dd>
            <dt>Обычный DNS</dt><dd>{d.ingress_ipv4?.join(", ") || "нет входных нод с IPv4"}</dd>
            <dt>Режим доступа</dt><dd>{d.access_mode}</dd>
          </dl>
        </Card>
      )}

      {creating && (
        <Modal title="Новый профиль устройства" onClose={() => setCreating(false)} footer={null}>
          <CreateForm onCreated={(v) => { setCreating(false); setIssued(v); list.reload(); }}
            onCancel={() => setCreating(false)} />
        </Modal>
      )}

      {issued && (
        <Modal title="Профиль создан" onClose={() => setIssued(null)}
          footer={<>
            <button className="btn" onClick={() => setIssued(null)}>Закрыть</button>
            <button className="btn primary" onClick={() => download(`/device-profiles/${issued.profile.id}/download`)}>
              <IconDownload />Скачать
            </button>
          </>}>
          {issued.token ? (
            <>
              <Notice kind="warn" title="Токен пути показывается один раз">
                Он превращает общий резолвер в персональный адрес. В базе хранится только хеш.
              </Notice>
              <div className="codeblock">{issued.token}</div>
              <Copyable value={issued.token} />
            </>
          ) : (
            <Notice kind="info" title="Токен для этой платформы недоступен">
              {issued.profile.config?.warning ?? "Профиль использует общий адрес резолвера."}
            </Notice>
          )}
        </Modal>
      )}

      {removing && (
        <Confirm title={`Удалить профиль ${removing.name}?`} danger confirmLabel="Удалить"
          body="Токен пути перестанет приниматься после следующей сборки и применения конфигурации. Устройства с этим профилем потеряют доступ к резолверу."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/device-profiles/${removing.id}`, { method: "DELETE" });
              toast({ kind: "ok", title: "Профиль удалён", body: "Соберите конфигурацию, чтобы отозвать токен на нодах." });
              setRemoving(null); list.reload();
            } catch (e) { toast({ kind: "bad", title: "Удаление не удалось", body: errText(e) }); }
          }} />
      )}
    </>
  );
}

function CreateForm({ onCreated, onCancel }: {
  onCreated: (v: { profile: Profile; token: string }) => void; onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState(TYPES[0].value);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const note = TYPES.find((t) => t.value === type)?.note;

  return (
    <>
      <Field label="Название" hint="Например: «iPhone Виталия» или «Домашний роутер».">
        <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Платформа" hint={note}>
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          {TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
      </Field>
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={onCancel}>Отмена</button>
        <button className="btn primary" disabled={busy || !name.trim()} onClick={async () => {
          setBusy(true); setError("");
          try {
            onCreated(await api("/device-profiles", { method: "POST", body: { name, type } }));
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать</button>
      </div>
    </>
  );
}
