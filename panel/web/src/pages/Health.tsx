import { useState } from "react";
import { api, ago, fmtTime, timeTitle } from "../api";
import { Card, ErrorState, Spinner, StatusBadge, usePoll } from "../ui";

type Summary = {
  node_id: string; name: string; role: string; status: string; last_seen_at: string | null;
  success_last_hour: number | null; failure_last_hour: number | null; avg_latency_ms: number | null;
};
type Sample = {
  id: number; node_id: string | null; kind: string; success: boolean;
  latency_ms: number; error_code: string; detail: string; observed_at: string;
};
type Ev = { id: number; level: string; component: string; code: string; message: string; created_at: string };

export default function Health() {
  const [tab, setTab] = useState<"nodes" | "samples" | "events">("nodes");
  const summary = usePoll<{ items: Summary[] }>(() => api("/health/summary"), 15000, []);
  const samples = usePoll<{ items: Sample[] }>(() => api("/health/samples?limit=200"), 15000, []);
  const events = usePoll<{ items: Ev[] }>(() => api("/events?limit=200"), 15000, []);

  return (
    <>
      <div><div className="eyebrow">наблюдение</div><h1>Здоровье и события</h1></div>

      <div className="tabs" role="tablist">
        <button className={`tab${tab === "nodes" ? " active" : ""}`} role="tab" aria-selected={tab === "nodes"}
          onClick={() => setTab("nodes")}>Ноды</button>
        <button className={`tab${tab === "samples" ? " active" : ""}`} role="tab" aria-selected={tab === "samples"}
          onClick={() => setTab("samples")}>Проверки</button>
        <button className={`tab${tab === "events" ? " active" : ""}`} role="tab" aria-selected={tab === "events"}
          onClick={() => setTab("events")}>События</button>
      </div>

      {tab === "nodes" && (
        <Card tight>
          {summary.error ? <ErrorState message={summary.error} onRetry={summary.reload} />
            : summary.loading && !summary.data ? <Spinner />
            : (
              <div className="table-wrap">
                <table className="table">
                  <thead><tr><th>Нода</th><th>Роль</th><th>Статус</th><th>Heartbeat</th>
                    <th>Успешно / час</th><th>Ошибок / час</th><th>Средняя задержка</th></tr></thead>
                  <tbody>
                    {summary.data!.items.map((s) => {
                      const total = (s.success_last_hour ?? 0) + (s.failure_last_hour ?? 0);
                      const pct = total ? Math.round(((s.success_last_hour ?? 0) / total) * 100) : 0;
                      return (
                        <tr key={s.node_id}>
                          <td className="mono small">{s.name}</td>
                          <td><span className={`badge ${s.role === "ingress" ? "direct" : "managed"}`}>{s.role}</span></td>
                          <td><StatusBadge status={s.status} /></td>
                          <td className="small dim" title={timeTitle(s.last_seen_at)}>{ago(s.last_seen_at)}</td>
                          <td>
                            <div className="num small">{s.success_last_hour ?? 0}</div>
                            <div className="bar" style={{ marginTop: 4, width: 90 }}>
                              <span style={{ width: `${pct}%`, background: pct > 95 ? "var(--ok)" : pct > 80 ? "var(--warn)" : "var(--danger)" }} />
                            </div>
                          </td>
                          <td className="num small" style={{ color: s.failure_last_hour ? "var(--danger)" : undefined }}>
                            {s.failure_last_hour ?? 0}
                          </td>
                          <td className="num small">{s.avg_latency_ms ? `${Math.round(s.avg_latency_ms)} мс` : "—"}</td>
                        </tr>
                      );
                    })}
                    {summary.data!.items.length === 0 && (
                      <tr><td colSpan={7}><div className="empty" style={{ padding: 28 }}>
                        <h3>Нет данных</h3><p className="muted small">Данные появятся, как только зарегистрируется первая нода.</p>
                      </div></td></tr>
                    )}
                  </tbody>
                </table>
              </div>
            )}
        </Card>
      )}

      {tab === "samples" && (
        <Card tight>
          {samples.loading && !samples.data ? <Spinner /> : (
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Время</th><th>Тип</th><th>Результат</th><th>Задержка</th><th>Подробности</th></tr></thead>
                <tbody>
                  {samples.data?.items.map((s) => (
                    <tr key={s.id}>
                      <td className="small dim" title={timeTitle(s.observed_at)}>{fmtTime(s.observed_at)}</td>
                      <td><span className="badge plain">{s.kind}</span></td>
                      <td>{s.success ? <span className="badge ok">успех</span>
                        : <span className="badge bad">{s.error_code || "ошибка"}</span>}</td>
                      <td className="num small">{s.latency_ms ? `${s.latency_ms} мс` : "—"}</td>
                      <td className="tiny dim mono" style={{ maxWidth: 460, wordBreak: "break-all" }}>{s.detail}</td>
                    </tr>
                  ))}
                  {samples.data?.items.length === 0 && (
                    <tr><td colSpan={5}><div className="empty" style={{ padding: 28 }}>
                      <h3>Проверок ещё не было</h3>
                      <p className="muted small">Задайте домен для проверки в настройках сервиса — панель начнёт проверять реальный путь.</p>
                    </div></td></tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === "events" && (
        <Card tight>
          {events.loading && !events.data ? <Spinner /> : (
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Время</th><th>Уровень</th><th>Компонент</th><th>Событие</th></tr></thead>
                <tbody>
                  {events.data?.items.map((e) => (
                    <tr key={e.id}>
                      <td className="small dim" title={timeTitle(e.created_at)}>{fmtTime(e.created_at)}</td>
                      <td><span className={`badge ${e.level === "error" ? "bad" : e.level === "warn" ? "warn" : "plain"}`}>{e.level}</span></td>
                      <td className="mono tiny">{e.component}</td>
                      <td className="small">{e.message}<div className="tiny dim mono">{e.code}</div></td>
                    </tr>
                  ))}
                  {events.data?.items.length === 0 && (
                    <tr><td colSpan={4}><div className="empty" style={{ padding: 28 }}>
                      <h3>Событий нет</h3><p className="muted small">Здесь появятся регистрации нод, выкаты и отказы.</p>
                    </div></td></tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </>
  );
}
