import { useEffect, useState } from "react";
import { api, idemKey, shortHash } from "../api";
import { Card, Confirm, ErrorState, Field, Modal, Notice, Segmented, Spinner, errText, useAsync, useToast } from "../ui";
import { IconPlus, IconRefresh, IconTrash } from "../icons";
import { countryName, flagOf } from "../countries";

type Service = {
  id: string; name: string; slug: string; enabled: boolean; priority: number;
  dns_ttl: number; udp_mode: string; allowed_ports: number[];
  rule_set_id: string | null; rule_set_name: string | null;
  nodes: NodeRow[];
  rule_count: number | null; rule_set_hash: string | null;
  probe: Record<string, unknown>; probe_in_set: boolean; version: number;
  domains: string[];
};
type Named = { id: string; name: string };
type NodeRow = { id: string; name: string; role: string; country: string; status: string };
type SearchHit = { id: string; name: string; slug: string; matched: string[]; total: number };

// Страны выходных нод в выборе. Больше одной — сервис нельзя сохранить: отказ
// основной ноды увёл бы трафик в другую страну под тем же аккаунтом.
function egressCountries(nodes: NodeRow[], ids: string[]): string[] {
  const seen = new Set<string>();
  for (const id of ids) {
    const n = nodes.find((x) => x.id === id);
    if (n?.role === "egress") seen.add(n.country || "");
  }
  return [...seen];
}

// Выбор нод сервиса. Порядок отметки — это порядок отказа: работает первая
// живая, остальные ждут своей очереди.
function NodePicker({ nodes, value, onChange }: {
  nodes: NodeRow[]; value: string[]; onChange: (ids: string[]) => void;
}) {
  const toggle = (id: string) =>
    onChange(value.includes(id) ? value.filter((v) => v !== id) : [...value, id]);
  const rankIn = (role: string, id: string) =>
    value.filter((v) => nodes.find((n) => n.id === v)?.role === role).indexOf(id);

  const section = (role: "ingress" | "egress", title: string, hint: string) => {
    const rows = nodes.filter((n) => n.role === role);
    return (
      <Field label={title} hint={hint}>
        {rows.length === 0
          ? <div className="tiny dim">нод этой роли пока нет</div>
          : rows.map((n) => {
              const on = value.includes(n.id);
              const r = on ? rankIn(role, n.id) : -1;
              return (
                <label key={n.id} className={`pick${on ? " on" : ""}`}>
                  <input type="checkbox" checked={on} onChange={() => toggle(n.id)} />
                  <span className="flag">{flagOf(n.country)}</span>
                  <span className="pick-name">{n.name}</span>
                  <span className="tiny dim">{countryName(n.country) || "страна не указана"}</span>
                  <span className="spacer" />
                  {n.status !== "healthy" && <span className="tiny dim">{n.status}</span>}
                  {on && <span className="badge">{r === 0 ? "основная" : `резерв ${r}`}</span>}
                </label>
              );
            })}
      </Field>
    );
  };

  const countries = egressCountries(nodes, value);
  const ingressCount = nodes.filter((n) => n.role === "ingress").length;
  return (
    <>
      <Notice kind="info" title="Вход выбирать не нужно">
        Устройства стучатся во {ingressCount === 1 ? "вход" : `все входы (${ingressCount})`}, и каждый вход
        обслуживает все сервисы — это дверь, а не маршрут. Страну и путь решает нода выхода.
      </Notice>
      {section("egress", "Ноды выхода", "Через кого сервис выходит к сайту. Первая отмеченная — основная, остальные подхватят при её отказе.")}
      {countries.length > 1 && (
        <Notice kind="warn" title="Ноды выхода из разных стран">
          Выбраны {countries.map((c) => countryName(c) || "без страны").join(" и ")}. При отказе основной ноды
          трафик уйдёт в другую страну, и сайт увидит смену географии под тем же аккаунтом — так теряют аккаунты.
          Оставьте ноды одной страны.
        </Notice>
      )}
    </>
  );
}
type CatalogItem = { slug: string; name: string; preset: string; probe_host: string; domains: number };

export default function Services() {
  const list = useAsync<{ items: Service[] }>(() => api("/services"), []);
  // Запрос уходит не на каждую букву: пока набирают «google.com», это одиннадцать
  // запросов вместо одного.
  const [query, setQuery] = useState("");
  const [typed, setTyped] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setQuery(typed.trim()), 250);
    return () => clearTimeout(t);
  }, [typed]);
  const found = useAsync<{ items: SearchHit[] }>(
    () => (query.length < 2
      ? Promise.resolve({ items: [] })
      : api(`/services/search?q=${encodeURIComponent(query)}`)),
    [query]);
  const rules = useAsync<{ items: Named[] }>(() => api("/rule-sets"), []);
  const nodesReq = useAsync<{ items: NodeRow[] }>(() => api("/nodes"), []);
  const [editing, setEditing] = useState<Service | null>(null);
  const [wizard, setWizard] = useState(false);
  const [removing, setRemoving] = useState<Service | null>(null);
  const toast = useToast();

  const nodes = (nodesReq.data?.items ?? []).filter((n) => n.status !== "disabled");
  const hits = found.data?.items ?? [];
  const hitOf = (id: string) => (query.length >= 2 ? hits.find((h) => h.id === id) : undefined);
  const all = list.data?.items ?? [];
  const shown = query.length >= 2 ? all.filter((s) => hits.some((h) => h.id === s.id)) : all;

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Сервисы</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setWizard(true)}><IconPlus />Новый сервис</button>
      </div>

      <Notice kind="info" title="Сервис — это то, что вы включаете">
        Соберите сервис в мастере: домены (списком, из каталога, с GitHub или по ссылке) и ноды, через которые
        он ходит. Ноды выбираются прямо здесь, групп больше нет: список нод выхода — это порядок отказа, и все
        они должны быть из одной страны, иначе сайт увидит переезд аккаунта.
      </Notice>

      <Card tight>
        <div className="searchbar">
          <input className="input" value={typed} placeholder="Поиск по домену: netflix.com, www.google.com, goog…"
            aria-label="Поиск сервиса по домену"
            onChange={(e) => setTyped(e.target.value)} />
          {typed && <button className="btn sm ghost" onClick={() => setTyped("")}>Сбросить</button>}
        </div>
        {query.length >= 2 && !found.loading && (
          <div className="searchnote tiny dim">
            {hits.length === 0
              ? `Ни один сервис не обслуживает «${query}» — этот домен пойдёт напрямую.`
              : `Нашлось сервисов: ${hits.length}. Домен обслуживает тот, чьё совпадение длиннее.`}
          </div>
        )}
      </Card>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading ? <Spinner />
        : shown.length === 0 && query.length >= 2 ? null
        : list.data!.items.length === 0 ? (
          <Card>
            <div className="empty">
              <h3>Своих сервисов пока нет</h3>
              <p className="muted small">
                Соберите сервис в мастере — он проведёт по шагам: название, домены, маршрут, настройки.
              </p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setWizard(true)}>
                <IconPlus />Новый сервис
              </button>
            </div>
          </Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr><th>Сервис</th><th>Домены</th><th>Маршрут</th><th>TTL</th>
                    <th>Порты</th><th>Приоритет</th><th>Состояние</th><th /></tr>
                </thead>
                <tbody>
                  {shown.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{s.name}</div>
                        <div className="tiny dim mono">{s.slug}</div>
                        {hitOf(s.id) && (
                          <div className="tiny mono" style={{ color: "var(--accent)", marginTop: 2 }}>
                            {hitOf(s.id)!.matched.slice(0, 3).join(", ")}
                            {hitOf(s.id)!.total > 3 ? ` и ещё ${hitOf(s.id)!.total - 3}` : ""}
                          </div>
                        )}
                        {!s.probe_in_set && (
                          <div className="tiny" style={{ color: "var(--warn)", marginTop: 2 }}
                            title="Проба всегда будет падать: этот домен не входит в набор">
                            домен пробы не в наборе
                          </div>
                        )}
                      </td>
                      <td>
                        <div className="small">{s.rule_set_name ?? <span className="dim">не выбран</span>}</div>
                        <div className="tiny dim mono">
                          {s.rule_count ?? 0} доменов · {shortHash(s.rule_set_hash)}
                        </div>
                      </td>
                      <td className="tiny">
                        <RouteCell nodes={s.nodes ?? []} />
                      </td>
                      <td className="num small">{s.dns_ttl} с</td>
                      <td className="num tiny">{s.allowed_ports?.join(", ")}</td>
                      <td className="num small">{s.priority}</td>
                      <td>{s.enabled ? <span className="badge ok">включён</span> : <span className="badge">выключен</span>}</td>
                      <td className="actions">
                        <button className="btn sm ghost" onClick={() => setEditing(s)}>Изменить</button>
                        <button className="btn sm ghost danger" aria-label={`Удалить ${s.name}`}
                          onClick={() => setRemoving(s)}><IconTrash /></button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

      {wizard && (
        <ServiceWizard
          nodes={nodes}
          onClose={() => setWizard(false)}
          onSaved={() => { setWizard(false); list.reload(); rules.reload(); }}
        />
      )}

      {editing && (
        <ServiceForm
          service={editing}
          nodes={nodes}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); list.reload(); }}
        />
      )}

      {removing && (
        <Confirm title={`Удалить сервис ${removing.name}?`} danger confirmLabel="Удалить"
          body="Домены сервиса перестанут проходить через инфраструктуру после следующей сборки и применения конфигурации."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/services/${removing.id}`, { method: "DELETE" });
              toast({ kind: "ok", title: "Сервис удалён", body: "Соберите новую конфигурацию, чтобы применить изменение." });
              setRemoving(null); list.reload();
            } catch (e) { toast({ kind: "bad", title: "Удаление отклонено", body: errText(e) }); }
          }} />
      )}
    </>
  );
}

// Маршрут в списке сервисов: флаги и имена нод, а не имя группы — из имени
// группы нельзя было понять, из какой страны увидят пользователя.
function RouteCell({ nodes }: { nodes: NodeRow[] }) {
  const part = (role: string) => nodes.filter((n) => n.role === role);
  const line = (rows: NodeRow[]) =>
    rows.length === 0 ? <span className="dim">—</span>
      : rows.map((n) => <span key={n.id} className="mono" title={countryName(n.country)}>{flagOf(n.country)} {n.name}</span>)
          .reduce((a, b) => <>{a}<span className="dim">, </span>{b}</>);
  // Стрелка — отдельная строка по центру между входом и выходом. Приклеенная
  // к имени ноды, она читалась как часть имени, а не как переход между ними.
  return (
    <div className="route-cell">
      <div className="route-row dim">любой вход</div>
      <div className="route-arrow dim">↓</div>
      <div className="route-row">{line(part("egress"))}</div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// New service: a 4-step wizard that creates the domain list and the service
// together via POST /services/wizard.
// ---------------------------------------------------------------------------

type DomainMode = "manual" | "catalog" | "github" | "url";

function ServiceWizard({ nodes, onClose, onSaved }: {
  nodes: NodeRow[];
  onClose: () => void; onSaved: () => void;
}) {
  const catalog = useAsync<{ items: CatalogItem[] }>(() => api("/services/catalog"), []);
  const [step, setStep] = useState(1);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const [name, setName] = useState("");
  const [mode, setMode] = useState<DomainMode>("manual");
  const [domains, setDomains] = useState("");
  const [presetKey, setPresetKey] = useState("");
  const [repo, setRepo] = useState(""); const [path, setPath] = useState(""); const [ref, setRef] = useState("main");
  const [url, setUrl] = useState("");

  // Picking a catalogue preset in step 2 also fills the name and probe host if
  // they're still empty — the convenience the removed step-1 chips used to give.
  const pickPreset = (key: string) => {
    setPresetKey(key);
    const c = catalog.data?.items.find((i) => i.preset === key);
    if (c) {
      setName((n) => n || c.name);
      setProbe((p) => p || c.probe_host);
    }
  };

  // По умолчанию отмечаем всё, что есть по одной ноде на роль: типичный случай,
  // и мастер не заставляет кликать очевидное.
  const only = (role: string) => nodes.filter((n) => n.role === role);
  const [nodeIds, setNodeIds] = useState<string[]>(
    only("egress").length === 1 ? [only("egress")[0].id] : []);

  const [ttl, setTtl] = useState(60);
  const [ports, setPorts] = useState("443");
  const [udp, setUdp] = useState("disabled_fallback");
  const [probe, setProbe] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);

  const domainsChosen =
    (mode === "manual" && domains.trim() !== "") ||
    (mode === "catalog" && presetKey !== "") ||
    (mode === "github" && repo.trim() !== "" && path.trim() !== "") ||
    (mode === "url" && url.trim() !== "");

  const hasEgress = nodeIds.some((id) => nodes.find((n) => n.id === id)?.role === "egress");
  const mixedCountries = egressCountries(nodes, nodeIds).length > 1;
  const canNext = step === 1 ? name.trim() !== ""
    : step === 2 ? domainsChosen
    : step === 3 ? hasEgress && !mixedCountries
    : true;

  const submit = async () => {
    setBusy(true); setError("");
    const body: Record<string, unknown> = {
      name,
      node_ids: nodeIds,
      dns_ttl: ttl,
      allowed_ports: ports.split(",").map((p) => Number(p.trim())).filter((n) => n > 0 && n < 65536),
      udp_mode: udp,
      probe_host: probe.trim(),
    };
    if (mode === "manual") body.domains = domains.split("\n");
    if (mode === "catalog") body.preset = presetKey;
    if (mode === "github") { body.repo = repo.trim(); body.path = path.trim(); body.ref = ref.trim() || "main"; }
    if (mode === "url") body.url = url.trim();
    try {
      await api("/services/wizard", { method: "POST", body, headers: { "Idempotency-Key": idemKey() } });
      onSaved();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  const missingNodes = only("ingress").length === 0 || only("egress").length === 0;

  return (
    <Modal title="Новый сервис" onClose={onClose} wide footer={
      <>
        {step > 1 && <button className="btn" onClick={() => setStep(step - 1)}>Назад</button>}
        <div className="spacer" />
        <button className="btn" onClick={onClose}>Отмена</button>
        {step < 4
          ? <button className="btn primary" disabled={!canNext} onClick={() => setStep(step + 1)}>Далее</button>
          : <button className="btn primary" disabled={busy || !canNext || missingNodes} onClick={submit}>
              {busy ? <span className="spin" /> : null}Создать сервис
            </button>}
      </>
    }>
      <Steps step={step} labels={["Название", "Домены", "Маршрут", "Настройки"]} />

      {step === 1 && (
        <Field label="Название сервиса" hint="Например: Gemini. Идентификатор для метрик создастся автоматически. Готовые наборы доменов — на шаге «Домены».">
          <input className="input" autoFocus value={name} placeholder="Gemini"
            onChange={(e) => setName(e.target.value)} />
        </Field>
      )}

      {step === 2 && (
        <>
          <Field label="Откуда взять домены">
            <div className="seg">
              {([["manual", "Списком"], ["catalog", "Из каталога"], ["github", "GitHub"], ["url", "Ссылка"]] as const)
                .map(([v, l]) => (
                  <button key={v} type="button" className={`seg-btn${mode === v ? " sel" : ""}`}
                    onClick={() => setMode(v)}>{l}</button>
                ))}
            </div>
          </Field>
          {mode === "manual" && (
            <Field label="Домены" hint="По одному в строке. Домен покрывает и все поддомены (openai.com → и api.openai.com). Только точный хост — full:host. Можно загрузить файл.">
              <textarea className="textarea mono" rows={8} value={domains} placeholder={"gemini.google.com\naistudio.google.com"}
                onChange={(e) => setDomains(e.target.value)} />
              <label className="btn sm" style={{ marginTop: 8, display: "inline-flex", cursor: "pointer" }}>
                Загрузить файл
                <input type="file" accept=".txt,.list,text/plain" style={{ display: "none" }}
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) { const r = new FileReader(); r.onload = () => setDomains((d) => (d.trim() ? d.replace(/\s*$/, "") + "\n" : "") + String(r.result ?? "").trim()); r.readAsText(f); }
                    e.target.value = "";
                  }} />
              </label>
            </Field>
          )}
          {mode === "catalog" && (
            <Field label="Встроенный список">
              <select className="select" value={presetKey} onChange={(e) => pickPreset(e.target.value)}>
                <option value="">— выберите —</option>
                {(catalog.data?.items ?? []).map((c) =>
                  <option key={c.preset} value={c.preset}>{c.name} ({c.domains})</option>)}
              </select>
            </Field>
          )}
          {mode === "github" && (
            <div className="grid g3">
              <Field label="Репозиторий" hint="owner/repo"><input className="input mono" value={repo}
                placeholder="v2fly/domain-list-community" onChange={(e) => setRepo(e.target.value)} /></Field>
              <Field label="Путь к файлу"><input className="input mono" value={path}
                placeholder="data/openai" onChange={(e) => setPath(e.target.value)} /></Field>
              <Field label="Ветка"><input className="input mono" value={ref}
                placeholder="main" onChange={(e) => setRef(e.target.value)} /></Field>
            </div>
          )}
          {mode === "url" && (
            <Field label="HTTPS-ссылка на список" hint="Обычный текстовый список доменов, по одному в строке.">
              <input className="input mono" value={url} placeholder="https://example.com/list.txt"
                onChange={(e) => setUrl(e.target.value)} />
            </Field>
          )}
        </>
      )}

      {step === 3 && (
        <>
          {missingNodes && (
            <Notice kind="warn" title="Сначала заведите ноды">
              Сервису нужна хотя бы одна нода выхода, а флоту — хотя бы одна нода входа.
              Заведите их на странице «Ноды» и вернитесь.
            </Notice>
          )}
          <NodePicker nodes={nodes} value={nodeIds} onChange={setNodeIds} />
        </>
      )}

      {step === 4 && (
        <>
          <Field label="Домен для проверки"
            hint="Панель подключается к точке входа с этим именем и проверяет сертификат. Не адрес, требующий входа в аккаунт.">
            <input className="input mono" value={probe} placeholder="gemini.google.com"
              onChange={(e) => setProbe(e.target.value)} />
          </Field>
          <button type="button" className="linklike" onClick={() => setShowAdvanced(!showAdvanced)}>
            {showAdvanced ? "Скрыть дополнительные настройки" : "Дополнительные настройки"}
          </button>
          {showAdvanced && (
            <div className="grid g3" style={{ marginTop: 12 }}>
              <Field label="TTL ответа DNS" hint="30–300 с.">
                <input className="input num" type="number" min={30} max={300} value={ttl}
                  onChange={(e) => setTtl(Number(e.target.value))} />
              </Field>
              <Field label="Разрешённые порты" hint="Через запятую. Обычно 443.">
                <input className="input mono" value={ports} onChange={(e) => setPorts(e.target.value)} />
              </Field>
              <Field label="Режим UDP / QUIC">
                <select className="select" value={udp} onChange={(e) => setUdp(e.target.value)}>
                  <option value="disabled_fallback">Откат на TCP (рекоменд.)</option>
                  <option value="proxy">Проксировать UDP</option>
                  <option value="separate_ip">Отдельный IP</option>
                </select>
              </Field>
            </div>
          )}
        </>
      )}

      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

function Steps({ step, labels }: { step: number; labels: string[] }) {
  return (
    <div className="steps">
      {labels.map((l, i) => {
        const n = i + 1;
        const state = n < step ? "done" : n === step ? "cur" : "";
        return (
          <div key={l} className={`step ${state}`}>
            <span className="step-num">{n}</span>
            <span className="step-label">{l}</span>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Edit existing service (unchanged behaviour): direct field editing.
// ---------------------------------------------------------------------------

function ServiceForm({ service, nodes, onClose, onSaved }: {
  service: Service; nodes: NodeRow[];
  onClose: () => void; onSaved: () => void;
}) {
  const [name, setName] = useState(service.name);
  const [domains, setDomains] = useState((service.domains ?? []).join("\n"));
  const [nodeIds, setNodeIds] = useState<string[]>((service.nodes ?? []).map((n) => n.id));
  const [ttl, setTtl] = useState(service.dns_ttl);
  const [priority, setPriority] = useState(service.priority);
  const [ports, setPorts] = useState((service.allowed_ports ?? [443]).join(", "));
  const [udp, setUdp] = useState(service.udp_mode);
  const [probeHost, setProbeHost] = useState((service.probe as any)?.hostname ?? "");
  const [enabled, setEnabled] = useState(service.enabled);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"domains" | "route" | "params">("domains");

  const addFile = (f: File) => {
    const r = new FileReader();
    r.onload = () => setDomains((d) => (d.trim() ? d.replace(/\s*$/, "") + "\n" : "") + String(r.result ?? "").trim());
    r.readAsText(f);
  };

  const save = async () => {
    setBusy(true); setError("");
    const body: Record<string, unknown> = {
      name, description: "", enabled,
      node_ids: nodeIds,
      allowed_ports: ports.split(",").map((p) => Number(p.trim())).filter((n) => n > 0 && n < 65536),
      udp_mode: udp, dns_ttl: ttl, priority,
      probe: probeHost ? { hostname: probeHost.trim(), port: 443 } : {},
    };
    try {
      await api(`/services/${service.id}`, { method: "PATCH", headers: { "If-Match": String(service.version) }, body });
      // Domains live on the service; save them (and rebuild) as a second step.
      await api(`/services/${service.id}/domains`, { method: "PATCH", body: { domains: domains.split("\n") } });
      onSaved();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={`Сервис ${service.name}`} onClose={onClose} wide footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" onClick={save}
          disabled={busy || egressCountries(nodes, nodeIds).length > 1}>
          {busy ? <span className="spin" /> : null}Сохранить
        </button>
      </>
    }>
      <div className="grid g2">
        <Field label="Название"><input className="input" value={name} autoFocus
          onChange={(e) => setName(e.target.value)} placeholder="Gemini" /></Field>
        <Field label="Идентификатор" hint="Изменить нельзя: он используется в метриках.">
          <input className="input mono" value={service.slug} disabled />
        </Field>
      </div>

      <div className="tabs" role="tablist">
        {([["domains", "Домены"], ["route", "Маршрут"], ["params", "Параметры"]] as const).map(([v, l]) => (
          <button key={v} type="button" role="tab" aria-selected={tab === v}
            className={`tab${tab === v ? " active" : ""}`} onClick={() => setTab(v)}>{l}</button>
        ))}
      </div>

      {tab === "domains" && (
        <>
          <Field label="Домены" hint="По одному в строке. Домен покрывает и все поддомены (openai.com → и api.openai.com). Только точный хост — full:host. Правки применятся после сохранения.">
            <textarea className="textarea mono" rows={8} value={domains}
              placeholder={"gemini.google.com\naistudio.google.com"}
              onChange={(e) => setDomains(e.target.value)} />
            <label className="btn sm" style={{ marginTop: 8, display: "inline-flex", cursor: "pointer" }}>
              Загрузить файл
              <input type="file" accept=".txt,.list,text/plain" style={{ display: "none" }}
                onChange={(e) => { const f = e.target.files?.[0]; if (f) addFile(f); e.target.value = ""; }} />
            </label>
          </Field>
          <SourcesSection serviceId={service.id} />
        </>
      )}

      {tab === "route" && (
        <>
          <NodePicker nodes={nodes} value={nodeIds} onChange={setNodeIds} />
          <Field label="Домен для проверки"
            hint="Панель подключается к точке входа с этим именем и проверяет, что сертификат принадлежит настоящему сервису. Не используйте адреса, требующие входа в аккаунт.">
            <input className="input mono" value={probeHost} onChange={(e) => setProbeHost(e.target.value)}
              placeholder="gemini.google.com" />
          </Field>
        </>
      )}

      {tab === "params" && (
        <>
          <div className="grid g3">
            <Field label="TTL ответа DNS" hint="30–300 с. Меньше — быстрее переключение, больше запросов.">
              <input className="input num" type="number" min={30} max={300} value={ttl}
                onChange={(e) => setTtl(Number(e.target.value))} />
            </Field>
            <Field label="Приоритет" hint="Больше — важнее при пересечении доменов между сервисами.">
              <input className="input num" type="number" value={priority}
                onChange={(e) => setPriority(Number(e.target.value))} />
            </Field>
            <Field label="Разрешённые порты" hint="Через запятую. Обычно достаточно 443.">
              <input className="input mono" value={ports} onChange={(e) => setPorts(e.target.value)} />
            </Field>
          </div>
          <Field label="Режим UDP / QUIC">
            <Segmented value={udp} onChange={setUdp} wide options={[
              { value: "disabled_fallback", label: "Откат на TCP", hint: "UDP/443 отклоняется, браузер уходит на TCP. Рекомендуется по умолчанию." },
              { value: "proxy", label: "Проксировать UDP", hint: "Включайте только после сквозной проверки конкретного сервиса." },
              { value: "separate_ip", label: "Отдельный IP", hint: "Отдельный адрес под QUIC." },
            ]} />
          </Field>
          <label className="check">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            Сервис включён
          </label>
        </>
      )}

      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Auto-update sources, managed inside the service window. Each source is a
// GitHub/HTTPS list that feeds the service's domains; adding, removing, or
// refreshing rebuilds and activates at once (config still needs a deploy).
// ---------------------------------------------------------------------------

type Source = { id: string; name: string; type: string; url: string; repo: string; ref: string; path: string };
type SourceFetch = { source_id: string; status: string; entries: number; error: string };

function SourcesSection({ serviceId }: { serviceId: string }) {
  const data = useAsync<{ sources: Source[]; fetches: SourceFetch[] }>(() => api(`/services/${serviceId}/sources`), [serviceId]);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Source | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const sources = data.data?.sources ?? [];

  const refresh = async () => {
    setBusy(true);
    try {
      const r = await api<{ build: { unchanged: boolean; added: number; removed: number } }>(
        `/services/${serviceId}/refresh`, { method: "POST", headers: { "Idempotency-Key": idemKey() } });
      toast({ kind: "ok", title: r.build.unchanged ? "Изменений нет" : "Домены обновлены",
        body: r.build.unchanged ? undefined : `Добавлено ${r.build.added}, удалено ${r.build.removed}. Соберите конфигурацию, чтобы применить.` });
      data.reload();
    } catch (e) { toast({ kind: "bad", title: "Обновление не удалось", body: errText(e) }); }
    finally { setBusy(false); }
  };

  return (
    <Field label="Автообновление доменов"
      hint="Необязательно. Подтяните список с GitHub или по ссылке — он будет обновляться поверх доменов, вписанных вручную.">
      {sources.length > 0 && (
        <div className="table-wrap" style={{ marginBottom: 8 }}>
          <table className="table">
            <tbody>
              {sources.map((s) => {
                const last = data.data!.fetches.find((f) => f.source_id === s.id);
                return (
                  <tr key={s.id}>
                    <td>
                      <div className="small" style={{ fontWeight: 550 }}>{s.name || s.repo || s.url || s.path}</div>
                      <div className="tiny dim mono" style={{ wordBreak: "break-all" }}>
                        {s.repo ? `${s.repo}@${s.ref || "main"}:${s.path}` : s.url || s.path}
                      </div>
                      {last && (
                        <div className="tiny" style={{ marginTop: 2, color: last.status === "ok" ? "var(--text-3)" : "var(--danger)" }}>
                          {last.status === "ok" ? `${last.entries} доменов` : last.error}
                        </div>
                      )}
                    </td>
                    <td className="actions">
                      <button type="button" className="btn sm ghost danger" aria-label="Удалить источник"
                        onClick={() => setRemoving(s)}><IconTrash /></button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <div className="btn-row">
        <button type="button" className="btn sm" onClick={() => setAdding(true)}><IconPlus />Добавить источник</button>
        {sources.length > 0 && (
          <button type="button" className="btn sm" disabled={busy} onClick={refresh}>
            {busy ? <span className="spin" /> : <IconRefresh />}Обновить сейчас
          </button>
        )}
      </div>

      {adding && (
        <AddServiceSource serviceId={serviceId} onClose={() => setAdding(false)}
          onAdded={() => { setAdding(false); data.reload(); }} />
      )}
      {removing && (
        <Confirm title="Удалить источник?" danger confirmLabel="Удалить"
          body="Домены этого источника исчезнут из сервиса. Соберите конфигурацию, чтобы применить."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await api(`/services/${serviceId}/sources/${removing.id}`, { method: "DELETE" });
            setRemoving(null); data.reload();
          }} />
      )}
    </Field>
  );
}

function AddServiceSource({ serviceId, onClose, onAdded }: { serviceId: string; onClose: () => void; onAdded: () => void }) {
  const [type, setType] = useState("github_repo");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("main");
  const [path, setPath] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <Modal title="Источник доменов" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api(`/services/${serviceId}/sources`, { method: "POST", body: { name, type, url, repo, ref, path } });
            onAdded();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Добавить</button>
      </>
    }>
      <Field label="Откуда">
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          <option value="github_repo">GitHub: репозиторий + путь</option>
          <option value="github_raw">GitHub: прямая ссылка</option>
          <option value="https">Произвольный HTTPS-адрес</option>
        </select>
      </Field>
      <Field label="Название" hint="Как источник будет подписан.">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="OpenAI (v2fly)" />
      </Field>
      {type === "github_repo" ? (
        <>
          <div className="grid g2">
            <Field label="Репозиторий"><input className="input mono" value={repo}
              onChange={(e) => setRepo(e.target.value)} placeholder="v2fly/domain-list-community" /></Field>
            <Field label="Ветка или тег" hint="Для стабильности лучше тег или commit — ветка меняется без спроса.">
              <input className="input mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" />
            </Field>
          </div>
          <Field label="Путь к файлу"><input className="input mono" value={path}
            onChange={(e) => setPath(e.target.value)} placeholder="data/openai" /></Field>
        </>
      ) : (
        <Field label="Адрес" hint="Только HTTPS. Обычный текстовый список доменов, по одному в строке.">
          <input className="input mono" value={url} onChange={(e) => setUrl(e.target.value)}
            placeholder="https://raw.githubusercontent.com/owner/repo/main/list.txt" />
        </Field>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}
