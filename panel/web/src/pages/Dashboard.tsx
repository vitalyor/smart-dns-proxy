import { useState } from "react";
import { Link } from "react-router-dom";
import { api, ago, plural, timeTitle } from "../api";
import { Card, ErrorState, Notice, Section, Spinner, Stat, usePoll } from "../ui";
import { IconArrowIn, IconArrowOut, IconGrid, IconLayers } from "../icons";

type NodeStat = { role: string; status: string; count: number; last_seen: string | null };
type SvcStat = {
  id: string; name: string; slug: string; enabled: boolean; rules: number;
  ingress_group: string | null; egress_group: string | null;
  last_probe: boolean | null; latency_ms: number | null;
};
type Alert = { level: string; code: string; message: string; hint: string; action?: string; href?: string };
type DeployNode = {
  name: string; role: string; applied_sequence: number | null;
  desired_sequence: number | null; behind: boolean; stale: boolean;
};
type Ev = { id: number; level: string; component: string; code: string; message: string; created_at: string };

type Data = {
  nodes: NodeStat[]; services: SvcStat[];
  active_revision: { sequence: number; state: string; activated_at: string | null } | null;
  pending_rule_approvals: number; nodes_with_drift: number; nodes_stale: number;
  deploy_nodes: DeployNode[];
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

      <DeployBar nodes={data.deploy_nodes ?? []} seq={data.active_revision?.sequence ?? null} />

      {data.alerts.length > 0 && (
        <div className="col" style={{ gap: 10 }}>
          {data.alerts.map((a) => (
            <Notice key={a.code} kind={a.level === "error" ? "bad" : a.level === "warn" ? "warn" : "info"}
              title={a.message}>
              {a.hint}
              {a.href && (
                <div style={{ marginTop: 10 }}>
                  <Link className="btn sm" to={a.href}>{a.action}</Link>
                </div>
              )}
            </Notice>
          ))}
        </div>
      )}

      <Section title="Инфраструктура" note="что сейчас держит трафик">
        <div className="grid g4">
          <Stat icon={<IconArrowIn />} tone={ingressOk ? "direct" : "bad"} label="Входные ноды"
            value={`${healthy("ingress")}/${total("ingress")}`} note="принимают DNS и HTTPS"
            state={ingressOk ? "ok" : "bad"} />
          <Stat icon={<IconArrowOut />} tone={egressOk ? "managed" : "bad"} label="Выходные ноды"
            value={`${healthy("egress")}/${total("egress")}`} note="выходят к сервисам"
            state={egressOk ? "ok" : "bad"} />
          <Stat icon={<IconGrid />} tone="violet" label="Сервисы"
            value={String(data.services.filter((s) => s.enabled).length)}
            note={plural(data.services.reduce((a, s) => a + s.rules, 0), "правило", "правила", "правил")} />
          <Stat icon={<IconLayers />} tone={data.nodes_with_drift ? "warn" : "ok"} label="Расхождение"
            value={String(data.nodes_with_drift)} note="нод ещё не применили конфигурацию"
            state={data.nodes_with_drift ? "warn" : "ok"} />
        </div>
      </Section>

      <Section title="Сервисы" note="что проходит через инфраструктуру"
        actions={<Link className="btn sm" to="/services">Все сервисы</Link>}>
        <ServiceDigest items={data.services} />
      </Section>

      <Section title="Последние события" note="лента control plane"
        actions={<Link className="btn sm" to="/health">Все события</Link>}>
        <Card tight>
          {data.events.length === 0 ? (
            <div className="empty"><h3>Событий пока нет</h3>
              <p className="muted small">Здесь появятся выкаты, обновления списков и отказы нод.</p></div>
          ) : (
            <ul className="feed">
              {data.events.slice(0, 8).map((e) => (
                <li key={e.id}>
                  <span className={`badge ${e.level === "error" ? "bad" : e.level === "warn" ? "warn" : "plain"}`}>
                    {e.component}
                  </span>
                  <span className="small" style={{ flex: 1, minWidth: 0 }}>{e.message}</span>
                  <span className="tiny dim" title={timeTitle(e.created_at)}>{ago(e.created_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </Section>
    </>
  );
}

// DeployBar — состояние выката одной строкой, всегда на виду. Зелёная, когда все
// ноды на назначенной конфигурации; жёлтая, когда кто-то отстал; красная, когда
// нода молчит. Именно этого не хватало: расхождение было цифрой в плитке.
function DeployBar({ nodes, seq }: { nodes: DeployNode[]; seq: number | null }) {
  if (nodes.length === 0) return null;
  const behind = nodes.filter((n) => n.behind);
  const stale = nodes.filter((n) => n.stale);
  const tone = stale.length ? "bad" : behind.length ? "warn" : "ok";
  const applied = nodes.length - behind.length;

  return (
    <div className={`deploybar ${tone}`}>
      <span className="deploybar-dot" aria-hidden="true" />
      <div className="deploybar-main">
        <div className="deploybar-title">
          {tone === "ok" && <>Конфигурация {seq !== null ? `#${seq}` : "—"} применена на всех нодах</>}
          {tone === "warn" && <>Конфигурация {seq !== null ? `#${seq}` : "—"} применена на {applied} из {nodes.length} нод</>}
          {tone === "bad" && <>Нет связи с {stale.length === 1 ? "нодой" : "нодами"}: {stale.map((n) => n.name).join(", ")}</>}
        </div>
        <div className="deploybar-sub">
          {tone === "ok" && "Все ноды подтвердили приём. Изменения применяются без перезапуска."}
          {tone === "warn" && <>Отстают: {behind.map((n) => `${n.name} (#${n.applied_sequence ?? "—"})`).join(", ")}. Панель досылает сама.</>}
          {tone === "bad" && "Трафик идёт по последней рабочей конфигурации. Новые изменения до этих нод не доедут."}
        </div>
      </div>
      <div className="deploybar-nodes">
        {nodes.map((n) => (
          <span key={n.name} className={`chip ${n.stale ? "bad" : n.behind ? "warn" : "ok"}`}
            title={`${n.role} · применена #${n.applied_sequence ?? "—"}${n.behind ? `, назначена #${n.desired_sequence ?? "—"}` : ""}`}>
            {n.name}
          </span>
        ))}
      </div>
      <Link className="btn sm" to="/revisions">Выкаты</Link>
    </div>
  );
}

// ServiceDigest — восемьдесят строк таблицы не читаются. Сначала счёт по
// состояниям, потом только то, что требует внимания; полный список живёт на
// своей странице.
function ServiceDigest({ items }: { items: SvcStat[] }) {
  const [tab, setTab] = useState<"attention" | "all">("attention");
  if (items.length === 0) {
    return (
      <Card tight>
        <div className="empty">
          <h3>Ни одного сервиса</h3>
          <p className="muted small">Пройдите быстрый старт: набор правил → сервис → выкат.</p>
          <Link className="btn primary" to="/setup" style={{ marginTop: 14 }}>Открыть быстрый старт</Link>
        </div>
      </Card>
    );
  }
  const on = items.filter((s) => s.enabled);
  const off = items.filter((s) => !s.enabled);
  const failed = on.filter((s) => s.last_probe === false);
  const unknown = on.filter((s) => s.last_probe === null);
  const rules = items.reduce((a, s) => a + s.rules, 0);
  const attention = [...failed, ...unknown];
  const rows = tab === "attention" ? attention : items;

  return (
    <Card tight>
      <div className="digest">
        <div className="digest-nums">
          <b>{on.length}</b> включено<span className="sep">·</span>
          <b>{off.length}</b> выключено<span className="sep">·</span>
          <b>{rules}</b> {plural(rules, "правило", "правила", "правил").split(" ")[1]}
        </div>
        <div className="seg tone-neutral">
          <button className={`seg-btn${tab === "attention" ? " sel" : ""}`} onClick={() => setTab("attention")}>
            Требуют внимания{attention.length ? ` · ${attention.length}` : ""}
          </button>
          <button className={`seg-btn${tab === "all" ? " sel" : ""}`} onClick={() => setTab("all")}>
            Все · {items.length}
          </button>
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="digest-ok">
          Все включённые сервисы прошли проверку доступности.
        </div>
      ) : (
        <div className="table-wrap" style={{ maxHeight: 420, overflowY: "auto" }}>
          <table className="table">
            <thead><tr><th>Сервис</th><th>Правил</th><th>Маршрут</th><th>Проба</th></tr></thead>
            <tbody>
              {rows.map((s) => (
                <tr key={s.id}>
                  <td>
                    <div className="row" style={{ gap: 8 }}>
                      <span style={{ fontWeight: 550 }}>{s.name}</span>
                      {!s.enabled && <span className="badge">выключен</span>}
                    </div>
                    <div className="tiny dim mono">{s.slug}</div>
                  </td>
                  <td className="num">{s.rules}</td>
                  <td className="tiny mono dim">{s.ingress_group ?? "—"} → {s.egress_group ?? "—"}</td>
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
