import { useState } from "react";
import { api } from "../api";
import {
  Card, Confirm, ErrorState, Field, Modal, Notice, Spinner, StatusBadge,
  errText, useAsync, useToast,
} from "../ui";
import { IconPlus, IconTrash } from "../icons";

type Group = { id: string; name: string; mode: string; version: number };
type Member = { id: string; node_id: string; node_name: string; status: string; priority: number; weight: number; enabled: boolean };
type Row = { group: Group; members: Member[] };
type Node = { id: string; name: string; role: string };

const MODES: Record<string, { value: string; label: string; hint: string }[]> = {
  ingress: [
    { value: "active_active", label: "Активная пара", hint: "DNS возвращает все живые ноды и меняет их порядок при каждом ответе." },
    { value: "primary_fallback", label: "Основная и резервная", hint: "Резервная нода выдаётся только после подтверждённого отказа основной." },
    { value: "weighted", label: "С весами", hint: "Адреса выдаются с заданной вероятностью. Точного распределения это не гарантирует." },
  ],
  egress: [
    { value: "primary_fallback", label: "Основная и резервная", hint: "Ingress пробует ноды по приоритету и уходит на резерв при отказе." },
    { value: "weighted", label: "С весами", hint: "Новые соединения распределяются между нодами пропорционально весу." },
    { value: "lowest_latency", label: "По минимальной задержке", hint: "Переключение только если кандидат быстрее на 20% и 20 мс в трёх замерах подряд." },
    { value: "manual_fixed", label: "Фиксированная нода", hint: "Всегда используется первая живая нода по приоритету." },
  ],
};

export default function Groups({ kind }: { kind: "ingress" | "egress" }) {
  const base = `/${kind}-groups`;
  const groups = useAsync<{ items: Row[] }>(() => api(base), [kind]);
  const nodes = useAsync<{ items: Node[] }>(() => api("/nodes"), []);
  const [creating, setCreating] = useState(false);
  const [addingTo, setAddingTo] = useState<Row | null>(null);
  const [removing, setRemoving] = useState<Group | null>(null);
  const toast = useToast();

  const title = kind === "ingress" ? "Ingress-группы" : "Egress-группы";
  const lead = kind === "ingress"
    ? "Группа определяет, какие адреса DNS выдаёт для управляемых доменов."
    : "Группа определяет, через какие зарубежные ноды сервис выходит к origin.";

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">инфраструктура</div><h1>{title}</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Создать группу</button>
      </div>

      <Notice kind="info" title={lead}>
        {kind === "ingress"
          ? "Два DNS-адреса в настройках операционной системы не гарантируют переключение primary → secondary: ОС могут использовать оба сервера или долго держаться за первый."
          : "Локальное переключение выполняет сама ingress-нода, поэтому резервирование продолжает работать даже при выключенной панели."}
      </Notice>

      {groups.error ? <ErrorState message={groups.error} onRetry={groups.reload} />
        : groups.loading ? <Spinner />
        : groups.data!.items.length === 0 ? (
          <Card>
            <div className="empty">
              <h3>Групп пока нет</h3>
              <p className="muted small">Создайте группу и добавьте в неё ноды — сервисы ссылаются на группы, а не на отдельные ноды.</p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating(true)}>
                <IconPlus />Создать группу
              </button>
            </div>
          </Card>
        ) : groups.data!.items.map((row) => (
          <Card key={row.group.id} title={row.group.name}
            eyebrow={MODES[kind].find((m) => m.value === row.group.mode)?.label ?? row.group.mode}
            actions={
              <>
                <button className="btn sm" onClick={() => setAddingTo(row)}><IconPlus />Добавить ноду</button>
                <button className="btn sm ghost danger" onClick={() => setRemoving(row.group)}><IconTrash /></button>
              </>
            } tight>
            <p className="small muted" style={{ padding: "12px 16px", margin: 0, borderBottom: "1px solid var(--line-soft)" }}>
              {MODES[kind].find((m) => m.value === row.group.mode)?.hint}
            </p>
            {row.members.length === 0 ? (
              <div className="empty" style={{ padding: 28 }}>
                <h3>В группе нет нод</h3>
                <p className="muted small">Пока группа пуста, сервисы, которые на неё ссылаются, не скомпилируются.</p>
              </div>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead><tr><th>Нода</th><th>Статус</th><th>Приоритет</th><th>Вес</th><th /></tr></thead>
                  <tbody>
                    {row.members.map((m) => (
                      <tr key={m.id}>
                        <td className="mono small">{m.node_name}</td>
                        <td><StatusBadge status={m.status} /></td>
                        <td className="num">{m.priority}</td>
                        <td className="num">{m.weight}</td>
                        <td className="actions">
                          <button className="btn sm ghost danger" onClick={async () => {
                            try {
                              await api(`${base}/${row.group.id}/members/${m.node_id}`, { method: "DELETE" });
                              toast({ kind: "ok", title: "Нода убрана из группы" });
                              groups.reload();
                            } catch (e) { toast({ kind: "bad", title: "Не удалось убрать ноду", body: errText(e) }); }
                          }}>Убрать</button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        ))}

      {creating && (
        <CreateGroup kind={kind} onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); groups.reload(); }} />
      )}

      {addingTo && (
        <AddMember base={base} group={addingTo}
          candidates={(nodes.data?.items ?? []).filter(
            (n) => n.role === kind && !addingTo.members.some((m) => m.node_id === n.id))}
          onClose={() => setAddingTo(null)}
          onAdded={() => { setAddingTo(null); groups.reload(); }} />
      )}

      {removing && (
        <Confirm title={`Удалить группу ${removing.name}?`} danger confirmLabel="Удалить"
          body="Если на группу ссылается хотя бы один сервис, панель откажет в удалении и покажет список зависимостей."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`${base}/${removing.id}`, { method: "DELETE" });
              toast({ kind: "ok", title: "Группа удалена" });
              setRemoving(null); groups.reload();
            } catch (e) { toast({ kind: "bad", title: "Удаление отклонено", body: errText(e) }); }
          }} />
      )}
    </>
  );
}

function CreateGroup({ kind, onClose, onCreated }: { kind: "ingress" | "egress"; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [mode, setMode] = useState(MODES[kind][0].value);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const hint = MODES[kind].find((m) => m.value === mode)?.hint;

  return (
    <Modal title="Создать группу" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api(`/${kind}-groups`, { method: "POST", body: { name, mode, settings: {} } });
            onCreated();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать</button>
      </>
    }>
      <Field label="Название"><input className="input" autoFocus value={name}
        onChange={(e) => setName(e.target.value)} placeholder={kind === "ingress" ? "Вход РФ" : "Выход ЕС"} /></Field>
      <Field label="Режим" hint={hint}>
        <select className="select" value={mode} onChange={(e) => setMode(e.target.value)}>
          {MODES[kind].map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
        </select>
      </Field>
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}

function AddMember({ base, group, candidates, onClose, onAdded }: {
  base: string; group: Row; candidates: Node[]; onClose: () => void; onAdded: () => void;
}) {
  const [nodeId, setNodeId] = useState(candidates[0]?.id ?? "");
  const [priority, setPriority] = useState(1);
  const [weight, setWeight] = useState(1);
  const [error, setError] = useState("");

  return (
    <Modal title={`Добавить ноду в «${group.group.name}»`} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={!nodeId} onClick={async () => {
          setError("");
          try {
            await api(`${base}/${group.group.id}/members`, {
              method: "POST", body: { node_id: nodeId, priority, weight, enabled: true },
            });
            onAdded();
          } catch (e) { setError(errText(e)); }
        }}>Добавить</button>
      </>
    }>
      {candidates.length === 0 ? (
        <Notice kind="warn" title="Нет подходящих нод">
          Все ноды нужной роли уже в этой группе, либо ни одна нода такой роли ещё не зарегистрирована.
        </Notice>
      ) : (
        <>
          <Field label="Нода">
            <select className="select" value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
              {candidates.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
            </select>
          </Field>
          <div className="grid g2">
            <Field label="Приоритет" hint="Меньше — раньше в очереди.">
              <input className="input num" type="number" min={1} value={priority}
                onChange={(e) => setPriority(Number(e.target.value))} />
            </Field>
            <Field label="Вес" hint="Используется только в режиме «с весами».">
              <input className="input num" type="number" min={1} value={weight}
                onChange={(e) => setWeight(Number(e.target.value))} />
            </Field>
          </div>
        </>
      )}
      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}
