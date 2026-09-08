import { useEffect, useState } from "react";
import { api, ago, idemKey, plural, timeTitle } from "../api";
import {
  Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Segmented, Spinner,
  StatusBadge, errText, useAsync, usePoll, useToast,
} from "../ui";

type Role = "ingress" | "egress";
import { IconLayers, IconPlus, IconRefresh, IconShield, IconSliders, IconTrash } from "../icons";
import { COUNTRIES, countryName, flagOf } from "../countries";

type Node = {
  id: string; name: string; role: string; status: string;
  public_ipv4: string | null; public_ipv6: string | null;
  relay_endpoint: string | null; mgmt_address: string; agent_version: string;
  last_seen_at: string | null; last_error: string; country: string;
  observed_ipv4: string | null;
  desired_sequence: number | null; applied_sequence: number | null;
  services: string[]; cert_days_left: number | null; version: number;
};

export default function Nodes() {
  // Live status: re-poll every 5s. Data is swapped in place, so the table never
  // flickers to a spinner after the first load (see the guard below).
  const nodes = usePoll<{ items: Node[] }>(() => api("/nodes"), 5000, []);
  // Holds the role to pre-select, so the egress section's button opens the modal
  // on "egress" instead of always defaulting to "ingress".
  const [creating, setCreating] = useState<Role | null>(null);
  const [issued, setIssued] = useState<{ name: string; role: string; install_command: string; bundle: string } | null>(null);
  const [removing, setRemoving] = useState<Node | null>(null);
  const [editing, setEditing] = useState<Node | null>(null);
  const [certNode, setCertNode] = useState<Node | null>(null);
  const [svcNode, setSvcNode] = useState<Node | null>(null);
  const toast = useToast();


  return (
    <>
      <div className="row">
        <div>
          <div className="eyebrow">инфраструктура</div>
          <h1>Ноды</h1>
        </div>
        <div className="spacer" />
        <button className="btn" onClick={() => nodes.reload()}><IconRefresh />Обновить</button>
        <button className="btn primary" onClick={() => setCreating("ingress")}><IconPlus />Добавить ноду</button>
      </div>

      <Notice kind="info" title="Ноды работают автономно">
        Если панель недоступна, нода продолжает обслуживать трафик на последней проверенной конфигурации.
        Расхождение между назначенной и применённой конфигурацией видно в колонке «Конфигурация».
      </Notice>

      {nodes.error && !nodes.data ? <ErrorState message={nodes.error} onRetry={nodes.reload} />
        : nodes.loading && !nodes.data ? <Spinner />
        : nodes.data!.items.length === 0 ? (
          <Card tight>
            <div className="empty">
              <h3>Нод пока нет</h3>
              <p className="muted small">
                Добавьте ноду: панель выдаст команду и ключ подключения. Выполните её на сервере —
                панель сама подключится к ноде на её порт 3333.
              </p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating("ingress")}>
                <IconPlus />Добавить ноду
              </button>
            </div>
          </Card>
        ) : (["ingress", "egress"] as const).map((role) => {
          const rows = nodes.data!.items.filter((n) => n.role === role);
          const meta = role === "ingress"
            ? { title: "Точки входа", eyebrow: "принимают DNS и HTTPS от устройств", cls: "direct" }
            : { title: "Точки выхода", eyebrow: "выходят к сайтам за рубежом", cls: "managed" };
          return (
            <Card key={role} title={meta.title} eyebrow={meta.eyebrow} tight>
              {rows.length === 0 ? (
                <div className="empty" style={{ padding: 28 }}>
                  <h3>Нет {role === "ingress" ? "входных" : "выходных"} нод</h3>
                  <p className="muted small">
                    {role === "ingress"
                      ? "Сервер в России, куда устройства отправляют запросы."
                      : "Зарубежный сервер, чей IP видит конечный сервис."}
                  </p>
                  <button className="btn sm primary" style={{ marginTop: 12 }} onClick={() => setCreating(role)}>
                    <IconPlus />Добавить
                  </button>
                </div>
              ) : (
                <div className="table-wrap">
                  <table className="table fixed">
                    <colgroup>
                      <col style={{ width: "17%" }} /><col style={{ width: "14%" }} />
                      <col style={{ width: "19%" }} /><col style={{ width: "9%" }} />
                      <col style={{ width: "10%" }} /><col style={{ width: "8%" }} />
                      <col style={{ width: "23%" }} />
                    </colgroup>
                    <thead>
                      <tr>
                        <th>Нода</th><th>Статус</th><th>Адреса</th>
                        <th>Конфигурация</th><th>Heartbeat</th><th>Сервисы</th><th />
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((n) => (
                        <tr key={n.id}>
                          <td>
                            <div className="row" style={{ gap: 8 }}>
                              <span className={`node-dot ${meta.cls}`} aria-hidden="true" />
                              {n.country && (
                                <span className="flag" title={countryName(n.country)}
                                  aria-label={countryName(n.country)}>{flagOf(n.country)}</span>
                              )}
                              <div style={{ fontWeight: 550 }}>{n.name}</div>
                            </div>
                            <div className="tiny dim" style={{ marginLeft: 16 }}>
                              {n.country ? `${countryName(n.country)} · ` : ""}
                              <span className="mono">агент {n.agent_version || "—"}</span>
                            </div>
                          </td>
                          <td>
                            <StatusBadge status={n.status} />
                            {n.cert_days_left != null && n.cert_days_left > 0 && (
                              <div className={`tiny${n.cert_days_left < 14 ? "" : " dim"}`}
                                style={{ marginTop: 4, color: n.cert_days_left < 14 ? "var(--warn)" : undefined }}
                                title="Срок сертификата идентичности ноды">
                                cert: {n.cert_days_left} дн
                              </div>
                            )}
                            {n.last_error && <div className="tiny" style={{ color: "var(--danger)", marginTop: 4 }}>{n.last_error}</div>}
                          </td>
                          <td className="mono tiny">
                            <div>{n.public_ipv4 ?? "— IPv4"}</div>
                            {n.observed_ipv4 && n.public_ipv4 && n.observed_ipv4 !== n.public_ipv4 && (
                              <div style={{ color: "var(--warn)" }}
                                title="Нода сообщает о себе другой адрес. В DNS пока уходит записанный: молча подменить боевую A-запись панель не станет. Проверьте, не переехал ли сервер.">
                                нода видит {n.observed_ipv4}
                              </div>
                            )}
                            <div className="dim">{n.public_ipv6 ?? "— IPv6"}</div>
                            {n.relay_endpoint && <div className="dim">relay {n.relay_endpoint}</div>}
                            <div className="dim">mgmt {n.mgmt_address || "—"}</div>
                          </td>
                          <td className="num small">
                            {n.applied_sequence ?? "—"}
                            {n.desired_sequence !== null && n.desired_sequence !== n.applied_sequence && (
                              <span className="badge warn" style={{ marginLeft: 6 }}>ждёт #{n.desired_sequence}</span>
                            )}
                          </td>
                          <td className="small dim" title={timeTitle(n.last_seen_at)}>{ago(n.last_seen_at)}</td>
                          {/* Число, а не перечисление: нода обслуживает десятки сервисов,
                              и список имён растягивал строку на пол-экрана. Имена — в подсказке. */}
                          <td className="small dim" title={n.services?.join(", ")}>
                            {n.role === "ingress"
                              ? "все"
                              : n.services?.length
                                ? plural(n.services.length, "сервис", "сервиса", "сервисов")
                                : "—"}
                          </td>
                          <td className="actions">
                            <button className="btn sm ghost" onClick={() => setEditing(n)}>Изменить</button>
                            <button className="btn sm ghost icon"
                              title={n.role === "ingress" ? "Сервисы, выходящие прямо с этой ноды" : "Сервисы через эту ноду"}
                              aria-label={`Сервисы ноды ${n.name}`}
                              onClick={() => setSvcNode(n)}><IconLayers /></button>
                            {n.role === "ingress" && (
                              <button className="btn sm ghost icon" title="Сертификат резолвера"
                                aria-label={`Сертификат ноды ${n.name}`}
                                onClick={() => setCertNode(n)}><IconShield /></button>
                            )}
                            <button className="btn sm ghost icon"
                              title={n.status === "maintenance" ? "Вернуть в работу" : "Перевести в обслуживание"}
                              aria-label={n.status === "maintenance"
                                ? `Вернуть в работу ноду ${n.name}`
                                : `Перевести в обслуживание ноду ${n.name}`}
                              onClick={async () => {
                              try {
                                await api(`/nodes/${n.id}/maintenance`, {
                                  method: "POST", body: { enabled: n.status !== "maintenance" },
                                });
                                toast({ kind: "ok", title: n.status === "maintenance" ? "Обслуживание снято" : "Нода в обслуживании" });
                                nodes.reload();
                              } catch (e) { toast({ kind: "bad", title: "Не удалось изменить режим", body: errText(e) }); }
                            }}><IconSliders /></button>
                            <button className="btn sm ghost icon danger" title="Удалить ноду"
                              aria-label={`Удалить ${n.name}`}
                              onClick={() => setRemoving(n)}><IconTrash /></button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          );
        })}

      {creating && (
        <CreateNode
          initialRole={creating}
          onClose={() => setCreating(null)}
          onCreated={(v) => { setCreating(null); setIssued(v); nodes.reload(); }}
        />
      )}

      {issued && (
        <Modal title="Команда установки ноды" onClose={() => setIssued(null)} wide
          footer={<button className="btn primary" onClick={() => setIssued(null)}>Готово</button>}>
          <Notice kind="warn" title="Ключ подключения показывается один раз">
            Ключ подключения несёт идентичность сервера и привязку к панели; в базе он не хранится.
            Потеряли — удалите ноду и создайте заново.
          </Notice>

          <div className="eyebrow" style={{ marginTop: 4 }}>Шаг 1 — запустите на сервере</div>
          <div className="codeblock">{issued.install_command}</div>
          <Copyable value={issued.install_command} label="Копировать команду" />

          <div className="eyebrow" style={{ marginTop: 18 }}>Шаг 2 — вставьте ключ подключения, когда установщик попросит</div>
          <div className="codeblock" style={{ maxHeight: 160, overflow: "auto", wordBreak: "break-all" }}>{issued.bundle}</div>
          <Copyable value={issued.bundle} label="Копировать ключ" />

          <p className="small muted" style={{ margin: "14px 0 0" }}>
            Секрет вводится по запросу — он не попадёт в историю команд.
            Нода поднимется сервером на порту 3333 и будет ждать панель (наружу не звонит) —
            откройте 3333 фаерволом только для IP панели.
          </p>
        </Modal>
      )}
      {removing && (
        <Confirm title={`Удалить ноду ${removing.name}?`} danger confirmLabel="Удалить"
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/nodes/${removing.id}`, { method: "DELETE" });
              toast({ kind: "ok", title: "Нода удалена" });
              setRemoving(null); nodes.reload();
            } catch (e) { toast({ kind: "bad", title: "Удаление отклонено", body: errText(e) }); }
          }}
          body={<>
            Панель перестанет управлять этой нодой, а её сертификат будет отозван.
            Контейнеры на сервере продолжат работать на текущей конфигурации, пока вы не остановите их вручную.
          </>} />
      )}

      {editing && (
        <EditNode node={editing} onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); nodes.reload(); }} />
      )}

      {certNode && (
        <CertModal node={certNode} onClose={() => setCertNode(null)}
          onIssued={() => nodes.reload()} />
      )}

      {svcNode && (
        <NodeServicesModal node={svcNode} onClose={() => setSvcNode(null)}
          onSaved={() => { setSvcNode(null); nodes.reload(); }} />
      )}
    </>
  );
}

// CertModal issues a DoT/DoH certificate for an ingress node via the panel →
// node ACME HTTP-01 flow. The node opens :80 only for the challenge.
// isHostname catches the fat-finger cases before an ACME order is spent: a bare
// IP, a pasted URL with scheme/port/path, spaces, or a single label with no dot.
function isHostname(s: string): boolean {
  if (!s || s.length > 253) return false;
  if (/[/:\s]/.test(s)) return false; // scheme, port, path, whitespace
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) return false; // IP literal, not a name
  return /^([a-z0-9](-*[a-z0-9])*)(\.[a-z0-9](-*[a-z0-9])*)+$/i.test(s);
}

// certReason turns a raw ACME/transport failure into a plain-language cause and
// a fix, so a failed issuance is actionable from the panel without SSHing in.
// The raw text is still shown verbatim under a details toggle.
function certReason(raw: string): { title: string; fix?: string } {
  const e = (raw || "").toLowerCase();
  if (/(rate ?limit|too many|ratelimited)/.test(e))
    return {
      title: "Let’s Encrypt временно ограничил выпуск для этого домена",
      fix: "Слишком много сертификатов на этот домен за неделю. Подождите или включите «Тестовый режим (staging)», чтобы проверить настройку без лимитов.",
    };
  if (/(no such host|lookup|could not resolve|no address|name resolution)/.test(e))
    return {
      title: "Домен вообще не резолвится",
      fix: "Опечатка в имени или ещё нет A-записи. Проверьте домен и что A-запись создана в DNS.",
    };
  if (/(a-record|validation failed|:80|unreachable|connection refused|unauthorized|403|timeout|timed out)/.test(e))
    return {
      title: "Домен не подтвердил, что указывает на эту ноду",
      fix: "A-запись домена должна вести на IP этой ноды, а порт 80 — быть доступен из интернета на время выпуска (ноду откроет его на пару секунд сама). Проверьте оба условия.",
    };
  if (/(no tls directory|only issued on ingress)/.test(e))
    return { title: "Нода не может выпускать сертификат", fix: "Сертификаты выпускаются только на ingress-нодах с настроенным TLS_DIR." };
  if (/(certificate not found|finalize|order is not ready|too many pending)/.test(e))
    return {
      title: "Let’s Encrypt не завершил выпуск (временный сбой)",
      fix: "Обычно лечится повторным нажатием «Выпустить» — особенно если A-запись появилась только что. Если повторяется, подождите минуту и попробуйте снова.",
    };
  return { title: "Не удалось выпустить сертификат" };
}

function CertFailure({ raw }: { raw: string }) {
  const r = certReason(raw);
  return (
    <Notice kind="bad" title={r.title}>
      {r.fix && <p style={{ margin: "0 0 8px" }}>{r.fix}</p>}
      <details>
        <summary className="small muted" style={{ cursor: "pointer" }}>Что вернула нода</summary>
        <pre className="mono tiny" style={{ whiteSpace: "pre-wrap", margin: "6px 0 0" }}>{raw}</pre>
      </details>
    </Notice>
  );
}

function CertModal({ node, onClose, onIssued }: {
  node: Node; onClose: () => void; onIssued: () => void;
}) {
  const cfg = useAsync<{ settings: Record<string, any> }>(() => api("/settings"), []);
  const [domain, setDomain] = useState("");
  const [touched, setTouched] = useState(false);
  const [email, setEmail] = useState("");
  const [force, setForce] = useState(false);
  const [staging, setStaging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ ok: boolean; not_after?: string; domain?: string; error?: string } | null>(null);
  const toast = useToast();

  // Names the resolver actually answers on. The domain field is prefilled from
  // them so a typo can't quietly burn an ACME order on the wrong hostname.
  const names = [cfg.data?.settings.doh_hostname, cfg.data?.settings.dot_hostname]
    .map((x) => (x ?? "").trim()).filter(Boolean);
  const configured = names[0] ?? "";
  useEffect(() => {
    if (!touched && configured) setDomain(configured);
  }, [configured, touched]);

  const d = domain.trim();
  const valid = isHostname(d);
  const mismatch = d !== "" && names.length > 0 && !names.includes(d);

  const issue = async () => {
    setBusy(true); setError(""); setResult(null);
    try {
      const r = await api<{ ok: boolean; not_after?: string; domain?: string; error?: string }>(
        `/nodes/${node.id}/certificate`,
        { method: "POST", body: { domain: d, email: email.trim(), force, staging } },
      );
      setResult(r);
      if (r.ok) { toast({ kind: "ok", title: "Сертификат выпущен", body: `Действует до ${r.not_after}` }); onIssued(); }
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Сертификат для ${node.name}`} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Закрыть</button>
        <button className="btn primary" disabled={busy || !valid} onClick={issue}>
          {busy ? <span className="spin" /> : null}Выпустить
        </button>
      </>
    }>
      <Notice kind="info" title="Как это работает">
        Панель попросит ноду выпустить сертификат Let’s Encrypt по проверке HTTP-01. Нода на
        несколько секунд откроет порт 80 для проверки и сразу закроет его. Нужно, чтобы домен
        A-записью указывал на эту ноду, а порт 80 был доступен из интернета. Новый сертификат
        dns-frontend подхватит сам, без перезапуска.
      </Notice>
      <Field label="Домен"
        hint={configured
          ? "Подставлен из настроек (имя DoH/DoT). Меняйте только осознанно."
          : "В настройках не задано имя DoH/DoT — задайте его там, чтобы домен подставлялся сам."}
        error={d !== "" && !valid ? "Похоже на не-домен: уберите http://, порт, путь и пробелы, IP не подойдёт." : undefined}>
        <input className="input mono" autoFocus value={domain} placeholder="dns.example.com"
          onChange={(e) => { setTouched(true); setDomain(e.target.value); }} />
      </Field>
      {mismatch && (
        <Notice kind="warn" title="Домен не совпадает с настроенным именем резолвера">
          Устройства обращаются к <b className="mono">{names.join(", ")}</b>. Сертификат уйдёт на
          другое имя — это правильно, только если вы заранее готовите смену домена. Иначе исправьте.
        </Notice>
      )}
      <Field label="Email для Let’s Encrypt" hint="Необязательно. Туда придут напоминания об истечении.">
        <input className="input mono" value={email} placeholder="you@example.com"
          onChange={(e) => setEmail(e.target.value)} />
      </Field>
      <label className="check">
        <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
        Перевыпустить принудительно, даже если текущий сертификат ещё годен
      </label>
      <label className="check">
        <input type="checkbox" checked={staging} onChange={(e) => setStaging(e.target.checked)} />
        Тестовый режим (staging) — без лимитов, но браузеры такому не доверяют
      </label>
      {result && result.ok && (
        <Notice kind="info" title="Готово">
          Сертификат для {result.domain} выпущен, действует до {result.not_after}.
        </Notice>
      )}
      {(error || (result && !result.ok)) && <CertFailure raw={error || result?.error || ""} />}
    </Modal>
  );
}

function CreateNode({ initialRole, onClose, onCreated }: {
  initialRole: Role;
  onClose: () => void;
  onCreated: (v: { name: string; role: string; install_command: string; bundle: string }) => void;
}) {
  const [role, setRole] = useState<Role>(initialRole);
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [mgmtPort, setMgmtPort] = useState(3333);
  const [ipv4, setIpv4] = useState("");
  const [found, setFound] = useState<string[]>([]);
  const [manualIP, setManualIP] = useState(false);
  const [looking, setLooking] = useState(false);
  const [lookErr, setLookErr] = useState("");
  const [relayPort, setRelayPort] = useState(8443);
  const [country, setCountry] = useState(initialRole === "ingress" ? "RU" : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  return (
    <Modal title="Добавить ноду" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            const body: Record<string, unknown> = {
              role, name, country, host: host.trim(),
              public_ipv4: manualIP ? ipv4 : "",
              mgmt_address: host.trim() ? `${host.trim()}:${mgmtPort}` : "",
            };
            if (role === "egress") body.relay_port = relayPort;
            const v = await api<{ name: string; role: string; install_command: string; bundle: string }>(
              "/nodes", { method: "POST", body, headers: { "Idempotency-Key": idemKey() } }
            );
            onCreated(v);
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать и выдать ключ</button>
      </>
    }>
      <Field label="Роль ноды">
        <Segmented<Role> value={role} wide tone={role === "ingress" ? "direct" : "managed"}
          onChange={(r) => { setRole(r); setCountry(r === "ingress" ? "RU" : ""); }} options={[
          { value: "ingress", label: "Точка входа", hint: "Принимает DNS и HTTPS от устройств. Сервер в России." },
          { value: "egress", label: "Точка выхода", hint: "Выходит к сайтам за рубежом. Её IP видит конечный сервис." },
        ]} />
      </Field>
      <Field label="Имя ноды" hint="Необязательно. По умолчанию сгенерируется автоматически.">
        <input className="input mono" placeholder={role === "ingress" ? "ingress-msk-01" : "egress-ams-01"} value={name}
          onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Страна"
        hint={role === "ingress"
          ? "Где стоит сервер. Точка входа должна быть в России."
          : "Её видят сайты. От страны зависит, какой регион вам покажут."}>
        <CountrySelect value={country} onChange={setCountry} />
      </Field>
      <div className="hostport">
        <Field label="Адрес сервера" hint="Имя или IP. Имя лучше: переживёт смену адреса.">
          <input className="input mono" placeholder="1c.est.example.net" value={host}
            onChange={(e) => { setHost(e.target.value); setFound([]); setIpv4(""); setLookErr(""); }}
            onBlur={async () => {
              const h = host.trim();
              if (!h) return;
              setLooking(true); setLookErr("");
              try {
                const r = await api<{ ipv4: string[] }>(`/nodes/resolve?host=${encodeURIComponent(h)}`);
                setFound(r.ipv4); setIpv4(r.ipv4[0] ?? "");
              } catch (e) { setLookErr(errText(e)); setFound([]); setIpv4(""); }
              finally { setLooking(false); }
            }} />
        </Field>
        <Field label="Порт управления" hint="По умолч. 3333.">
          <input className="input num" type="number" value={mgmtPort}
            onChange={(e) => setMgmtPort(Number(e.target.value))} />
        </Field>
      </div>
      {looking && <div className="small dim">проверяю имя…</div>}
      {!manualIP ? (
        <div className="small dim">
          Публичный IPv4 нода сообщит сама, как только поднимется.{" "}
          <button type="button" className="linklike" onClick={() => setManualIP(true)}>
            Указать вручную
          </button>
          {" "}— если сервер за NAT или адрес нужно задать иначе.
        </div>
      ) : (
        <>
          <Field label="Публичный IPv4"
            hint={role === "ingress"
              ? "Этот адрес DNS выдаёт устройствам. Пусто — значит возьмём со слов ноды."
              : "Пусто — значит возьмём со слов ноды."}
            error={lookErr || undefined}>
            {found.length > 1 ? (
              <select className="select mono" value={ipv4} onChange={(e) => setIpv4(e.target.value)}>
                {found.map((a) => <option key={a} value={a}>{a}</option>)}
              </select>
            ) : (
              <input className="input mono" placeholder="203.0.113.5" value={ipv4}
                onChange={(e) => setIpv4(e.target.value)} />
            )}
          </Field>
          {found.length > 1 && (
            <div className="small dim" style={{ marginTop: -8 }}>
              У имени {found.length} адреса — выберите тот, что принадлежит серверу.
              Несколько адресов часто означают прокси перед ним.
            </div>
          )}
        </>
      )}
      {role === "egress" && (
        <Field label="Порт туннеля" hint="Куда входные ноды подключаются по защищённому каналу.">
          <input className="input num" type="number" value={relayPort}
            onChange={(e) => setRelayPort(Number(e.target.value))} />
        </Field>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

function EditNode({ node, onClose, onSaved }: { node: Node; onClose: () => void; onSaved: () => void }) {
  const mgmtParts = splitHostPort(node.mgmt_address ?? "", 3333);
  const relayParts = splitHostPort(node.relay_endpoint ?? "", 8443);
  const [v4, setV4] = useState(node.public_ipv4 ?? "");
  const [v6, setV6] = useState(node.public_ipv6 ?? "");
  const [relayHost, setRelayHost] = useState(relayParts.host);
  const [relayPort, setRelayPort] = useState(relayParts.port);
  const [mgmtHost, setMgmtHost] = useState(mgmtParts.host);
  const [mgmtPort, setMgmtPort] = useState(mgmtParts.port);
  const [name, setName] = useState(node.name);
  const [country, setCountry] = useState(node.country ?? "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <Modal title={`Настройки ноды ${node.name}`} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api(`/nodes/${node.id}`, {
              method: "PATCH",
              headers: { "If-Match": String(node.version) },
              body: {
                name, country, public_ipv4: v4 || null, public_ipv6: v6 || null,
                mgmt_address: mgmtHost.trim() ? `${mgmtHost.trim()}:${mgmtPort}` : null,
                relay_endpoint: relayHost.trim() ? `${relayHost.trim()}:${relayPort}` : null,
              },
            });
            onSaved();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Сохранить</button>
      </>
    }>
      <Field label="Имя"><input className="input mono" value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="Страна" hint="Показывается флагом в списке нод и в группах.">
        <CountrySelect value={country} onChange={setCountry} />
      </Field>
      <div className="hostport">
        <Field label="Адрес управления" hint="Хост или IP агента.">
          <input className="input mono" value={mgmtHost} onChange={(e) => setMgmtHost(e.target.value)} placeholder="203.0.113.5" />
        </Field>
        <Field label="Порт" hint="По умолч. 3333.">
          <input className="input num" type="number" value={mgmtPort} onChange={(e) => setMgmtPort(Number(e.target.value))} />
        </Field>
      </div>
      <Field label="Публичный IPv4" hint="Этот адрес DNS выдаёт клиентам для управляемых доменов.">
        <input className="input mono" value={v4} onChange={(e) => setV4(e.target.value)} placeholder="203.0.113.5" />
      </Field>
      <Field label="Публичный IPv6" hint="AAAA публикуется только после успешной сквозной проверки IPv6.">
        <input className="input mono" value={v6} onChange={(e) => setV6(e.target.value)} placeholder="2001:db8::5" />
      </Field>
      {node.role === "egress" && (
        <div className="hostport">
          <Field label="Адрес туннеля" hint="Куда входные ноды подключаются по защищённому каналу.">
            <input className="input mono" value={relayHost} onChange={(e) => setRelayHost(e.target.value)} placeholder="198.51.100.9" />
          </Field>
          <Field label="Порт" hint="По умолч. 8443.">
            <input className="input num" type="number" value={relayPort} onChange={(e) => setRelayPort(Number(e.target.value))} />
          </Field>
        </div>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

// splitHostPort breaks "host:port" for the split address/port inputs. Splits on
// the last colon so bracketed IPv6 ([::1]:3333) keeps its host intact.
function splitHostPort(s: string, defPort: number): { host: string; port: number } {
  if (!s) return { host: "", port: defPort };
  const i = s.lastIndexOf(":");
  if (i < 0) return { host: s, port: defPort };
  const p = Number(s.slice(i + 1));
  return { host: s.slice(0, i), port: p > 0 ? p : defPort };
}

// CountrySelect — выбор страны списком с флагом. Флаг рисует система из кода
// страны, поэтому ни картинок, ни шрифта грузить не нужно.
function CountrySelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="row" style={{ gap: 10 }}>
      <span className="flag lg" aria-hidden="true">{flagOf(value) || "🏳️"}</span>
      <select className="select" value={value} onChange={(e) => onChange(e.target.value)} style={{ flex: 1 }}>
        <option value="">не указана</option>
        {COUNTRIES.map((c) => (
          <option key={c.code} value={c.code}>{flagOf(c.code)} {c.name}</option>
        ))}
      </select>
    </div>
  );
}

// Сервисы, которые ходят через ноду. Тот же список, что в карточке сервиса, но
// с другой стороны: заводя ноду, удобнее отметить её сервисы разом.
type SvcRow = {
  id: string; name: string; enabled: boolean;
  nodes: { id: string; role: string; country: string; name: string }[];
};

function NodeServicesModal({ node, onClose, onSaved }: {
  node: Node; onClose: () => void; onSaved: () => void;
}) {
  const list = useAsync<{ items: SvcRow[] }>(() => api("/services"), []);
  const [picked, setPicked] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const toast = useToast();

  const items = list.data?.items ?? [];
  // Первая отрисовка после загрузки: отмечаем то, что уже привязано.
  const current = picked ?? items.filter((s) => s.nodes.some((n) => n.id === node.id)).map((s) => s.id);

  // Страна сервиса по его нодам выхода. Если она чужая, ноду туда добавить
  // нельзя: отказ основной ноды увёл бы трафик в другую страну.
  const blockedBy = (s: SvcRow): string | null => {
    // Вход в этом списке значит «выходить прямо с него», поэтому его страна
    // участвует в сравнении наравне с заграничными нодами.
    const other = s.nodes.filter((n) => n.id !== node.id);
    const cc = [...new Set(other.map((n) => n.country || "?"))];
    if (cc.length === 0) return null;
    if (cc.length === 1 && cc[0] === (node.country || "?")) return null;
    return cc.map((c) => countryName(c) || "без страны").join(", ");
  };

  const toggle = (id: string) =>
    setPicked(current.includes(id) ? current.filter((x) => x !== id) : [...current, id]);

  const save = async () => {
    setBusy(true); setError("");
    try {
      await api(`/nodes/${node.id}/services`, { method: "PUT", body: { service_ids: current } });
      toast({ kind: "ok", title: "Сервисы ноды сохранены",
        body: "Соберите и выкатите конфигурацию, чтобы изменение доехало до нод." });
      onSaved();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Сервисы через ${node.name}`} onClose={onClose} wide footer={
      <>
        <span className="tiny dim">отмечено: {current.length}</span>
        <div className="spacer" />
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" onClick={save} disabled={busy || list.loading}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </>
    }>
      <Notice kind="info" title="Через какие сервисы выходит эта нода">
        Ноды выхода одного сервиса должны быть из одной страны — сервисы, уже привязанные к другой стране,
        отмечены и недоступны.
      </Notice>
      {error && <Notice kind="bad" title="Не сохранилось">{error}</Notice>}
      {list.loading ? <Spinner /> : (
        <div className="picklist">
          {items.map((s) => {
            const blocked = blockedBy(s);
            const on = current.includes(s.id);
            return (
              <label key={s.id} className={`pick${on ? " on" : ""}${blocked ? " disabled" : ""}`}>
                <input type="checkbox" checked={on} disabled={!!blocked} onChange={() => toggle(s.id)} />
                <span className="pick-name">{s.name}</span>
                {!s.enabled && <span className="tiny dim">выключен</span>}
                <span className="spacer" />
                {blocked
                  ? <span className="tiny" style={{ color: "var(--warn)" }}>уже через {blocked}</span>
                  : <span className="tiny dim">
                      {s.nodes.filter((n) => n.role === "egress").map((n) => flagOf(n.country)).join(" ")}
                    </span>}
              </label>
            );
          })}
        </div>
      )}
    </Modal>
  );
}
