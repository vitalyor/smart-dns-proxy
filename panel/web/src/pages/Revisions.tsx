import { useState } from "react";
import { api, fmtTime, shortHash, timeTitle } from "../api";
import { Card, ErrorState, Modal, Notice, Spinner, errText, usePoll, useToast } from "../ui";
import { IconLayers, IconPlay, IconRotate } from "../icons";

type Revision = {
  id: string; sequence: number; state: string; model_hash: string; error: string;
  summary: { summary?: { services: number; ingress_nodes: number; egress_nodes: number; total_rules: number;
    rules_by_service?: Record<string, number>; lab_mode?: boolean }; warnings?: string[] };
  activated_at: string | null; created_at: string;
};
type Deployment = {
  id: string; node_name: string; state: string; wave: number;
  error_code: string; error_detail: string; started_at: string; finished_at: string | null;
};
type Detail = { revision: Revision; deployments: Deployment[];
  artifacts: { node_id: string; node_name: string; sha256: string; size_bytes: number }[] };

const STATE: Record<string, { kind: string; label: string }> = {
  draft: { kind: "", label: "черновик" },
  compiled: { kind: "direct", label: "собрана" },
  validation_failed: { kind: "bad", label: "ошибка сборки" },
  awaiting_approval: { kind: "warn", label: "ждёт подтверждения" },
  deploying: { kind: "managed", label: "выкатывается" },
  active: { kind: "ok", label: "активна" },
  partially_active: { kind: "warn", label: "частично активна" },
  superseded: { kind: "", label: "заменена" },
  rolled_back: { kind: "bad", label: "откачена" },
};
const DEPLOY_STATE: Record<string, { kind: string; label: string }> = {
  pending: { kind: "", label: "ожидает" },
  downloading: { kind: "direct", label: "загрузка" },
  validating: { kind: "direct", label: "проверка" },
  applying: { kind: "managed", label: "применение" },
  applied: { kind: "ok", label: "применена" },
  failed: { kind: "bad", label: "ошибка" },
  rolled_back: { kind: "bad", label: "откат" },
  skipped: { kind: "", label: "пропущена" },
};

export default function Revisions() {
  const list = usePoll<{ items: Revision[] }>(() => api("/revisions"), 10000, []);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [preview, setPreview] = useState<any | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const compile = async (deploy: boolean) => {
    setBusy(true);
    try {
      const r = await api<any>("/revisions/compile", {
        method: "POST", body: { deploy, dry_run: false },
        headers: { "Idempotency-Key": crypto.randomUUID() },
      });
      toast({ kind: "ok", title: deploy ? "Ревизия собрана и выкатывается" : "Ревизия собрана",
        body: `Сервисов: ${r.summary.services}, правил: ${r.summary.total_rules}.` });
      list.reload();
    } catch (e) { toast({ kind: "bad", title: "Сборка не удалась", body: errText(e) }); }
    finally { setBusy(false); }
  };

  const dryRun = async () => {
    setBusy(true);
    try {
      setPreview(await api("/revisions/compile", { method: "POST", body: { dry_run: true } }));
    } catch (e) { toast({ kind: "bad", title: "Проверка не прошла", body: errText(e) }); }
    finally { setBusy(false); }
  };

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">конфигурация</div><h1>Ревизии</h1></div>
        <div className="spacer" />
        <button className="btn" disabled={busy} onClick={dryRun}>Проверить без сборки</button>
        <button className="btn" disabled={busy} onClick={() => compile(false)}><IconLayers />Собрать</button>
        <button className="btn primary" disabled={busy} onClick={() => compile(true)}>
          {busy ? <span className="spin" /> : <IconPlay />}Собрать и выкатить
        </button>
      </div>

      <Notice kind="info" title="Выкат идёт волнами">
        Первая нода каждой роли получает конфигурацию как канарейка, остальные — после её успешного отчёта.
        Панель считает ревизию применённой, только когда агент вернул тот же SHA-256 артефакта.
      </Notice>

      {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
        : list.loading && !list.data ? <Spinner />
        : list.data!.items.length === 0 ? (
          <Card><div className="empty">
            <h3>Ревизий пока нет</h3>
            <p className="muted small">Ревизия — это снимок всей конфигурации. Соберите первую, когда появятся сервис, группы и ноды.</p>
          </div></Card>
        ) : (
          <Card tight>
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>#</th><th>Состояние</th><th>Содержимое</th><th>Хеш модели</th>
                  <th>Создана</th><th>Активирована</th><th /></tr></thead>
                <tbody>
                  {list.data!.items.map((r) => {
                    const s = STATE[r.state] ?? { kind: "", label: r.state };
                    const sum = r.summary?.summary;
                    return (
                      <tr key={r.id}>
                        <td className="num" style={{ fontWeight: 600 }}>{r.sequence}</td>
                        <td>
                          <span className={`badge ${s.kind}`}>{s.label}</span>
                          {r.error && <div className="tiny" style={{ color: "var(--danger)", marginTop: 4 }}>{r.error}</div>}
                        </td>
                        <td className="small">
                          {sum ? <>{sum.services} сервисов · {sum.total_rules} правил · {sum.ingress_nodes}+{sum.egress_nodes} нод</>
                            : <span className="dim">—</span>}
                        </td>
                        <td className="hash">{shortHash(r.model_hash)}</td>
                        <td className="small dim" title={timeTitle(r.created_at)}>{fmtTime(r.created_at)}</td>
                        <td className="small dim" title={timeTitle(r.activated_at)}>{fmtTime(r.activated_at)}</td>
                        <td className="actions">
                          <button className="btn sm ghost" onClick={async () => {
                            try { setDetail(await api(`/revisions/${r.id}`)); }
                            catch (e) { toast({ kind: "bad", title: "Не удалось открыть ревизию", body: errText(e) }); }
                          }}>Детали</button>
                          {["compiled", "active", "partially_active", "rolled_back", "superseded"].includes(r.state) && (
                            <button className="btn sm" onClick={async () => {
                              try {
                                await api(`/revisions/${r.id}/deploy`, {
                                  method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() } });
                                toast({ kind: "ok", title: `Выкат ревизии #${r.sequence} начат` });
                                list.reload();
                              } catch (e) { toast({ kind: "bad", title: "Выкат отклонён", body: errText(e) }); }
                            }}>Выкатить</button>
                          )}
                          {["active", "partially_active"].includes(r.state) && (
                            <button className="btn sm ghost danger" onClick={async () => {
                              try {
                                const v = await api<{ target_revision_id: string }>(`/revisions/${r.id}/rollback`, {
                                  method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() } });
                                toast({ kind: "warn", title: "Откат запущен", body: `Целевая ревизия: ${shortHash(v.target_revision_id)}` });
                                list.reload();
                              } catch (e) { toast({ kind: "bad", title: "Откат невозможен", body: errText(e) }); }
                            }}><IconRotate />Откатить</button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </Card>
        )}

      {preview && (
        <Modal title="Предварительная проверка" onClose={() => setPreview(null)} wide
          footer={<button className="btn" onClick={() => setPreview(null)}>Закрыть</button>}>
          <Notice kind="info" title="Ничего не изменено">
            Это сухой прогон компилятора: артефакты не сохранены и на ноды не отправлены.
          </Notice>
          <div className="grid g4">
            <Tile label="Сервисов" value={preview.summary.services} />
            <Tile label="Правил" value={preview.summary.total_rules} />
            <Tile label="Ingress" value={preview.summary.ingress_nodes} />
            <Tile label="Egress" value={preview.summary.egress_nodes} />
          </div>
          {preview.warnings?.length > 0 && (
            <Notice kind="warn" title="Предупреждения">
              <ul style={{ margin: "4px 0 0 16px", padding: 0 }}>
                {preview.warnings.map((w: string, i: number) => <li key={i}>{w}</li>)}
              </ul>
            </Notice>
          )}
        </Modal>
      )}

      {detail && (
        <Modal title={`Ревизия #${detail.revision.sequence}`} onClose={() => setDetail(null)} wide
          footer={<button className="btn" onClick={() => setDetail(null)}>Закрыть</button>}>
          <dl className="kv">
            <dt>Хеш модели</dt><dd>{detail.revision.model_hash || "—"}</dd>
            <dt>Состояние</dt><dd>{STATE[detail.revision.state]?.label ?? detail.revision.state}</dd>
            <dt>Создана</dt><dd>{fmtTime(detail.revision.created_at)}</dd>
          </dl>

          {detail.revision.summary?.warnings?.length ? (
            <Notice kind="warn" title="Предупреждения компилятора">
              <ul style={{ margin: "4px 0 0 16px", padding: 0 }}>
                {detail.revision.summary.warnings.map((w, i) => <li key={i}>{w}</li>)}
              </ul>
            </Notice>
          ) : null}

          <div>
            <div className="eyebrow" style={{ marginBottom: 8 }}>состояние по нодам</div>
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Нода</th><th>Волна</th><th>Состояние</th><th>Начато</th><th>Завершено</th></tr></thead>
                <tbody>
                  {detail.deployments.map((dp) => {
                    const st = DEPLOY_STATE[dp.state] ?? { kind: "", label: dp.state };
                    return (
                      <tr key={dp.id}>
                        <td className="mono small">{dp.node_name}</td>
                        <td className="small">{dp.wave === 0 ? "канарейка" : "основная"}</td>
                        <td>
                          <span className={`badge ${st.kind}`}>{st.label}</span>
                          {dp.error_detail && <div className="tiny" style={{ color: "var(--danger)", marginTop: 4 }}>{dp.error_detail}</div>}
                        </td>
                        <td className="small dim">{fmtTime(dp.started_at)}</td>
                        <td className="small dim">{fmtTime(dp.finished_at)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          <div>
            <div className="eyebrow" style={{ marginBottom: 8 }}>артефакты</div>
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Нода</th><th>SHA-256</th><th>Размер</th></tr></thead>
                <tbody>
                  {detail.artifacts.map((a) => (
                    <tr key={a.node_id}>
                      <td className="mono small">{a.node_name}</td>
                      <td className="hash">{a.sha256}</td>
                      <td className="num small">{a.size_bytes} Б</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}

function Tile({ label, value }: { label: string; value: number }) {
  return (
    <div className="card tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
    </div>
  );
}
