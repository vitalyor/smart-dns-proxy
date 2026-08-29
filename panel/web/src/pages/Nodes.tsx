import { useState } from "react";
import { api, ago, timeTitle } from "../api";
import {
  Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Segmented, Spinner,
  StatusBadge, errText, usePoll, useToast,
} from "../ui";

type Role = "ingress" | "egress";
import { IconPlus, IconRefresh, IconTrash } from "../icons";

type Node = {
  id: string; name: string; role: string; status: string;
  public_ipv4: string | null; public_ipv6: string | null;
  relay_endpoint: string | null; mgmt_address: string; agent_version: string;
  last_seen_at: string | null; last_error: string;
  desired_sequence: number | null; applied_sequence: number | null;
  groups: string[]; cert_days_left: number | null; version: number;
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
                      <col style={{ width: "18%" }} /><col style={{ width: "11%" }} />
                      <col style={{ width: "22%" }} /><col style={{ width: "11%" }} />
                      <col style={{ width: "11%" }} /><col style={{ width: "9%" }} />
                      <col style={{ width: "18%" }} />
                    </colgroup>
                    <thead>
                      <tr>
                        <th>Нода</th><th>Статус</th><th>Адреса</th>
                        <th>Конфигурация</th><th>Heartbeat</th><th>Группы</th><th />
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((n) => (
                        <tr key={n.id}>
                          <td>
                            <div className="row" style={{ gap: 8 }}>
                              <span className={`node-dot ${meta.cls}`} aria-hidden="true" />
                              <div style={{ fontWeight: 550 }}>{n.name}</div>
                            </div>
                            <div className="tiny dim mono" style={{ marginLeft: 16 }}>агент {n.agent_version || "—"}</div>
                          </td>
                          <td>
                            <StatusBadge status={n.status} />
                            {n.cert_days_left != null && (
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
                          <td className="tiny mono dim">{n.groups?.length ? n.groups.join(", ") : "—"}</td>
                          <td className="actions">
                            <button className="btn sm ghost" onClick={() => setEditing(n)}>Изменить</button>
                            {n.role === "ingress" && (
                              <button className="btn sm ghost" onClick={() => setCertNode(n)}>Сертификат</button>
                            )}
                            <button className="btn sm ghost" onClick={async () => {
                              try {
                                await api(`/nodes/${n.id}/maintenance`, {
                                  method: "POST", body: { enabled: n.status !== "maintenance" },
                                });
                                toast({ kind: "ok", title: n.status === "maintenance" ? "Обслуживание снято" : "Нода в обслуживании" });
                                nodes.reload();
                              } catch (e) { toast({ kind: "bad", title: "Не удалось изменить режим", body: errText(e) }); }
                            }}>
                              {n.status === "maintenance" ? "Вернуть в работу" : "Обслуживание"}
                            </button>
                            <button className="btn sm ghost danger" aria-label={`Удалить ${n.name}`}
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
    </>
  );
}

// CertModal issues a DoT/DoH certificate for an ingress node via the panel →
// node ACME HTTP-01 flow. The node opens :80 only for the challenge.
function CertModal({ node, onClose, onIssued }: {
  node: Node; onClose: () => void; onIssued: () => void;
}) {
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [force, setForce] = useState(false);
  const [staging, setStaging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ ok: boolean; not_after?: string; domain?: string; error?: string } | null>(null);
  const toast = useToast();

  const issue = async () => {
    setBusy(true); setError(""); setResult(null);
    try {
      const r = await api<{ ok: boolean; not_after?: string; domain?: string; error?: string }>(
        `/nodes/${node.id}/certificate`,
        { method: "POST", body: { domain: domain.trim(), email: email.trim(), force, staging } },
      );
      setResult(r);
      if (r.ok) { toast({ kind: "ok", title: "Сертификат выпущен", body: `Действует до ${r.not_after}` }); onIssued(); }
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Сертификат для ${node.name}`} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Закрыть</button>
        <button className="btn primary" disabled={busy || !domain.trim()} onClick={issue}>
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
      <Field label="Домен" hint="Имя, по которому устройства обращаются к DoT/DoH.">
        <input className="input mono" autoFocus value={domain} placeholder="dns.example.com"
          onChange={(e) => setDomain(e.target.value)} />
      </Field>
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
      {result && !result.ok && (
        <div className="notice bad" role="alert"><span className="notice-bar" />
          <div className="n-body">Не удалось: {result.error}</div></div>
      )}
      {result && result.ok && (
        <Notice kind="info" title="Готово">
          Сертификат для {result.domain} выпущен, действует до {result.not_after}.
        </Notice>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
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
  const [mgmtHost, setMgmtHost] = useState("");
  const [mgmtPort, setMgmtPort] = useState(3333);
  const [ipv4, setIpv4] = useState("");
  const [relayPort, setRelayPort] = useState(8443);
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
              role, name, public_ipv4: ipv4,
              mgmt_address: mgmtHost.trim() ? `${mgmtHost.trim()}:${mgmtPort}` : "",
            };
            if (role === "egress") body.relay_port = relayPort;
            const v = await api<{ name: string; role: string; install_command: string; bundle: string }>(
              "/nodes", { method: "POST", body, headers: { "Idempotency-Key": crypto.randomUUID() } }
            );
            onCreated(v);
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать и выдать ключ</button>
      </>
    }>
      <Field label="Роль ноды">
        <Segmented<Role> value={role} onChange={setRole} wide tone={role === "ingress" ? "direct" : "managed"} options={[
          { value: "ingress", label: "Точка входа", hint: "Принимает DNS и HTTPS от устройств. Сервер в России." },
          { value: "egress", label: "Точка выхода", hint: "Выходит к сайтам за рубежом. Её IP видит конечный сервис." },
        ]} />
      </Field>
      <Field label="Имя ноды" hint="Необязательно. По умолчанию сгенерируется автоматически.">
        <input className="input mono" placeholder={role === "ingress" ? "ingress-msk-01" : "egress-ams-01"} value={name}
          onChange={(e) => setName(e.target.value)} />
      </Field>
      <div className="hostport">
        <Field label="Адрес управления" hint="Хост или IP, куда панель подключается к агенту.">
          <input className="input mono" placeholder="203.0.113.5" value={mgmtHost}
            onChange={(e) => setMgmtHost(e.target.value)} />
        </Field>
        <Field label="Порт" hint="По умолч. 3333.">
          <input className="input num" type="number" value={mgmtPort}
            onChange={(e) => setMgmtPort(Number(e.target.value))} />
        </Field>
      </div>
      <Field label="Публичный IPv4" hint={role === "ingress" ? "Этот адрес DNS выдаёт устройствам для управляемых доменов." : "На нём слушает туннель; входные ноды подключаются сюда."}>
        <input className="input mono" placeholder="203.0.113.5" value={ipv4}
          onChange={(e) => setIpv4(e.target.value)} />
      </Field>
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
                name, public_ipv4: v4 || null, public_ipv6: v6 || null,
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
