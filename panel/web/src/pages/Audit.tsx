import { useState } from "react";
import { api, fmtTime, timeTitle } from "../api";
import { Card, ErrorState, Modal, Spinner, useAsync } from "../ui";

type Entry = {
  id: number; actor: string; action: string; object_type: string; object_id: string;
  request_id: string; before: any; after: any; created_at: string;
};

export default function Audit() {
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const list = useAsync<{ items: Entry[] }>(
    () => api(`/audit?limit=200${query ? `&q=${encodeURIComponent(query)}` : ""}`), [query]);
  const [detail, setDetail] = useState<Entry | null>(null);

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">наблюдение</div><h1>Журнал аудита</h1></div>
        <div className="spacer" />
        <form onSubmit={(e) => { e.preventDefault(); setQuery(q); }} className="row" style={{ gap: 6 }}>
          <input className="input" style={{ width: 240 }} placeholder="действие, актор или объект"
            value={q} onChange={(e) => setQ(e.target.value)} aria-label="Поиск по журналу" />
          <button className="btn" type="submit">Найти</button>
        </form>
      </div>

      <p className="small muted" style={{ marginTop: 0 }}>
        Записывается каждое изменение конфигурации. Пароли, токены и секреты заменяются
        на <span className="mono">***redacted***</span> до записи.
      </p>

      <Card tight>
        {list.error ? <ErrorState message={list.error} onRetry={list.reload} />
          : list.loading ? <Spinner />
          : (
            <div className="table-wrap">
              <table className="table">
                <thead><tr><th>Время</th><th>Кто</th><th>Действие</th><th>Объект</th><th>Запрос</th><th /></tr></thead>
                <tbody>
                  {list.data!.items.map((e) => (
                    <tr key={e.id}>
                      <td className="small dim" title={timeTitle(e.created_at)}>{fmtTime(e.created_at)}</td>
                      <td className="small">{e.actor}</td>
                      <td className="mono small">{e.action}</td>
                      <td className="tiny dim mono">{e.object_type}{e.object_id ? ` ${e.object_id.slice(0, 8)}` : ""}</td>
                      <td className="hash">{e.request_id}</td>
                      <td className="actions">
                        {(e.before || e.after) && (
                          <button className="btn sm ghost" onClick={() => setDetail(e)}>Показать</button>
                        )}
                      </td>
                    </tr>
                  ))}
                  {list.data!.items.length === 0 && (
                    <tr><td colSpan={6}><div className="empty" style={{ padding: 28 }}>
                      <h3>Записей нет</h3>
                      <p className="muted small">{query ? "Попробуйте другой запрос." : "Журнал заполнится при первых изменениях."}</p>
                    </div></td></tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
      </Card>

      {detail && (
        <Modal title={detail.action} onClose={() => setDetail(null)} wide
          footer={<button className="btn" onClick={() => setDetail(null)}>Закрыть</button>}>
          <div className="grid g2">
            <div>
              <div className="eyebrow" style={{ marginBottom: 8 }}>до</div>
              <div className="codeblock">{detail.before ? JSON.stringify(detail.before, null, 2) : "—"}</div>
            </div>
            <div>
              <div className="eyebrow" style={{ marginBottom: 8 }}>после</div>
              <div className="codeblock">{detail.after ? JSON.stringify(detail.after, null, 2) : "—"}</div>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}
