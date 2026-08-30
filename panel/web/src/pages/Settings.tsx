import { useEffect, useState } from "react";
import { api, apiDownload, apiUpload } from "../api";
import { Card, Copyable, ErrorState, Field, Modal, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconKey, IconShield } from "../icons";
import type { Me } from "../App";

type SettingsResp = { settings: Record<string, any>; lab_mode: boolean; version: string };

export default function Settings({ me, onChanged }: { me: Me; onChanged: () => void }) {
  const s = useAsync<SettingsResp>(() => api("/settings"), []);
  const [form, setForm] = useState<Record<string, any>>({});
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => { if (s.data) setForm(s.data.settings); }, [s.data]);

  if (s.error) return <ErrorState message={s.error} onRetry={s.reload} />;
  if (s.loading || !s.data) return <Spinner />;

  const set = (k: string, v: any) => setForm((f) => ({ ...f, [k]: v }));
  const str = (k: string, d = "") => (form[k] ?? d) as string;
  const num = (k: string, d = 0) => Number(form[k] ?? d);

  const save = async () => {
    setBusy(true);
    try {
      await api("/settings", {
        method: "PUT",
        body: {
          dns_upstream: str("dns_upstream"),
          dns_access_mode: str("dns_access_mode"),
          dns_allowed_cidrs: form.dns_allowed_cidrs ?? [],
          dns_rate_limit_qps: num("dns_rate_limit_qps", 50),
          dns_rate_limit_burst: num("dns_rate_limit_burst", 250),
          doh_hostname: str("doh_hostname"),
          dot_hostname: str("dot_hostname"),
          doh_path: str("doh_path", "/dns-query"),
          publish_aaaa: str("publish_aaaa", "false"),
          egress_resolver: str("egress_resolver"),
          timezone: str("timezone"),
          quic_policy: str("quic_policy"),
          log_level: str("log_level", "info"),
          node_log_level: str("node_log_level", "info"),
        },
      });
      toast({ kind: "ok", title: "Настройки сохранены", body: "Соберите конфигурацию, чтобы применить их на нодах." });
      s.reload();
    } catch (e) { toast({ kind: "bad", title: "Не удалось сохранить", body: errText(e) }); }
    finally { setBusy(false); }
  };

  const cidrs: string[] = form.dns_allowed_cidrs ?? [];

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Настройки</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={save} disabled={busy}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </div>

      <Notice kind="info" title="Настройки попадают на ноды только через конфигурацию">
        Изменения здесь меняют желаемое состояние. Ноды получат его после сборки и применения конфигурации.
      </Notice>

      <div className="grid g2">
        <Card title="Доступ к резолверу" eyebrow="кто может задавать вопросы">
          <div className="col" style={{ gap: 14 }}>
            <Field label="Режим доступа" hint={accessHint(str("dns_access_mode", "allowlist"))}>
              <select className="select" value={str("dns_access_mode", "allowlist")}
                onChange={(e) => set("dns_access_mode", e.target.value)}>
                <option value="allowlist">По списку адресов</option>
                <option value="doh-token">Токен в пути DoH</option>
                <option value="mtls">Взаимный TLS</option>
                <option value="restricted-public-dot">Публичный DoT с жёсткими лимитами</option>
              </select>
            </Field>
            <Field label="Разрешённые сети" hint="По одной подсети в строке, например 203.0.113.0/24.">
              <textarea className="textarea mono" value={cidrs.join("\n")}
                onChange={(e) => set("dns_allowed_cidrs", e.target.value.split("\n").map((x) => x.trim()).filter(Boolean))} />
            </Field>
            <div className="grid g2">
              <Field label="Лимит запросов, /с" hint="На подсеть /24 (IPv4) или /64 (IPv6).">
                <input className="input num" type="number" min={1} value={num("dns_rate_limit_qps", 50)}
                  onChange={(e) => set("dns_rate_limit_qps", Number(e.target.value))} />
              </Field>
              <Field label="Всплеск">
                <input className="input num" type="number" min={1} value={num("dns_rate_limit_burst", 250)}
                  onChange={(e) => set("dns_rate_limit_burst", Number(e.target.value))} />
              </Field>
            </div>
          </div>
        </Card>

        <Card title="Имена и адреса" eyebrow="что видят устройства">
          <div className="col" style={{ gap: 14 }}>
            <Field label="Имя для DoH" hint="Домен, на который выписан TLS-сертификат входной ноды.">
              <input className="input mono" value={str("doh_hostname")}
                onChange={(e) => set("doh_hostname", e.target.value)} placeholder="dns.example.net" />
            </Field>
            <Field label="Путь DoH">
              <input className="input mono" value={str("doh_path", "/dns-query")}
                onChange={(e) => set("doh_path", e.target.value)} />
            </Field>
            <Field label="Имя для DoT" hint="Его вписывают в Android Private DNS.">
              <input className="input mono" value={str("dot_hostname")}
                onChange={(e) => set("dot_hostname", e.target.value)} placeholder="dns.example.net" />
            </Field>
            <Field label="Резолвер на egress" hint="Через него выходная нода определяет настоящий адрес сайта.">
              <input className="input mono" value={str("egress_resolver")}
                onChange={(e) => set("egress_resolver", e.target.value)} placeholder="1.1.1.1:53" />
            </Field>
          </div>
        </Card>
      </div>

      <div className="grid g2">
        <Card title="Протоколы" eyebrow="границы поддержки">
          <div className="col" style={{ gap: 14 }}>
            <Field label="Публиковать AAAA"
              hint="Happy Eyeballs предпочитает IPv6. Включайте только после сквозной проверки IPv6, иначе часть запросов начнёт молча падать.">
              <select className="select" value={str("publish_aaaa", "false")}
                onChange={(e) => set("publish_aaaa", e.target.value)}>
                <option value="false">Нет (рекомендуется)</option>
                <option value="true">Да, IPv6 проверен</option>
              </select>
            </Field>
            <Field label="Политика QUIC / HTTP-3"
              hint="Поддержка HTTP/3 не гарантируется. По умолчанию UDP/443 отклоняется, и браузер откатывается на TCP.">
              <select className="select" value={str("quic_policy", "disabled_fallback")}
                onChange={(e) => set("quic_policy", e.target.value)}>
                <option value="disabled_fallback">Отключено, откат на TCP</option>
                <option value="proxy">Проксировать (после сквозной проверки)</option>
                <option value="separate_ip">Отдельный IP для QUIC</option>
              </select>
            </Field>
            <Field label="Апстрим DNS для ingress" hint="Адрес Unbound во внутренней сети ноды.">
              <input className="input mono" value={str("dns_upstream")}
                onChange={(e) => set("dns_upstream", e.target.value)} placeholder="unbound:53" />
            </Field>
          </div>
        </Card>

        <Security me={me} onChanged={onChanged} />
      </div>

      <Card title="Логирование" eyebrow="сколько писать и куда">
        <div className="grid g2">
          <Field label="Уровень панели"
            hint="Применяется сразу, перезапуск не нужен. Успешные запросы к API пишутся только на debug — их и так считают метрики.">
            <select className="select" value={str("log_level", "info")}
              onChange={(e) => set("log_level", e.target.value)}>
              {LOG_LEVELS}
            </select>
          </Field>
          <Field label="Уровень нод"
            hint="Уезжает на ноды со следующей конфигурацией. Нода, где LOG_LEVEL задан локально, остаётся на своём — так отлаживают один хост.">
            <select className="select" value={str("node_log_level", "info")}
              onChange={(e) => set("node_log_level", e.target.value)}>
              {LOG_LEVELS}
            </select>
          </Field>
        </div>
        {(str("log_level", "info") === "debug" || str("node_log_level", "info") === "debug") ? (
          <Notice kind="warn" title="Debug пишет имена доменов и отключает подавление повторов">
            На debug ingress записывает SNI каждого проксируемого соединения, а одинаковые строки
            перестают сворачиваться. Это режим для короткого разбора проблемы, а не для постоянной работы:
            включили, воспроизвели, вернули info.
          </Notice>
        ) : (
          <p className="muted" style={{ marginTop: 12 }}>
            На info и выше повторяющаяся строка пишется раз в минуту с полем <code className="mono">suppressed</code> —
            счётчиком проглоченных повторов. Файлы логов Docker ограничены: 3 × 10 МиБ на контейнер.
          </p>
        )}
      </Card>

      {me.user.role === "owner" && <BackupRestore />}

      <Card title="О системе" eyebrow="версии и режим">
        <dl className="kv">
          <dt>Версия панели</dt><dd>{s.data.version}</dd>
          <dt>Лабораторный режим</dt>
          <dd>{s.data.lab_mode ? "включён — egress достигает приватных адресов" : "выключен"}</dd>
          <dt>Часовой пояс</dt><dd>{str("timezone", "UTC")}</dd>
          <dt>Уровень логов панели</dt><dd className="mono">{str("log_level", "info")}</dd>
        </dl>
      </Card>
    </>
  );
}

const LOG_LEVELS = (
  <>
    <option value="error">error — только сбои</option>
    <option value="warn">warn — сбои и подозрительное</option>
    <option value="info">info — обычный режим</option>
    <option value="debug">debug — разбор проблемы</option>
  </>
);

function accessHint(mode: string): string {
  switch (mode) {
    case "doh-token": return "Каждому устройству выдаётся уникальный путь. Работает там, где клиент умеет произвольный DoH-адрес.";
    case "mtls": return "Для управляемых клиентов с клиентским сертификатом.";
    case "restricted-public-dot":
      return "Для Android Private DNS: резолвер контролируемо публичный, защита — строгие лимиты запросов, а не персональная аутентификация.";
    default: return "Лучший вариант при фиксированных адресах или доступе через VPN.";
  }
}

function BackupRestore() {
  const toast = useToast();
  const [pass, setPass] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [rpass, setRpass] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [restarting, setRestarting] = useState(false);

  const doBackup = async () => {
    setBusy(true);
    try {
      await apiDownload("/backup", { passphrase: pass });
      toast({ kind: "ok", title: "Копия скачана", body: pass ? "Файл зашифрован — храните пароль отдельно." : "Файл не зашифрован." });
    } catch (e) { toast({ kind: "bad", title: "Не удалось создать копию", body: errText(e) }); }
    finally { setBusy(false); }
  };

  const doRestore = async () => {
    if (!file) return;
    setConfirm(false); setBusy(true);
    try {
      const fd = new FormData();
      fd.append("file", file);
      fd.append("passphrase", rpass);
      await apiUpload("/restore", fd);
      setRestarting(true);
      const t0 = Date.now();
      const poll = async () => {
        try { const r = await fetch("/healthz"); if (r.ok) { location.href = "/"; return; } } catch { /* down mid-restart */ }
        if (Date.now() - t0 < 120000) setTimeout(poll, 2000);
      };
      setTimeout(poll, 3000);
    } catch (e) { toast({ kind: "bad", title: "Не удалось восстановить", body: errText(e) }); setBusy(false); }
  };

  if (restarting) {
    return (
      <Card title="Перенос панели" eyebrow="восстановление из копии">
        <Notice kind="info" title="Панель перезапускается с восстановленными данными">
          Когда она поднимется, страница откроется заново. Войдите учётными данными <b>старой</b> панели —
          база и ключи заменены копией. Если сервер сменился, впустите новый IP на нодах: <code className="mono">ufw allow … 3333</code>.
        </Notice>
        <div className="row" style={{ marginTop: 12, gap: 10 }}><span className="spin" /> Ждём готовности…</div>
      </Card>
    );
  }

  return (
    <Card title="Резервная копия и перенос" eyebrow="вся панель одним файлом">
      <Notice kind="info" title="Копия содержит базу и приватные ключи — CA и сертификат панели">
        Этого достаточно, чтобы поднять панель на новом сервере: ноды продолжат доверять ей по отпечатку
        сертификата, переустанавливать ничего не нужно. Пароль шифрует файл — без него ключи в открытом виде.
      </Notice>
      <div className="grid g2" style={{ marginTop: 14 }}>
        <div className="col" style={{ gap: 12 }}>
          <div className="eyebrow">скачать с этой панели</div>
          <Field label="Пароль шифрования" hint="Необязательно, но копия содержит ключи — задайте и сохраните.">
            <input className="input" type="password" value={pass} autoComplete="off"
              onChange={(e) => setPass(e.target.value)} placeholder="пусто — без шифрования" />
          </Field>
          <button className="btn primary" onClick={doBackup} disabled={busy}>
            {busy ? <span className="spin" /> : null}Скачать копию
          </button>
        </div>
        <div className="col" style={{ gap: 12 }}>
          <div className="eyebrow">восстановить на этой панели</div>
          <Field label="Файл копии" hint="smartdns-*.tar или .tar.enc">
            <input className="input" type="file" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
          </Field>
          <Field label="Пароль копии" hint="Если файл зашифрован.">
            <input className="input" type="password" value={rpass} autoComplete="off"
              onChange={(e) => setRpass(e.target.value)} />
          </Field>
          <button className="btn danger" onClick={() => setConfirm(true)} disabled={busy || !file}>
            Восстановить и перезапустить
          </button>
        </div>
      </div>

      {confirm && (
        <Modal title="Восстановить из копии?" onClose={() => setConfirm(false)} footer={
          <>
            <button className="btn" onClick={() => setConfirm(false)}>Отмена</button>
            <button className="btn danger" onClick={doRestore}>Восстановить</button>
          </>
        }>
          <Notice kind="warn" title="Текущие база и ключи будут заменены, панель перезапустится">
            Делайте это на свежей установке при переносе: все нынешние данные этой панели пропадут,
            и вы войдёте заново учётными данными из копии.
          </Notice>
        </Modal>
      )}
    </Card>
  );
}

function Security({ me, onChanged }: { me: Me; onChanged: () => void }) {
  const [pwOpen, setPwOpen] = useState(false);
  const [totpOpen, setTotpOpen] = useState(false);
  const sessions = useAsync<{ items: any[]; current: string }>(() => api("/auth/sessions"), []);
  const toast = useToast();

  return (
    <Card title="Безопасность" eyebrow="учётная запись владельца">
      <div className="col" style={{ gap: 14 }}>
        <div className="row">
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 550 }}>Пароль</div>
            <div className="small muted">Смена пароля завершает все остальные сессии.</div>
          </div>
          <button className="btn sm" onClick={() => setPwOpen(true)}><IconKey />Сменить</button>
        </div>
        <div className="row">
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 550 }}>Двухфакторная аутентификация</div>
            <div className="small muted">
              {me.user.totp_enabled ? "Включена. При входе запрашивается код." : "Выключена — рекомендуем включить."}
            </div>
          </div>
          <button className="btn sm" onClick={() => setTotpOpen(true)}>
            <IconShield />{me.user.totp_enabled ? "Отключить" : "Включить"}
          </button>
        </div>

        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>активные сессии</div>
          {sessions.loading ? <Spinner /> : (
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Адрес</th><th>Клиент</th><th /></tr></thead>
                <tbody>
                  {sessions.data?.items.map((x) => (
                    <tr key={x.id}>
                      <td className="mono tiny">{x.ip ?? "—"}</td>
                      <td className="tiny dim" style={{ maxWidth: 260, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                        title={x.user_agent}>{x.user_agent || "—"}</td>
                      <td className="actions">
                        {x.id === sessions.data?.current ? <span className="badge ok">текущая</span> : (
                          <button className="btn sm ghost danger" onClick={async () => {
                            await api(`/auth/sessions/${x.id}`, { method: "DELETE" });
                            sessions.reload();
                          }}>Завершить</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {pwOpen && <ChangePassword onClose={() => setPwOpen(false)} onDone={() => { setPwOpen(false); onChanged(); }} />}
      {totpOpen && (
        me.user.totp_enabled
          ? <DisableTotp onClose={() => setTotpOpen(false)} onDone={() => { setTotpOpen(false); onChanged(); }} />
          : <EnableTotp onClose={() => setTotpOpen(false)} onDone={() => { setTotpOpen(false); onChanged();
              toast({ kind: "ok", title: "Двухфакторная аутентификация включена" }); }} />
      )}
    </Card>
  );
}

function ChangePassword({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [cur, setCur] = useState(""); const [next, setNext] = useState("");
  const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  return (
    <Modal title="Смена пароля" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api("/auth/password", { method: "POST", body: { current_password: cur, new_password: next } });
            onDone();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Сменить</button>
      </>
    }>
      <Field label="Текущий пароль"><input className="input" type="password" autoComplete="current-password"
        value={cur} onChange={(e) => setCur(e.target.value)} /></Field>
      <Field label="Новый пароль" hint="Минимум 8 символов." error={error}>
        <input className="input" type="password" autoComplete="new-password"
          value={next} onChange={(e) => setNext(e.target.value)} />
      </Field>
    </Modal>
  );
}

function EnableTotp({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const setup = useAsync<{ secret: string; uri: string }>(() => api("/auth/totp/setup", { method: "POST" }), []);
  const [code, setCode] = useState(""); const [error, setError] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);

  if (codes) {
    return (
      <Modal title="Резервные коды" onClose={onDone}
        footer={<button className="btn primary" onClick={onDone}>Я сохранил коды</button>}>
        <Notice kind="warn" title="Показываются один раз">
          Каждый код срабатывает однократно и заменяет код из приложения. Храните их отдельно от пароля.
        </Notice>
        <div className="codeblock">{codes.join("\n")}</div>
        <Copyable value={codes.join("\n")} />
      </Modal>
    );
  }

  return (
    <Modal title="Включить двухфакторную аутентификацию" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={code.length !== 6} onClick={async () => {
          setError("");
          try {
            const r = await api<{ recovery_codes: string[] }>("/auth/totp/enable", { method: "POST", body: { code } });
            setCodes(r.recovery_codes);
          } catch (e) { setError(errText(e)); }
        }}>Подтвердить</button>
      </>
    }>
      {setup.loading ? <Spinner /> : setup.error ? <ErrorState message={setup.error} /> : (
        <>
          <Field label="Секрет для приложения" hint="Добавьте вручную, если сканер QR недоступен.">
            <input className="input mono" readOnly value={setup.data!.secret} />
          </Field>
          <Copyable value={setup.data!.uri} label="Копировать otpauth-ссылку" />
          <Field label="Код из приложения" hint="Шесть цифр." error={error}>
            <input className="input mono" inputMode="numeric" maxLength={6} value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
          </Field>
        </>
      )}
    </Modal>
  );
}

function DisableTotp({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [pw, setPw] = useState(""); const [error, setError] = useState("");
  return (
    <Modal title="Отключить двухфакторную аутентификацию" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn danger" onClick={async () => {
          setError("");
          try { await api("/auth/totp/disable", { method: "POST", body: { password: pw } }); onDone(); }
          catch (e) { setError(errText(e)); }
        }}>Отключить</button>
      </>
    }>
      <Notice kind="warn" title="Вход останется защищён только паролем">
        Резервные коды будут удалены.
      </Notice>
      <Field label="Пароль" error={error}>
        <input className="input" type="password" value={pw} onChange={(e) => setPw(e.target.value)} />
      </Field>
    </Modal>
  );
}
