import { Link } from "react-router-dom";
import { api } from "../api";
import { Card, ErrorState, Spinner, useAsync } from "../ui";
import { IconCheck } from "../icons";

type Status = {
  nodes: { role: string; status: string; count: number }[];
  services: unknown[];
  active_revision: { sequence: number } | null;
};

/**
 * The quick start is a checklist, not a wizard: each step links to the page
 * that actually owns it, so the operator learns the panel's real structure
 * instead of a one-off flow they cannot repeat.
 */
export default function Setup() {
  const rules = useAsync<{ items: unknown[] }>(() => api("/rule-sets"), []);
  const groupsIn = useAsync<{ items: unknown[] }>(() => api("/ingress-groups"), []);
  const groupsEg = useAsync<{ items: unknown[] }>(() => api("/egress-groups"), []);
  const dash = useAsync<Status>(() => api("/dashboard"), []);
  const devices = useAsync<{ items: unknown[] }>(() => api("/device-profiles"), []);

  if (dash.error) return <ErrorState message={dash.error} onRetry={dash.reload} />;
  if (dash.loading || !dash.data) return <Spinner />;

  const nodesOf = (role: string) => dash.data!.nodes.filter((n) => n.role === role).reduce((a, b) => a + b.count, 0);

  const steps = [
    {
      title: "Добавить ingress-ноду",
      body: "Сервер в России, куда устройства отправляют DNS-запросы и TLS-соединения.",
      done: nodesOf("ingress") > 0,
      to: "/nodes", cta: "Добавить ноду",
    },
    {
      title: "Добавить egress-ноду",
      body: "Зарубежный сервер, чей IP увидит конечный сервис.",
      done: nodesOf("egress") > 0,
      to: "/nodes", cta: "Добавить ноду",
    },
    {
      title: "Собрать группы",
      body: "Сервисы ссылаются на группы, а не на отдельные ноды — так резерв настраивается один раз.",
      done: (groupsIn.data?.items.length ?? 0) > 0 && (groupsEg.data?.items.length ?? 0) > 0,
      to: "/ingress-groups", cta: "Открыть группы",
    },
    {
      title: "Создать набор правил",
      body: "Список доменов, которые надо направлять через инфраструктуру. Можно начать со встроенного пресета.",
      done: (rules.data?.items.length ?? 0) > 0,
      to: "/rule-sets", cta: "Создать набор",
    },
    {
      title: "Создать сервис",
      body: "Связывает набор правил с ingress- и egress-группой и задаёт TTL, порты и домен для проверки.",
      done: dash.data.services.length > 0,
      to: "/services", cta: "Создать сервис",
    },
    {
      title: "Собрать и выкатить ревизию",
      body: "Конфигурация становится неизменяемым снимком, который агенты применяют атомарно.",
      done: dash.data.active_revision !== null,
      to: "/revisions", cta: "Открыть ревизии",
    },
    {
      title: "Настроить устройство",
      body: "Профиль для Android, Apple, Windows или роутера с инструкцией по проверке.",
      done: (devices.data?.items.length ?? 0) > 0,
      to: "/devices", cta: "Создать профиль",
    },
  ];

  const doneCount = steps.filter((s) => s.done).length;
  const next = steps.find((s) => !s.done);

  return (
    <>
      <div><div className="eyebrow">начало работы</div><h1>Быстрый старт</h1></div>

      <Card>
        <div className="row" style={{ gap: 16 }}>
          <div style={{ flex: 1, minWidth: 200 }}>
            <div className="eyebrow">выполнено</div>
            <div className="tile-value" style={{ marginTop: 4 }}>{doneCount} / {steps.length}</div>
          </div>
          <div style={{ flex: 3, minWidth: 220 }}>
            <div className="bar" style={{ height: 8 }}>
              <span style={{ width: `${(doneCount / steps.length) * 100}%` }} />
            </div>
            <p className="small muted" style={{ marginBottom: 0, marginTop: 10 }}>
              {next ? `Следующий шаг: ${next.title.toLowerCase()}.`
                : "Всё готово. Проверьте на устройстве, что управляемый домен резолвится в адрес ingress, а обычный — в настоящий."}
            </p>
          </div>
        </div>
      </Card>

      <ol style={{ listStyle: "none", padding: 0, margin: 0, display: "grid", gap: 12 }}>
        {steps.map((s, i) => (
          <li key={s.title} className="card" style={{ padding: 16 }}>
            <div className="row" style={{ gap: 14, alignItems: "flex-start" }}>
              <span aria-hidden="true" style={{
                width: 28, height: 28, borderRadius: 8, flex: "none",
                display: "grid", placeItems: "center",
                background: s.done ? "var(--direct)" : "var(--surface-2)",
                color: s.done ? "#06201e" : "var(--text-3)",
                border: "1px solid var(--line)",
                fontFamily: "var(--mono)", fontSize: 12, fontWeight: 600,
              }}>{s.done ? <IconCheck size={16} /> : i + 1}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600 }}>{s.title}</div>
                <div className="small muted" style={{ marginTop: 2 }}>{s.body}</div>
              </div>
              {s.done ? <span className="badge ok">готово</span>
                : <Link className="btn sm primary" to={s.to}>{s.cta}</Link>}
            </div>
          </li>
        ))}
      </ol>

      <Card title="Как проверить результат" eyebrow="ручная сверка">
        <p className="small muted" style={{ marginTop: 0 }}>
          Замените <span className="mono">УПРАВЛЯЕМЫЙ-ДОМЕН</span> на домен из вашего набора правил,
          а <span className="mono">IP-INGRESS</span> — на адрес ingress-ноды.
        </p>
        <div className="codeblock">{`# 1. Управляемый домен должен вернуть адрес ingress-ноды
dig +short УПРАВЛЯЕМЫЙ-ДОМЕН @IP-INGRESS

# 2. Обычный домен должен вернуть свой настоящий адрес
dig +short example.org @IP-INGRESS

# 3. Сертификат должен принадлежать настоящему сервису, а не нам
openssl s_client -connect IP-INGRESS:443 -servername УПРАВЛЯЕМЫЙ-ДОМЕН </dev/null 2>/dev/null \\
  | openssl x509 -noout -subject -issuer

# 4. Сервис должен видеть IP egress-ноды
curl -s https://УПРАВЛЯЕМЫЙ-ДОМЕН/ -o /dev/null -w '%{remote_ip}\\n'`}</div>
      </Card>
    </>
  );
}
