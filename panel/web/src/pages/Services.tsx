import { useState } from "react";
import { api, shortHash } from "../api";
import { Card, Confirm, ErrorState, Field, Modal, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconPlus, IconTrash } from "../icons";

type Service = {
  id: string; name: string; slug: string; enabled: boolean; priority: number;
  dns_ttl: number; udp_mode: string; allowed_ports: number[];
  rule_set_id: string | null; ingress_group_id: string | null; egress_group_id: string | null;
  rule_set_name: string | null; ingress_group_name: string | null; egress_group_name: string | null;
  rule_count: number | null; rule_set_hash: string | null;
  probe: Record<string, unknown>; version: number;
};
type Named = { id: string; name: string };

export default function Services() {
  const list = useAsync<{ items: Service[] }>(() => api("/services"), []);
  const rules = useAsync<{ items: Named[] }>(() => api("/rule-sets"), []);
  const ing = useAsync<{ items: { group: Named }[] }>(() => api("/ingress-groups"), []);
  const egr = useAsync<{ items: { group: Named }[] }>(() => api("/egress-groups"), []);
  const [editing, setEditing] = useState<Service | "new" | null>(null);
  const [removing, setRemoving] = useState<Service | null>(null);
  const toast = useToast();

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Сервисы</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setEditing("new")}><IconPlus />Новый сервис</button>
      </div>

      <Notice kind="info" title="Сервис связывает список доменов с маршрутом">
        Один и тот же список доменов управляет и подменой DNS, и пропуском на входе, и разрешёнными адресами на выходе.
        Если два сервиса претендуют на один домен с одинаковым приоритетом, сборка конфигурации остановится
        и покажет список конфликтов.
      </Notice>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading ? <Spinner />
        : list.data!.items.length === 0 ? (
          <Card>
            <div className="empty">
              <h3>Сервисов пока нет</h3>
              <p className="muted small">
                Сервис — это то, что вы включаете: например, Gemini. Сначала создайте список доменов,
                затем свяжите его с точкой входа и точкой выхода.
              </p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setEditing("new")}>
                <IconPlus />Новый сервис
              </button>
            </div>
          </Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr><th>Сервис</th><th>Список доменов</th><th>Маршрут</th><th>TTL</th>
                    <th>Порты</th><th>Приоритет</th><th>Состояние</th><th /></tr>
                </thead>
                <tbody>
                  {list.data!.items.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{s.name}</div>
                        <div className="tiny dim mono">{s.slug}</div>
                      </td>
                      <td>
                        <div className="small">{s.rule_set_name ?? <span className="dim">не выбран</span>}</div>
                        <div className="tiny dim mono">
                          {s.rule_count ?? 0} правил · {shortHash(s.rule_set_hash)}
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

      {editing && (
        <ServiceForm
          service={editing === "new" ? null : editing}
          ruleSets={rules.data?.items ?? []}
          ingress={(ing.data?.items ?? []).map((r) => r.group)}
          egress={(egr.data?.items ?? []).map((r) => r.group)}
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

function ServiceForm({ service, ruleSets, ingress, egress, onClose, onSaved }: {
  service: Service | null; ruleSets: Named[]; ingress: Named[]; egress: Named[];
  onClose: () => void; onSaved: () => void;
}) {
  const [name, setName] = useState(service?.name ?? "");
  const [slug, setSlug] = useState(service?.slug ?? "");
  const [ruleSetId, setRuleSetId] = useState(service?.rule_set_id ?? ruleSets[0]?.id ?? "");
  const [ingressId, setIngressId] = useState(service?.ingress_group_id ?? ingress[0]?.id ?? "");
  const [egressId, setEgressId] = useState(service?.egress_group_id ?? egress[0]?.id ?? "");
  const [ttl, setTtl] = useState(service?.dns_ttl ?? 60);
  const [priority, setPriority] = useState(service?.priority ?? 100);
  const [ports, setPorts] = useState((service?.allowed_ports ?? [443]).join(", "));
  const [udp, setUdp] = useState(service?.udp_mode ?? "disabled_fallback");
  const [probeHost, setProbeHost] = useState((service?.probe as any)?.hostname ?? "");
  const [enabled, setEnabled] = useState(service?.enabled ?? true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const missing = !ruleSets.length || !ingress.length || !egress.length;

  const save = async () => {
    setBusy(true); setError("");
    const body: Record<string, unknown> = {
      name, description: "", enabled,
      rule_set_id: ruleSetId || null,
      ingress_group_id: ingressId || null,
      egress_group_id: egressId || null,
      allowed_ports: ports.split(",").map((p) => Number(p.trim())).filter((n) => n > 0 && n < 65536),
      udp_mode: udp, dns_ttl: ttl, priority,
      probe: probeHost ? { hostname: probeHost.trim(), port: 443 } : {},
    };
    try {
      if (service) {
        await api(`/services/${service.id}`, {
          method: "PATCH", headers: { "If-Match": String(service.version) }, body,
        });
      } else {
        await api("/services", { method: "POST", body: { ...body, slug: slug || undefined, notes: "" } });
      }
      onSaved();
    } catch (e) { setError(errText(e)); } finally { setBusy(false); }
  };

  return (
    <Modal title={service ? `Сервис ${service.name}` : "Новый сервис"} onClose={onClose} wide footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" onClick={save} disabled={busy || missing}>
          {busy ? <span className="spin" /> : null}{service ? "Сохранить" : "Создать"}
        </button>
      </>
    }>
      {missing && (
        <Notice kind="warn" title="Не хватает зависимостей">
          Чтобы создать сервис, нужны список доменов, точка входа и точка выхода.
          Создайте недостающее и вернитесь сюда.
        </Notice>
      )}
      <div className="grid g2">
        <Field label="Название"><input className="input" value={name} autoFocus
          onChange={(e) => setName(e.target.value)} placeholder="Gemini" /></Field>
        <Field label="Идентификатор" hint={service ? "Изменить нельзя: он используется в метриках." : "Оставьте пустым, чтобы сгенерировать из названия."}>
          <input className="input mono" value={slug} disabled={!!service}
            onChange={(e) => setSlug(e.target.value)} placeholder="gemini" />
        </Field>
      </div>
      <div className="grid g3">
        <Field label="Список доменов">
          <select className="select" value={ruleSetId} onChange={(e) => setRuleSetId(e.target.value)}>
            <option value="">— не выбран —</option>
            {ruleSets.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </Field>
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
      <Field label="Режим UDP / QUIC"
        hint="По умолчанию UDP/443 отклоняется, и браузер откатывается на TCP. Включайте проксирование только после сквозной проверки конкретного сервиса.">
        <select className="select" value={udp} onChange={(e) => setUdp(e.target.value)}>
          <option value="disabled_fallback">Отключено, откат на TCP (рекомендуется)</option>
          <option value="proxy">Проксировать UDP</option>
          <option value="separate_ip">Отдельный IP для QUIC</option>
        </select>
      </Field>
      <Field label="Домен для проверки"
        hint="Панель подключается к ingress с этим SNI и проверяет, что сертификат принадлежит настоящему origin. Не используйте адреса, требующие входа в аккаунт.">
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
