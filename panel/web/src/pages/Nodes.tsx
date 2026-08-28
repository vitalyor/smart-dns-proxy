import { useState } from "react";
import { api, ago, timeTitle } from "../api";
import {
  Card, Confirm, Copyable, ErrorState, Field, Modal, Notice, Spinner,
  StatusBadge, errText, useAsync, useToast,
} from "../ui";
import { IconPlus, IconRefresh, IconTrash } from "../icons";

type Node = {
  id: string; name: string; role: string; status: string;
  public_ipv4: string | null; public_ipv6: string | null;
  relay_endpoint: string | null; mgmt_address: string; agent_version: string;
  last_seen_at: string | null; last_error: string;
  desired_sequence: number | null; applied_sequence: number | null;
  groups: string[]; version: number;
};

export default function Nodes() {
  const nodes = useAsync<{ items: Node[] }>(() => api("/nodes"), []);
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<{ name: string; role: string; install_command: string; bundle: string } | null>(null);
  const [removing, setRemoving] = useState<Node | null>(null);
  const [editing, setEditing] = useState<Node | null>(null);
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
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Добавить ноду</button>
      </div>

      <Notice kind="info" title="Ноды работают автономно">
        Если панель недоступна, нода продолжает обслуживать трафик на последней проверенной конфигурации.
        Расхождение между назначенной и применённой ревизией видно в колонке «Ревизия».
      </Notice>

      <Card title="Зарегистрированные ноды" tight>
        {nodes.error ? <ErrorState message={nodes.error} onRetry={nodes.reload} />
          : nodes.loading ? <Spinner />
          : nodes.data!.items.length === 0 ? (
            <div className="empty">
              <h3>Нод пока нет</h3>
              <p className="muted small">
                Добавьте ноду: панель выдаст команду с бандлом. Выполните её на сервере —
                панель сама подключится к ноде на её порт 3333.
              </p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating(true)}>
                <IconPlus />Добавить ноду
              </button>
            </div>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>Нода</th><th>Роль</th><th>Статус</th><th>Адреса</th>
                    <th>Ревизия</th><th>Heartbeat</th><th>Группы</th><th />
                  </tr>
                </thead>
                <tbody>
                  {nodes.data!.items.map((n) => (
                    <tr key={n.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{n.name}</div>
                        <div className="tiny dim mono">агент {n.agent_version || "—"}</div>
                      </td>
                      <td><span className={`badge ${n.role === "ingress" ? "direct" : "managed"}`}>{n.role}</span></td>
                      <td>
                        <StatusBadge status={n.status} />
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

      {creating && (
        <CreateNode
          onClose={() => setCreating(false)}
          onCreated={(v) => { setCreating(false); setIssued(v); nodes.reload(); }}
        />
      )}

      {issued && (
        <Modal title="Команда установки ноды" onClose={() => setIssued(null)} wide
          footer={<button className="btn primary" onClick={() => setIssued(null)}>Готово</button>}>
          <Notice kind="warn" title="Бандл показывается один раз">
            Бандл несёт TLS-идентичность ноды и пин панели; в базе он не хранится.
            Потеряли — удалите ноду и создайте заново.
          </Notice>

          <div className="eyebrow" style={{ marginTop: 4 }}>Шаг 1 — запустите на сервере</div>
          <div className="codeblock">{issued.install_command}</div>
          <Copyable value={issued.install_command} label="Копировать команду" />

          <div className="eyebrow" style={{ marginTop: 18 }}>Шаг 2 — вставьте бандл, когда установщик попросит</div>
          <div className="codeblock" style={{ maxHeight: 160, overflow: "auto", wordBreak: "break-all" }}>{issued.bundle}</div>
          <Copyable value={issued.bundle} label="Копировать бандл" />

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
    </>
  );
}

function CreateNode({ onClose, onCreated }: {
  onClose: () => void;
  onCreated: (v: { name: string; role: string; install_command: string; bundle: string }) => void;
}) {
  const [role, setRole] = useState("ingress");
  const [name, setName] = useState("");
  const [mgmt, setMgmt] = useState("");
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
            const body: Record<string, unknown> = { role, name, mgmt_address: mgmt, public_ipv4: ipv4 };
            if (role === "egress") body.relay_port = relayPort;
            const v = await api<{ name: string; role: string; install_command: string; bundle: string }>(
              "/nodes", { method: "POST", body, headers: { "Idempotency-Key": crypto.randomUUID() } }
            );
            onCreated(v);
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать и выдать бандл</button>
      </>
    }>
      <Field label="Роль ноды" hint="Ingress принимает DNS и HTTPS от устройств. Egress выходит к сервисам за рубежом.">
        <select className="select" value={role} onChange={(e) => setRole(e.target.value)}>
          <option value="ingress">ingress — точка входа</option>
          <option value="egress">egress — точка выхода</option>
        </select>
      </Field>
      <Field label="Имя ноды" hint="Необязательно. По умолчанию сгенерируется автоматически.">
        <input className="input mono" placeholder="ingress-msk-01" value={name}
          onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Адрес управления" hint="host:port, куда панель подключается к агенту. Порт по умолчанию 3333.">
        <input className="input mono" placeholder="203.0.113.5:3333" value={mgmt}
          onChange={(e) => setMgmt(e.target.value)} />
      </Field>
      <Field label="Публичный IPv4" hint={role === "ingress" ? "Этот адрес DNS выдаёт устройствам для управляемых доменов." : "На нём слушает relay; ingress подключаются сюда."}>
        <input className="input mono" placeholder="203.0.113.5" value={ipv4}
          onChange={(e) => setIpv4(e.target.value)} />
      </Field>
      {role === "egress" && (
        <Field label="Порт relay" hint="Куда ingress подключаются по mTLS.">
          <input className="input num" type="number" value={relayPort}
            onChange={(e) => setRelayPort(Number(e.target.value))} />
        </Field>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

function EditNode({ node, onClose, onSaved }: { node: Node; onClose: () => void; onSaved: () => void }) {
  const [v4, setV4] = useState(node.public_ipv4 ?? "");
  const [v6, setV6] = useState(node.public_ipv6 ?? "");
  const [relay, setRelay] = useState(node.relay_endpoint ?? "");
  const [mgmt, setMgmt] = useState(node.mgmt_address ?? "");
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
              body: { name, public_ipv4: v4 || null, public_ipv6: v6 || null, relay_endpoint: relay || null, mgmt_address: mgmt || null },
            });
            onSaved();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Сохранить</button>
      </>
    }>
      <Field label="Имя"><input className="input mono" value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="Адрес управления" hint="host:port, куда панель подключается к агенту (порт 3333).">
        <input className="input mono" value={mgmt} onChange={(e) => setMgmt(e.target.value)} placeholder="203.0.113.5:3333" />
      </Field>
      <Field label="Публичный IPv4" hint="Этот адрес DNS выдаёт клиентам для управляемых доменов.">
        <input className="input mono" value={v4} onChange={(e) => setV4(e.target.value)} placeholder="203.0.113.5" />
      </Field>
      <Field label="Публичный IPv6" hint="AAAA публикуется только после успешной сквозной проверки IPv6.">
        <input className="input mono" value={v6} onChange={(e) => setV6(e.target.value)} placeholder="2001:db8::5" />
      </Field>
      {node.role === "egress" && (
        <Field label="Адрес туннеля" hint="host:port, куда ingress-ноды подключаются по mTLS.">
          <input className="input mono" value={relay} onChange={(e) => setRelay(e.target.value)} placeholder="198.51.100.9:8443" />
        </Field>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}
