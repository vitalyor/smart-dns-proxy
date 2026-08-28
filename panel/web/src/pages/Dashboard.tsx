import { Link } from "react-router-dom";
import { api, ago, fmtTime, plural, timeTitle } from "../api";
import { Card, ErrorState, Notice, Spinner, StatusBadge, usePoll } from "../ui";

type NodeStat = { role: string; status: string; count: number; last_seen: string | null };
type SvcStat = {
  id: string; name: string; slug: string; enabled: boolean; rules: number;
  ingress_group: string | null; egress_group: string | null;
  last_probe: boolean | null; latency_ms: number | null;
};
type Alert = { level: string; code: string; message: string; hint: string };
type Ev = { id: number; level: string; component: string; code: string; message: string; created_at: string };

type Data = {
  nodes: NodeStat[]; services: SvcStat[];
  active_revision: { sequence: number; state: string; activated_at: string | null } | null;
  pending_rule_approvals: number; nodes_with_drift: number; nodes_stale: number;
  events: Ev[]; alerts: Alert[]; lab_mode: boolean;
};

export default function Dashboard() {
  const { data, error, loading, reload } = usePoll<Data>(() => api("/dashboard"), 15000, []);

  if (error) return <ErrorState message={error} onRetry={reload} />;
  if (loading && !data) return <Spinner />;
  if (!data) return null;

  const total = (role: string) => data.nodes.filter((n) => n.role === role).reduce((a, b) => a + b.count, 0);
  const healthy = (role: string) =>
    data.nodes.filter((n) => n.role === role && n.status === "healthy").reduce((a, b) => a + b.count, 0);

  const ingressOk = healthy("ingress") > 0;
  const egressOk = healthy("egress") > 0;
  const managedLive = ingressOk && egressOk;
  const probeOk = data.services.some((s) => s.last_probe);

  return (
    <>
      {/* Signature: the product's two traffic paths, drawn from live state. */}
      <section className="flow">
        <div className="flow-title">
          <h2>Путь трафика</h2>
          <span className="eyebrow">
            {data.active_revision ? `конфигурация #${data.active_revision.sequence}` : "конфигурация не применена"}
          </span>
        </div>

        <div className="flow-track">
          <div className="flow-lane">
            <div className="lane-label managed">управляемые<br />домены</div>
            <div className="lane-hops">
              <Hop name="устройство" meta="ваш DNS" />
              <Wire kind="managed" live={ingressOk} />
              <Hop name="ingress" meta={`${healthy("ingress")}/${total("ingress")} онлайн`} bad={!ingressOk} />
              <Wire kind="managed" live={managedLive} />
              <Hop name="egress" meta={`${healthy("egress")}/${total("egress")} онлайн`} bad={!egressOk} />
              <Wire kind="managed" live={probeOk} />
              <Hop name="сервис" meta={probeOk ? "проба прошла" : "проба не подтверждена"} bad={!probeOk} />
            </div>
          </div>

          <div className="flow-lane">
            <div className="lane-label direct">обычные<br />домены</div>
            <div className="lane-hops">
              <Hop name="устройство" meta="ваш DNS" />
              <Wire kind="direct" live={ingressOk} />
              <Hop name="Unbound" meta="рекурсия + DNSSEC" bad={!ingressOk} />
              <Wire kind="direct" live={ingressOk} />
              <Hop name="сайт напрямую" meta="ваш IP провайдера" />
            </div>
          </div>
        </div>
      </section>

      {data.alerts.length > 0 && (
        <div className="col" style={{ gap: 10 }}>
          {data.alerts.map((a) => (
            <Notice key={a.code} kind={a.level === "error" ? "bad" : a.level === "warn" ? "warn" : "info"}
              title={a.message}>{a.hint}</Notice>
          ))}
        </div>
      )}

      <div className="grid g4">
        <Tile label="Входные ноды" value={`${healthy("ingress")}/${total("ingress")}`}
          note="нод принимают DNS и HTTPS" state={ingressOk ? "ok" : "bad"} />
        <Tile label="Выходные ноды" value={`${healthy("egress")}/${total("egress")}`}
          note="нод выходят к сервисам" state={egressOk ? "ok" : "bad"} />
        <Tile label="Сервисы" value={String(data.services.filter((s) => s.enabled).length)}
          note={plural(data.services.reduce((a, s) => a + s.rules, 0), "правило", "правила", "правил")} />
        <Tile label="Расхождение" value={String(data.nodes_with_drift)}
          note="нод ещё не применили конфигурацию" state={data.nodes_with_drift ? "warn" : "ok"} />
      </div>

      <div className="grid g2">
        <Card title="Сервисы" eyebrow="что проходит через инфраструктуру" tight
          actions={<Link className="btn sm" to="/services">Настроить</Link>}>
          {data.services.length === 0 ? (
            <div className="empty">
              <h3>Ни одного сервиса</h3>
              <p className="muted small">Пройдите быстрый старт: набор правил → сервис → выкат.</p>
              <Link className="btn primary" to="/setup" style={{ marginTop: 14 }}>Открыть быстрый старт</Link>
            </div>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr><th>Сервис</th><th>Правил</th><th>Маршрут</th><th>Проба</th></tr>
                </thead>
                <tbody>
                  {data.services.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{s.name}</div>
                        <div className="tiny dim mono">{s.slug}</div>
                      </td>
                      <td className="num">{s.rules}</td>
                      <td className="tiny mono dim">
                        {s.ingress_group ?? "—"} → {s.egress_group ?? "—"}
                      </td>
                      <td>
                        {s.last_probe === null ? <span className="badge">нет данных</span>
                          : s.last_probe ? <span className="badge ok">{s.latency_ms} мс</span>
                          : <span className="badge bad">не прошла</span>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>

        <Card title="Последние события" eyebrow="лента control plane" tight
          actions={<Link className="btn sm" to="/health">Все события</Link>}>
          {data.events.length === 0 ? (
            <div className="empty"><h3>Событий пока нет</h3><p className="muted small">Здесь появятся выкаты, обновления списков и отказы нод.</p></div>
          ) : (
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {data.events.slice(0, 9).map((e) => (
                <li key={e.id} style={{ padding: "10px 16px", borderBottom: "1px solid var(--line-soft)" }}>
                  <div className="row" style={{ gap: 8 }}>
                    <span className={`badge ${e.level === "error" ? "bad" : e.level === "warn" ? "warn" : "plain"}`}>
                      {e.component}
                    </span>
                    <span className="small" style={{ flex: 1, minWidth: 0 }}>{e.message}</span>
                    <span className="tiny dim" title={timeTitle(e.created_at)}>{ago(e.created_at)}</span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <Card title="Состояние нод" eyebrow="по ролям и статусам" tight
        actions={<Link className="btn sm" to="/nodes">Управление</Link>}>
        <div className="table-wrap">
          <table className="table">
            <thead><tr><th>Роль</th><th>Статус</th><th>Нод</th><th>Последний контакт</th></tr></thead>
            <tbody>
              {data.nodes.length === 0 && (
                <tr><td colSpan={4}><div className="empty" style={{ padding: 24 }}>
                  <h3>Нод пока нет</h3>
                  <p className="muted small">Создайте одноразовый токен и запустите установщик на сервере.</p>
                  <Link className="btn primary" to="/nodes" style={{ marginTop: 12 }}>Добавить ноду</Link>
                </div></td></tr>
              )}
              {data.nodes.map((n, i) => (
                <tr key={i}>
                  <td><span className={`badge ${n.role === "ingress" ? "direct" : "managed"}`}>{n.role}</span></td>
                  <td><StatusBadge status={n.status} /></td>
                  <td className="num">{n.count}</td>
                  <td className="small dim" title={timeTitle(n.last_seen)}>{fmtTime(n.last_seen)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  );
}

function Hop({ name, meta, bad }: { name: string; meta: string; bad?: boolean }) {
  return (
    <div className={`hop${bad ? " bad" : ""}`}>
      <span className="hop-name">{name}</span>
      <span className="hop-meta">{meta}</span>
    </div>
  );
}

function Wire({ kind, live }: { kind: "managed" | "direct"; live: boolean }) {
  return <span className={`wire ${kind} ${live ? "live" : "dead"}`} />;
}

function Tile({ label, value, note, state }: { label: string; value: string; note: string; state?: string }) {
  return (
    <div className="card tile">
      <div className="tile-label">{label}</div>
      <div className={`tile-value ${state ?? ""}`}>{value}</div>
      <div className="tile-note">{note}</div>
    </div>
  );
}
