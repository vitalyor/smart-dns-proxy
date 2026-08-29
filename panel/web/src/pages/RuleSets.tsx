import { useState } from "react";
import { Link } from "react-router-dom";
import { api, ago, plural, shortHash, timeTitle } from "../api";
import { Card, Confirm, ErrorState, Field, Modal, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconPlus, IconRefresh, IconTrash } from "../icons";

type RuleSet = {
  id: string; name: string; description: string; update_mode: string; interval_sec: number;
  allow_regex: boolean; priority: number; entry_count: number; source_count: number;
  active_sequence: number | null; active_hash: string | null;
  pending_version_id: string | null; used_by_services: number; last_fetch_at: string | null;
};

const MODE_LABEL: Record<string, string> = {
  auto_apply: "автоприменение",
  manual_approve: "с подтверждением",
  manual_only: "только вручную",
};

export default function RuleSets() {
  const list = useAsync<{ items: RuleSet[]; presets: string[] }>(() => api("/rule-sets"), []);
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<RuleSet | null>(null);
  const [busyId, setBusyId] = useState("");
  const toast = useToast();

  const fetchNow = async (rs: RuleSet) => {
    setBusyId(rs.id);
    try {
      const r = await api<{ status: string; added: number; removed: number; unchanged: boolean }>(
        `/rule-sets/${rs.id}/fetch`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() } });
      toast({
        kind: "ok",
        title: r.unchanged ? "Изменений нет" : r.status === "active" ? "Список обновлён" : "Кандидат ждёт подтверждения",
        body: r.unchanged ? "Источники вернули тот же контент." : `Добавлено ${r.added}, удалено ${r.removed}.`,
      });
      list.reload();
    } catch (e) {
      toast({ kind: "bad", title: "Обновление не удалось", body: `${errText(e)} Активная версия не изменилась.` });
    } finally { setBusyId(""); }
  };

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Общие списки</h1></div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}><IconPlus />Новый список</button>
      </div>

      <Notice kind="info" title="Нужны, только если список переиспользуется несколькими сервисами">
        Обычно домены задаются прямо в сервисе через мастер. Общий список имеет смысл, когда один и тот же
        набор доменов нужен сразу нескольким сервисам, или когда список большой и обновляется из источника.
      </Notice>

      <Notice kind="info" title="Ошибка загрузки никогда не стирает активный список">
        Если источник недоступен, вернул пустой ответ или HTML вместо списка, кандидат отклоняется и
        продолжает работать предыдущая версия. Массовые изменения — удалено больше 30% или добавлено
        больше 1000 записей — требуют ручного подтверждения даже в режиме автоприменения.
      </Notice>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading ? <Spinner />
        : list.data!.items.length === 0 ? (
          <Card>
            <div className="empty">
              <h3>Списков доменов пока нет</h3>
              <p className="muted small">
                Список доменов, которые надо направлять через вашу инфраструктуру.
                Можно начать со встроенного пресета и позже добавить источник на GitHub.
              </p>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setCreating(true)}>
                <IconPlus />Новый список
              </button>
            </div>
          </Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr><th>Список</th><th>Записей</th><th>Версия</th><th>Источники</th>
                    <th>Обновление</th><th>Последняя загрузка</th><th /></tr>
                </thead>
                <tbody>
                  {list.data!.items.map((rs) => (
                    <tr key={rs.id}>
                      <td>
                        <Link to={`/rule-sets/${rs.id}`} style={{ fontWeight: 550 }}>{rs.name}</Link>
                        <div className="tiny dim">{plural(rs.used_by_services, "сервис", "сервиса", "сервисов")} использует</div>
                      </td>
                      <td className="num">{rs.entry_count}</td>
                      <td className="small">
                        {rs.active_sequence ? <>#{rs.active_sequence} <span className="hash">{shortHash(rs.active_hash)}</span></>
                          : <span className="dim">нет активной</span>}
                        {rs.pending_version_id && <div><span className="badge warn" style={{ marginTop: 4 }}>ждёт подтверждения</span></div>}
                      </td>
                      <td className="num small">{rs.source_count}</td>
                      <td className="small">
                        <span className="badge plain">{MODE_LABEL[rs.update_mode]}</span>
                        <div className="tiny dim" style={{ marginTop: 4 }}>каждые {Math.round(rs.interval_sec / 3600)} ч</div>
                      </td>
                      <td className="small dim" title={timeTitle(rs.last_fetch_at)}>{ago(rs.last_fetch_at)}</td>
                      <td className="actions">
                        <button className="btn sm" disabled={busyId === rs.id} onClick={() => fetchNow(rs)}>
                          {busyId === rs.id ? <span className="spin" /> : <IconRefresh />}Обновить
                        </button>
                        <Link className="btn sm ghost" to={`/rule-sets/${rs.id}`}>Открыть</Link>
                        <button className="btn sm ghost danger" aria-label={`Удалить ${rs.name}`}
                          onClick={() => setRemoving(rs)}><IconTrash /></button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

      {creating && (
        <CreateRuleSet presets={list.data?.presets ?? []} onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); list.reload(); }} />
      )}

      {removing && (
        <Confirm title={`Удалить список ${removing.name}?`} danger confirmLabel="Удалить"
          body="Все версии и загруженные записи будут удалены. Если список используется сервисом, панель откажет в удалении."
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await api(`/rule-sets/${removing.id}`, { method: "DELETE" });
              toast({ kind: "ok", title: "Список удалён" });
              setRemoving(null); list.reload();
            } catch (e) { toast({ kind: "bad", title: "Удаление отклонено", body: errText(e) }); }
          }} />
      )}
    </>
  );
}

function CreateRuleSet({ presets, onClose, onCreated }: {
  presets: string[]; onClose: () => void; onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [mode, setMode] = useState("manual_approve");
  const [hours, setHours] = useState(6);
  const [preset, setPreset] = useState(presets[0] ?? "");
  const [usePreset, setUsePreset] = useState(presets.length > 0);
  const [manual, setManual] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <Modal title="Новый список доменов" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            const rs = await api<{ id: string }>("/rule-sets", {
              method: "POST",
              body: {
                name, description: "", update_mode: mode, interval_sec: hours * 3600,
                allow_regex: false, priority: 100,
                manual_include: manual.split("\n").map((s) => s.trim()).filter(Boolean),
                manual_exclude: [],
              },
            });
            if (usePreset && preset) {
              await api(`/rule-sets/${rs.id}/sources`, {
                method: "POST",
                body: { name: `Встроенный список: ${preset}`, type: "preset", path: preset, mode: "include" },
              });
            }
            await api(`/rule-sets/${rs.id}/fetch`, {
              method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() },
            }).catch(() => undefined);
            onCreated();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Создать</button>
      </>
    }>
      <Field label="Название"><input className="input" autoFocus value={name}
        onChange={(e) => setName(e.target.value)} placeholder="ИИ-ассистенты" /></Field>

      <Field label="Режим обновления" hint={
        mode === "auto_apply" ? "Безопасные изменения применяются сразу; подозрительные всё равно ждут подтверждения."
        : mode === "manual_approve" ? "Каждое обновление показывается как diff и ждёт вашего решения."
        : "Панель не будет обновлять список по расписанию."}>
        <select className="select" value={mode} onChange={(e) => setMode(e.target.value)}>
          <option value="manual_approve">С подтверждением (рекомендуется)</option>
          <option value="auto_apply">Автоприменение</option>
          <option value="manual_only">Только вручную</option>
        </select>
      </Field>

      {mode !== "manual_only" && (
        <Field label="Интервал проверки">
          <select className="select" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
            <option value={1}>каждый час</option>
            <option value={6}>каждые 6 часов</option>
            <option value={24}>раз в сутки</option>
          </select>
        </Field>
      )}

      {presets.length > 0 && (
        <>
          <label className="check">
            <input type="checkbox" checked={usePreset} onChange={(e) => setUsePreset(e.target.checked)} />
            Начать со встроенного списка
          </label>
          {usePreset && (
            <Field label="Встроенный список" hint="Поставляется с проектом. Позже можно заменить источником на GitHub.">
              <select className="select" value={preset} onChange={(e) => setPreset(e.target.value)}>
                {presets.map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
            </Field>
          )}
        </>
      )}

      <Field label="Свои домены" hint="По одному в строке. Поддерживаются domain:, domain-suffix:, full: и *.example.com.">
        <textarea className="textarea mono" value={manual} onChange={(e) => setManual(e.target.value)}
          placeholder={"domain:example.com\nfull:api.example.com"} />
      </Field>

      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}
