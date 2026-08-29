import { useState } from "react";
import { api, shortHash } from "../api";
import { Card, Confirm, ErrorState, Field, Modal, Notice, Segmented, Spinner, errText, useAsync, useToast } from "../ui";
import { IconPlus, IconRefresh, IconTrash } from "../icons";

type Service = {
  id: string; name: string; slug: string; enabled: boolean; priority: number;
  dns_ttl: number; udp_mode: string; allowed_ports: number[];
  rule_set_id: string | null; ingress_group_id: string | null; egress_group_id: string | null;
  rule_set_name: string | null; ingress_group_name: string | null; egress_group_name: string | null;
  rule_count: number | null; rule_set_hash: string | null;
  probe: Record<string, unknown>; probe_in_set: boolean; version: number;
  domains: string[];
};
type Named = { id: string; name: string };
type CatalogItem = { slug: string; name: string; preset: string; probe_host: string; domains: number };

export default function Services() {
  const list = useAsync<{ items: Service[] }>(() => api("/services"), []);
  const rules = useAsync<{ items: Named[] }>(() => api("/rule-sets"), []);
  const ing = useAsync<{ items: { group: Named }[] }>(() => api("/ingress-groups"), []);
  const egr = useAsync<{ items: { group: Named }[] }>(() => api("/egress-groups"), []);
  const [editing, setEditing] = useState<Service | null>(null);
  const [wizard, setWizard] = useState(false);
  const [removing, setRemoving] = useState<Service | null>(null);
  const toast = useToast();

  const ingress = (ing.data?.items ?? []).map((r) => r.group);
  const egress = (egr.data?.items ?? []).map((r) => r.group);

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Сервисы</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setWizard(true)}><IconPlus />Новый сервис</button>
      </div>

      <Notice kind="info" title="Сервис — это то, что вы включаете">
        Соберите сервис в мастере: домены (списком, из каталога, с GitHub или по ссылке), точка входа и
        точка выхода. Домены живут внутри сервиса; общие «Списки доменов» нужны, только если один список
        переиспользуется несколькими сервисами.
      </Notice>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading ? <Spinner />
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
                  {list.data!.items.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{s.name}</div>
                        <div className="tiny dim mono">{s.slug}</div>
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
                      <td className="tiny mono dim">
                        {s.ingress_group_name ?? "—"} → {s.egress_group_name ?? "—"}
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
          ingress={ingress} egress={egress}
          onClose={() => setWizard(false)}
          onSaved={() => { setWizard(false); list.reload(); rules.reload(); }}
        />
      )}

      {editing && (
        <ServiceForm
          service={editing}
          ingress={ingress} egress={egress}
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

// ---------------------------------------------------------------------------
// New service: a 4-step wizard that creates the domain list and the service
// together via POST /services/wizard.
// ---------------------------------------------------------------------------

type DomainMode = "manual" | "catalog" | "github" | "url";

function ServiceWizard({ ingress, egress, onClose, onSaved }: {
  ingress: Named[]; egress: Named[];
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

  const [ingressId, setIngressId] = useState(ingress[0]?.id ?? "");
  const [egressId, setEgressId] = useState(egress[0]?.id ?? "");
  // Nothing to choose when there's exactly one of each — collapse to a summary.
  const [showRoute, setShowRoute] = useState(!(ingress.length === 1 && egress.length === 1));

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

  const canNext = step === 1 ? name.trim() !== ""
    : step === 2 ? domainsChosen
    : step === 3 ? !!ingressId && !!egressId
    : true;

  const submit = async () => {
    setBusy(true); setError("");
    const body: Record<string, unknown> = {
      name,
      ingress_group_id: ingressId || null,
      egress_group_id: egressId || null,
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
      await api("/services/wizard", { method: "POST", body, headers: { "Idempotency-Key": crypto.randomUUID() } });
      onSaved();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  const missingGroups = ingress.length === 0 || egress.length === 0;
  const ingressName = ingress.find((g) => g.id === ingressId)?.name ?? "—";
  const egressName = egress.find((g) => g.id === egressId)?.name ?? "—";

  return (
    <Modal title="Новый сервис" onClose={onClose} wide footer={
      <>
        {step > 1 && <button className="btn" onClick={() => setStep(step - 1)}>Назад</button>}
        <div className="spacer" />
        <button className="btn" onClick={onClose}>Отмена</button>
        {step < 4
          ? <button className="btn primary" disabled={!canNext} onClick={() => setStep(step + 1)}>Далее</button>
          : <button className="btn primary" disabled={busy || !canNext || missingGroups} onClick={submit}>
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
            <Field label="Домены" hint="По одному в строке. Можно с префиксом domain:, full: или regexp:. Можно вписать и/или загрузить файл.">
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
          {missingGroups && (
            <Notice kind="warn" title="Сначала создайте точки входа и выхода">
              Сервису нужны хотя бы одна точка входа и одна точка выхода. Создайте их и вернитесь.
            </Notice>
          )}
          {!showRoute && !missingGroups ? (
            <Notice kind="info" title="Маршрут">
              Через: <b>{ingressName}</b> → <b>{egressName}</b>{" "}
              <button type="button" className="linklike" onClick={() => setShowRoute(true)}>изменить</button>
            </Notice>
          ) : (
            <div className="grid g2">
              <Field label="Точка входа" hint="Куда устройства отправляют запросы.">
                <select className="select" value={ingressId} onChange={(e) => setIngressId(e.target.value)}>
                  <option value="">— не выбрана —</option>
                  {ingress.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                </select>
              </Field>
              <Field label="Точка выхода" hint="Через кого сервис выходит к сайту.">
                <select className="select" value={egressId} onChange={(e) => setEgressId(e.target.value)}>
                  <option value="">— не выбрана —</option>
                  {egress.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                </select>
              </Field>
            </div>
          )}
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

function ServiceForm({ service, ingress, egress, onClose, onSaved }: {
  service: Service; ingress: Named[]; egress: Named[];
  onClose: () => void; onSaved: () => void;
}) {
  const [name, setName] = useState(service.name);
  const [domains, setDomains] = useState((service.domains ?? []).join("\n"));
  const [ingressId, setIngressId] = useState(service.ingress_group_id ?? "");
  const [egressId, setEgressId] = useState(service.egress_group_id ?? "");
  const [ttl, setTtl] = useState(service.dns_ttl);
  const [priority, setPriority] = useState(service.priority);
  const [ports, setPorts] = useState((service.allowed_ports ?? [443]).join(", "));
  const [udp, setUdp] = useState(service.udp_mode);
  const [probeHost, setProbeHost] = useState((service.probe as any)?.hostname ?? "");
  const [enabled, setEnabled] = useState(service.enabled);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const addFile = (f: File) => {
    const r = new FileReader();
    r.onload = () => setDomains((d) => (d.trim() ? d.replace(/\s*$/, "") + "\n" : "") + String(r.result ?? "").trim());
    r.readAsText(f);
  };

  const save = async () => {
    setBusy(true); setError("");
    const body: Record<string, unknown> = {
      name, description: "", enabled,
      ingress_group_id: ingressId || null,
      egress_group_id: egressId || null,
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
        <button className="btn primary" onClick={save} disabled={busy}>
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
      <Field label="Домены" hint="По одному в строке. Можно с префиксом domain:, full: или regexp:. Правки применятся после сохранения.">
        <textarea className="textarea mono" rows={7} value={domains}
          placeholder={"gemini.google.com\naistudio.google.com"}
          onChange={(e) => setDomains(e.target.value)} />
        <label className="btn sm" style={{ marginTop: 8, display: "inline-flex", cursor: "pointer" }}>
          Загрузить файл
          <input type="file" accept=".txt,.list,text/plain" style={{ display: "none" }}
            onChange={(e) => { const f = e.target.files?.[0]; if (f) addFile(f); e.target.value = ""; }} />
        </label>
      </Field>
      <SourcesSection serviceId={service.id} />
      <div className="grid g2">
        <Field label="Точка входа">
          <select className="select" value={ingressId} onChange={(e) => setIngressId(e.target.value)}>
            <option value="">— не выбрана —</option>
            {ingress.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </Field>
        <Field label="Точка выхода">
          <select className="select" value={egressId} onChange={(e) => setEgressId(e.target.value)}>
            <option value="">— не выбрана —</option>
            {egress.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </Field>
      </div>
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
      <Field label="Домен для проверки"
        hint="Панель подключается к точке входа с этим именем и проверяет, что сертификат принадлежит настоящему сервису. Не используйте адреса, требующие входа в аккаунт.">
        <input className="input mono" value={probeHost} onChange={(e) => setProbeHost(e.target.value)}
          placeholder="gemini.google.com" />
      </Field>
      <label className="check">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Сервис включён
      </label>
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
        `/services/${serviceId}/refresh`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() } });
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
      <div style={{ display: "flex", gap: 8 }}>
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
