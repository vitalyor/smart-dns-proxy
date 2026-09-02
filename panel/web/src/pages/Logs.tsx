import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import { Card, ErrorState, Notice, Spinner, useAsync } from "../ui";
import { IconPlay, IconRefresh } from "../icons";

type Entry = {
  seq: number; ts: number; client: string; proto: string; name: string;
  type: string; decision: string; rcode: string; ms: number;
};
type LogResp = { available?: boolean; seq: number; entries: Entry[] };
type Node = { id: string; name: string; role: string };

const CAP = 600; // держим в памяти столько последних строк

type Kind = "proxied" | "direct" | "denied";
function kindOf(decision: string): Kind {
  if (decision.startsWith("managed")) return "proxied";
  if (decision.startsWith("denied")) return "denied";
  return "direct";
}
const serviceOf = (d: string) => (d.startsWith("managed:") ? d.slice("managed:".length) : "");

export default function Logs() {
  const nodes = useAsync<{ items: Node[] }>(() => api("/nodes"), []);
  const [nodeId, setNodeId] = useState("");
  const [entries, setEntries] = useState<Entry[]>([]);
  const [available, setAvailable] = useState(true);
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState<"all" | Kind>("all");
  const [q, setQ] = useState("");
  const [err, setErr] = useState("");
  const seqRef = useRef(0);

  // По умолчанию — первый ingress (там и «проксируется», и «напрямую»).
  useEffect(() => {
    if (!nodeId && nodes.data?.items.length) {
      const ing = nodes.data.items.find((n) => n.role === "ingress") ?? nodes.data.items[0];
      setNodeId(ing.id);
    }
  }, [nodes.data, nodeId]);

  // Смена ноды — начинаем поток заново.
  useEffect(() => {
    seqRef.current = 0;
    setEntries([]);
    setAvailable(true);
    setErr("");
  }, [nodeId]);

  useEffect(() => {
    if (!nodeId || paused) return;
    let alive = true;
    const tick = async () => {
      try {
        const r = await api<LogResp>(`/nodes/${nodeId}/dns-log?after=${seqRef.current}`);
        if (!alive) return;
        setErr("");
        if (r.available === false) { setAvailable(false); return; }
        setAvailable(true);
        if (r.seq) seqRef.current = r.seq;
        if (r.entries?.length) {
          const batch = [...r.entries].reverse(); // newest-first
          setEntries((prev) => [...batch, ...prev].slice(0, CAP));
        }
      } catch (e) {
        if (alive) setErr(e instanceof Error ? e.message : String(e));
      }
    };
    tick();
    const id = setInterval(tick, 1500);
    return () => { alive = false; clearInterval(id); };
  }, [nodeId, paused]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return entries.filter((e) =>
      (filter === "all" || kindOf(e.decision) === filter) &&
      (needle === "" || e.name.toLowerCase().includes(needle)));
  }, [entries, filter, q]);

  // Сводка по накопленному окну.
  const stats = useMemo(() => {
    const n = entries.length;
    let proxied = 0, denied = 0, msSum = 0;
    const now = Date.now();
    let lastMin = 0;
    for (const e of entries) {
      const k = kindOf(e.decision);
      if (k === "proxied") proxied++;
      else if (k === "denied") denied++;
      msSum += e.ms;
      if (now - e.ts < 60000) lastMin++;
    }
    return {
      rate: lastMin,
      proxiedPct: n ? Math.round((proxied / n) * 100) : 0,
      avgMs: n ? Math.round(msSum / n) : 0,
      denied,
    };
  }, [entries]);

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">наблюдение</div><h1>Логи запросов</h1></div>
        <div className="spacer" />
        {nodes.data && (
          <select className="select" style={{ width: "auto" }} value={nodeId}
            onChange={(e) => setNodeId(e.target.value)} aria-label="Нода">
            {nodes.data.items.map((n) => (
              <option key={n.id} value={n.id}>{n.name} · {n.role}</option>
            ))}
          </select>
        )}
        <button className={`btn ${paused ? "primary" : ""}`} onClick={() => setPaused((p) => !p)}>
          {paused ? <><IconPlay />Возобновить</> : <><span className="live-dot" />Пауза</>}
        </button>
        <button className="btn ghost" onClick={() => { setEntries([]); }} title="Очистить экран">
          <IconRefresh />Очистить
        </button>
      </div>

      <Notice kind="info" title="Что здесь видно">
        Живой поток DNS-запросов с ноды. <b>Проксируется</b> — домен управляемого сервиса, ушёл через
        точку выхода; <b>Напрямую</b> — обычный домен, резолвился без прокси. Колонка <b>задержка</b> — время
        обработки DNS: маленькое значение при тормозящей странице значит, что дело не в DNS, а в канале.
      </Notice>

      {nodes.loading ? <Spinner />
        : nodes.error ? <ErrorState message={nodes.error} onRetry={nodes.reload} />
        : !available ? (
          <Card>
            <div className="empty">
              <h3>На этой ноде нет журнала запросов</h3>
              <p className="muted small">Это точка выхода — она только проксирует трафик наружу и не резолвит имена.
                Выберите ingress-ноду, чтобы видеть запросы.</p>
            </div>
          </Card>
        ) : (
          <>
            <div className="grid g4" style={{ marginBottom: 4 }}>
              <Tile label="в минуту" value={stats.rate} />
              <Tile label="проксируется" value={`${stats.proxiedPct}%`} tone="accent" />
              <Tile label="средняя задержка" value={`${stats.avgMs} мс`} tone={stats.avgMs > 300 ? "warn" : undefined} />
              <Tile label="отклонено" value={stats.denied} tone={stats.denied ? "bad" : undefined} />
            </div>

            <div className="row" style={{ gap: 8 }}>
              <div className="seg">
                {([["all", "Все"], ["proxied", "Проксируется"], ["direct", "Напрямую"], ["denied", "Отклонено"]] as const)
                  .map(([v, l]) => (
                    <button key={v} type="button" className={`seg-btn${filter === v ? " sel" : ""}`}
                      onClick={() => setFilter(v)}>{l}</button>
                  ))}
              </div>
              <div className="spacer" />
              <input className="input" style={{ maxWidth: 260 }} value={q} placeholder="Поиск по домену…"
                onChange={(e) => setQ(e.target.value)} />
            </div>

            {err && <Notice kind="warn" title="Нода временно недоступна">{err} — поток возобновится сам.</Notice>}

            <Card tight>
              <div className="table-wrap">
                <table className="table logs">
                  <thead>
                    <tr><th style={{ width: 92 }}>Время</th><th style={{ width: 128 }}>Источник</th><th>Домен</th><th style={{ width: 56 }}>Тип</th>
                      <th style={{ width: 150 }}>Решение</th><th style={{ width: 130 }}>Задержка</th><th style={{ width: 90 }}>Ответ</th></tr>
                  </thead>
                  <tbody>
                    {shown.length === 0 ? (
                      <tr><td colSpan={7}>
                        <div className="empty" style={{ padding: 32 }}>
                          <h3>{paused ? "Поток на паузе" : "Пока тихо"}</h3>
                          <p className="muted small">
                            {paused ? "Нажмите «Возобновить»." : "Откройте что-нибудь на устройстве — запросы появятся здесь."}
                          </p>
                        </div>
                      </td></tr>
                    ) : shown.map((e) => <Row key={e.seq} e={e} />)}
                  </tbody>
                </table>
              </div>
            </Card>
          </>
        )}
    </>
  );
}

function Row({ e }: { e: Entry }) {
  const k = kindOf(e.decision);
  const svc = serviceOf(e.decision);
  const bad = e.rcode !== "NOERROR" && e.rcode !== "nil";
  const barPct = Math.min(100, Math.round((e.ms / 200) * 100));
  return (
    <tr>
      <td className="tiny dim num">{new Date(e.ts).toLocaleTimeString("ru-RU")}</td>
      <td className="tiny mono" title={e.client || undefined}>{e.client || "—"}</td>
      <td className="mono small" style={{ wordBreak: "break-all" }}>
        {e.name}
        <span className="proto-tag">{e.proto}</span>
      </td>
      <td className="tiny mono dim">{e.type}</td>
      <td>
        {k === "proxied" ? <span className="badge ok">через выход{svc ? ` · ${svc}` : ""}</span>
          : k === "denied" ? <span className="badge bad">{e.decision.replace("denied:", "отклонён · ")}</span>
          : <span className="badge plain">напрямую</span>}
      </td>
      <td>
        <div className="lat">
          <span className={`lat-val num${e.ms > 300 ? " slow" : ""}`}>{e.ms} мс</span>
          <span className="lat-bar"><span className={`lat-fill${e.ms > 300 ? " slow" : ""}`} style={{ width: `${barPct}%` }} /></span>
        </div>
      </td>
      <td>{bad ? <span className="badge warn">{e.rcode}</span> : <span className="tiny dim">{e.rcode}</span>}</td>
    </tr>
  );
}

function Tile({ label, value, tone }: { label: string; value: ReactNode; tone?: string }) {
  return (
    <div className="card tile">
      <div className="tile-label">{label}</div>
      <div className={`tile-value ${tone ?? ""}`}>{value}</div>
    </div>
  );
}
