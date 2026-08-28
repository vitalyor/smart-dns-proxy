import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, fmtTime, shortHash, timeTitle } from "../api";
import { Card, Confirm, ErrorState, Field, Modal, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconCheck, IconClose, IconPlus, IconRefresh, IconTrash } from "../icons";

type Source = {
  id: string; name: string; type: string; url: string; repo: string; ref: string;
  path: string; mode: string; enabled: boolean; expected_sha256: string;
};
type Version = {
  id: string; sequence: number; content_hash: string; status: string;
  counts: Record<string, number>; warnings: string[]; created_at: string;
};
type Fetch = { source_id: string; status: string; http_status: number; entries: number; error: string; started_at: string };
type Detail = {
  rule_set: { id: string; name: string; update_mode: string; allow_regex: boolean;
    manual_include: string[]; manual_exclude: string[]; version: number };
  sources: Source[]; versions: Version[]; fetches: Fetch[]; preview: string[]; entry_count: number;
};
type Diff = {
  version: Version;
  counts: { before: number; after: number; added: number; removed: number };
  added: string[]; removed: string[];
};

export default function RuleSetDetail() {
  const { id = "" } = useParams();
  const d = useAsync<Detail>(() => api(`/rule-sets/${id}`), [id]);
  const [addingSource, setAddingSource] = useState(false);
  const [diff, setDiff] = useState<Diff | null>(null);
  const [removingSource, setRemovingSource] = useState<Source | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  if (d.error) return <ErrorState message={d.error} onRetry={d.reload} />;
  if (d.loading || !d.data) return <Spinner />;
  const rs = d.data.rule_set;
  const pending = d.data.versions.find((v) => v.status === "awaiting_approval");

  const openDiff = async (versionId?: string) => {
    try {
      const v = await api<Diff>(`/rule-sets/${id}/diff${versionId ? `?version_id=${versionId}` : ""}`);
      setDiff(v);
    } catch (e) { toast({ kind: "bad", title: "Не удалось получить diff", body: errText(e) }); }
  };

  return (
    <>
      <div className="row">
        <div>
          <div className="eyebrow"><Link to="/rule-sets">списки доменов</Link> / {rs.name}</div>
          <h1>{rs.name}</h1>
        </div>
        <div className="spacer" />
        <button className="btn" disabled={busy} onClick={async () => {
          setBusy(true);
          try {
            const r = await api<{ status: string; unchanged: boolean; added: number; removed: number }>(
              `/rule-sets/${id}/fetch`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() } });
            toast({ kind: "ok", title: r.unchanged ? "Изменений нет" : `Кандидат готов: ${r.status}`,
              body: r.unchanged ? undefined : `Добавлено ${r.added}, удалено ${r.removed}.` });
            d.reload();
          } catch (e) { toast({ kind: "bad", title: "Обновление не удалось", body: `${errText(e)} Активная версия сохранена.` }); }
          finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : <IconRefresh />}Обновить сейчас</button>
        {pending && <button className="btn primary" onClick={() => openDiff(pending.id)}>Посмотреть изменения</button>}
      </div>

      {pending && (
        <Notice kind="warn" title={`Версия #${pending.sequence} ждёт подтверждения`}>
          {pending.warnings.length > 0 ? pending.warnings.join(" ")
            : "Проверьте diff и примите решение — до подтверждения работает предыдущая версия."}
        </Notice>
      )}

      <div className="grid g2">
        <Card title="Источники" eyebrow={`${d.data.entry_count} записей после объединения`} tight
          actions={<button className="btn sm" onClick={() => setAddingSource(true)}><IconPlus />Источник</button>}>
          {d.data.sources.length === 0 ? (
            <div className="empty"><h3>Источников нет</h3>
              <p className="muted small">Добавьте ссылку на GitHub, произвольный HTTPS-адрес или встроенный список.</p></div>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Источник</th><th>Тип</th><th>Роль</th><th /></tr></thead>
                <tbody>
                  {d.data.sources.map((s) => {
                    const last = d.data!.fetches.find((f) => f.source_id === s.id);
                    return (
                      <tr key={s.id}>
                        <td>
                          <div className="small" style={{ fontWeight: 550 }}>{s.name || s.repo || s.url || s.path}</div>
                          <div className="tiny dim mono" style={{ wordBreak: "break-all" }}>
                            {s.repo ? `${s.repo}@${s.ref || "main"}:${s.path}` : s.url || s.path}
                          </div>
                          {last && (
                            <div className="tiny" style={{ marginTop: 4, color: last.status === "ok" ? "var(--text-3)" : "var(--danger)" }}>
                              {last.status === "ok" ? `${last.entries} записей` : last.error}
                            </div>
                          )}
                        </td>
                        <td><span className="badge plain">{s.type}</span></td>
                        <td>{s.mode === "exclude" ? <span className="badge bad">исключение</span> : <span className="badge ok">включение</span>}</td>
                        <td className="actions">
                          <button className="btn sm ghost danger" aria-label="Удалить источник"
                            onClick={() => setRemovingSource(s)}><IconTrash /></button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>

        <Card title="История версий" eyebrow="неизменяемые снимки" tight>
          <div className="table-wrap">
            <table className="table">
              <thead><tr><th>#</th><th>Хеш</th><th>Записей</th><th>Состояние</th><th>Создана</th><th /></tr></thead>
              <tbody>
                {d.data.versions.map((v) => (
                  <tr key={v.id}>
                    <td className="num">{v.sequence}</td>
                    <td className="hash">{shortHash(v.content_hash)}</td>
                    <td className="num">{v.counts?.total ?? 0}</td>
                    <td>
                      {v.status === "active" ? <span className="badge ok">активна</span>
                        : v.status === "awaiting_approval" ? <span className="badge warn">ждёт</span>
                        : v.status === "rejected" ? <span className="badge bad">отклонена</span>
                        : <span className="badge">{v.status}</span>}
                    </td>
                    <td className="small dim" title={timeTitle(v.created_at)}>{fmtTime(v.created_at)}</td>
                    <td className="actions">
                      <button className="btn sm ghost" onClick={() => openDiff(v.id)}>Diff</button>
                    </td>
                  </tr>
                ))}
                {d.data.versions.length === 0 && (
                  <tr><td colSpan={6}><div className="empty" style={{ padding: 24 }}>
                    <h3>Версий пока нет</h3><p className="muted small">Нажмите «Обновить сейчас», чтобы собрать первую.</p>
                  </div></td></tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      </div>

      <Card title="Активные правила" eyebrow="первые 200 записей после нормализации">
        {d.data.preview.length === 0 ? (
          <p className="muted small" style={{ margin: 0 }}>Активной версии нет.</p>
        ) : (
          <div className="difflist" style={{ maxHeight: 280 }}>
            {d.data.preview.map((p) => <div key={p}>{p}</div>)}
          </div>
        )}
      </Card>

      {addingSource && (
        <AddSource ruleSetId={id} onClose={() => setAddingSource(false)}
          onAdded={() => { setAddingSource(false); d.reload(); }} />
      )}

      {removingSource && (
        <Confirm title="Удалить источник?" danger confirmLabel="Удалить"
          body="Записи этого источника исчезнут из списка при следующем обновлении. Активная версия не меняется до пересборки."
          onClose={() => setRemovingSource(null)}
          onConfirm={async () => {
            await api(`/rule-sets/${id}/sources/${removingSource.id}`, { method: "DELETE" });
            setRemovingSource(null); d.reload();
          }} />
      )}

      {diff && (
        <Modal title={`Изменения версии #${diff.version.sequence}`} onClose={() => setDiff(null)} wide footer={
          diff.version.status === "awaiting_approval" ? (
            <>
              <button className="btn danger" onClick={async () => {
                await api(`/rule-sets/${id}/approve`, { method: "POST", body: { version_id: diff.version.id, reject: true } });
                toast({ kind: "ok", title: "Версия отклонена", body: "Активной осталась предыдущая." });
                setDiff(null); d.reload();
              }}><IconClose />Отклонить</button>
              <button className="btn primary" onClick={async () => {
                await api(`/rule-sets/${id}/approve`, { method: "POST", body: { version_id: diff.version.id } });
                toast({ kind: "ok", title: "Версия активирована", body: "Соберите конфигурацию, чтобы применить её на нодах." });
                setDiff(null); d.reload();
              }}><IconCheck />Применить</button>
            </>
          ) : <button className="btn" onClick={() => setDiff(null)}>Закрыть</button>
        }>
          <div className="grid g4">
            <Stat label="Было" value={diff.counts.before} />
            <Stat label="Стало" value={diff.counts.after} />
            <Stat label="Добавлено" value={diff.counts.added} tone="ok" />
            <Stat label="Удалено" value={diff.counts.removed} tone="bad" />
          </div>
          {diff.version.warnings?.length > 0 && (
            <Notice kind="warn" title="Предупреждения сборки">
              <ul style={{ margin: "4px 0 0 16px", padding: 0 }}>
                {diff.version.warnings.map((w, i) => <li key={i}>{w}</li>)}
              </ul>
            </Notice>
          )}
          <div className="grid g2">
            <div>
              <div className="eyebrow" style={{ marginBottom: 8 }}>добавлено</div>
              <div className="difflist">
                {diff.added.length === 0 ? <div className="dim">нет</div>
                  : diff.added.map((a) => <div key={a} className="add">+ {a}</div>)}
              </div>
            </div>
            <div>
              <div className="eyebrow" style={{ marginBottom: 8 }}>удалено</div>
              <div className="difflist">
                {diff.removed.length === 0 ? <div className="dim">нет</div>
                  : diff.removed.map((a) => <div key={a} className="rem">− {a}</div>)}
              </div>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: string }) {
  return (
    <div className="card tile">
      <div className="tile-label">{label}</div>
      <div className={`tile-value ${tone ?? ""}`}>{value}</div>
    </div>
  );
}

function AddSource({ ruleSetId, onClose, onAdded }: { ruleSetId: string; onClose: () => void; onAdded: () => void }) {
  const [type, setType] = useState("github_repo");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("main");
  const [path, setPath] = useState("");
  const [mode, setMode] = useState("include");
  const [sha, setSha] = useState("");
  const [body, setBody] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <Modal title="Добавить источник" onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose}>Отмена</button>
        <button className="btn primary" disabled={busy} onClick={async () => {
          setBusy(true); setError("");
          try {
            await api(`/rule-sets/${ruleSetId}/sources`, {
              method: "POST",
              body: { name, type, url, repo, ref, path, mode, expected_sha256: sha, body, enabled: true },
            });
            onAdded();
          } catch (e) { setError(errText(e)); } finally { setBusy(false); }
        }}>{busy ? <span className="spin" /> : null}Добавить</button>
      </>
    }>
      <Field label="Тип источника">
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          <option value="github_repo">GitHub: owner/repo + путь</option>
          <option value="github_raw">GitHub raw: прямая ссылка</option>
          <option value="https">Произвольный HTTPS-адрес</option>
          <option value="singbox_json">sing-box rule-set JSON</option>
          <option value="manual">Свой список (вставить текстом)</option>
        </select>
      </Field>
      <Field label="Название" hint="Как источник будет подписан в интерфейсе.">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>

      {type === "github_repo" ? (
        <>
          <div className="grid g2">
            <Field label="Репозиторий"><input className="input mono" value={repo}
              onChange={(e) => setRepo(e.target.value)} placeholder="owner/repo" /></Field>
            <Field label="Ref" hint="Для production укажите commit SHA или тег: ветка обновляется без вашего ведома.">
              <input className="input mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" />
            </Field>
          </div>
          <Field label="Путь к файлу"><input className="input mono" value={path}
            onChange={(e) => setPath(e.target.value)} placeholder="rules/ai.txt" /></Field>
        </>
      ) : type === "manual" ? (
        <Field label="Список доменов" hint="По одному в строке.">
          <textarea className="textarea mono" value={body} onChange={(e) => setBody(e.target.value)} />
        </Field>
      ) : (
        <Field label="Адрес" hint="Только HTTPS. Каждое перенаправление проверяется на приватные адреса.">
          <input className="input mono" value={url} onChange={(e) => setUrl(e.target.value)}
            placeholder="https://raw.githubusercontent.com/owner/repo/main/list.txt" />
        </Field>
      )}

      <div className="grid g2">
        <Field label="Роль в списке" hint="Исключения применяются после объединения всех включений.">
          <select className="select" value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="include">Включение</option>
            <option value="exclude">Исключение</option>
          </select>
        </Field>
        <Field label="Ожидаемый SHA-256" hint="Необязательно. Загрузка с другим хешем будет отклонена.">
          <input className="input mono" value={sha} onChange={(e) => setSha(e.target.value)} />
        </Field>
      </div>

      {error && <div className="notice bad" role="alert"><span className="notice-bar" /><div className="n-body">{error}</div></div>}
    </Modal>
  );
}
