import { useState } from "react";
import { api, ago, timeTitle } from "../api";
import { Card, ErrorState, Field, Notice, Spinner, errText, useAsync, useToast } from "../ui";
import { IconShield, IconKey } from "../icons";

type Cert = { domains: string[]; not_after: string; staging: boolean; updated_at: string; days_left: number };
type NodeCert = { name: string; status: string; state: string; days_left: number };
type Resp = { domains_needed: string[]; cloudflare_ready: boolean; certificate?: Cert; nodes?: NodeCert[] };

function certState(s: string) {
  switch (s) {
    case "current": return <span className="badge ok">актуальный</span>;
    case "stale": return <span className="badge warn">старый, будет дослан</span>;
    case "no_certificate": return <span className="badge">панель ещё не выпускала</span>;
    default: return <span className="badge">нода не сообщает</span>;
  }
}

export default function Certificates() {
  const st = useAsync<Resp>(() => api("/certificates"), []);
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [staging, setStaging] = useState(false);
  const [busy, setBusy] = useState("");
  const toast = useToast();

  if (st.loading) return <Spinner />;
  if (st.error) return <ErrorState message={st.error} onRetry={st.reload} />;
  const d = st.data!;

  const saveToken = async () => {
    setBusy("token");
    try {
      await api("/certificates/cloudflare-token", { method: "PUT", body: { token: token.trim() } });
      setToken("");
      toast({ kind: "ok", title: "Токен сохранён и проверен" });
      st.reload();
    } catch (e) { toast({ kind: "bad", title: "Токен не принят", body: errText(e) }); }
    finally { setBusy(""); }
  };

  const issue = async () => {
    setBusy("issue");
    try {
      const r = await api<{ domains: string[]; not_after: string; nodes_updated: number }>(
        "/certificates/issue", { method: "POST", body: { email: email.trim(), staging } });
      toast({ kind: "ok", title: "Сертификат выпущен",
        body: `Действует до ${new Date(r.not_after).toLocaleDateString("ru-RU")}, разослан на узлы: ${r.nodes_updated}` });
      st.reload();
    } catch (e) { toast({ kind: "bad", title: "Не удалось выпустить", body: errText(e) }); }
    finally { setBusy(""); }
  };

  return (
    <>
      <div className="row">
        <div><div className="eyebrow">доступ</div><h1>Сертификат резолвера</h1></div>
      </div>

      <Notice kind="info" title="Один wildcard на весь флот">
        Панель выпускает сертификат на имя резолвера и <code className="mono">*.</code>-поддомены
        через проверку DNS в Cloudflare и сама рассылает его на входные ноды. Wildcard нужен, чтобы у
        Android заработал персональный DoT-адрес <code className="mono">&lt;токен&gt;.имя</code>. Токен
        Cloudflare остаётся в панели и на ноды не попадает.
      </Notice>

      <div className="grid g2">
        <Card title="Что будет в сертификате" eyebrow="из имён резолвера">
          {d.domains_needed.length ? (
            <ul className="mono small" style={{ margin: 0, paddingLeft: 18 }}>
              {d.domains_needed.map((x) => <li key={x}>{x}</li>)}
            </ul>
          ) : (
            <Notice kind="warn" title="Сначала задайте имя DoH/DoT">
              Домены берутся из имён резолвера в Настройках. Пока они пусты, выпускать нечего.
            </Notice>
          )}
          {d.certificate && (
            <dl className="kv" style={{ marginTop: 14 }}>
              <dt>Текущий</dt>
              <dd>
                {d.certificate.staging && <span className="badge warn" style={{ marginRight: 6 }}>staging</span>}
                действует ещё {d.certificate.days_left} дн
                <div className="tiny dim" title={timeTitle(d.certificate.not_after)}>
                  до {new Date(d.certificate.not_after).toLocaleDateString("ru-RU")} · обновлён {ago(d.certificate.updated_at)}
                </div>
              </dd>
            </dl>
          )}
        </Card>

        <Card title="Токен Cloudflare" eyebrow="права: DNS Edit + Zone Read">
          {d.cloudflare_ready
            ? <Notice kind="info" title="Токен сохранён">Он проверен и готов к выпуску. Можно вставить новый, чтобы заменить.</Notice>
            : <Notice kind="warn" title="Токен не задан">Без него выпуск невозможен.</Notice>}
          <Field label="API-токен" hint="Проверяется при сохранении. Показывается один раз — Cloudflare его потом не отдаёт.">
            <input className="input mono" type="password" value={token} placeholder="cf..."
              onChange={(e) => setToken(e.target.value)} />
          </Field>
          <button className="btn" onClick={saveToken} disabled={busy !== "" || !token.trim()}>
            {busy === "token" ? <span className="spin" /> : <IconKey />}Сохранить токен
          </button>
        </Card>
      </div>

      <Card title="Что отдают ноды" eyebrow="не то, что лежит в базе, а то, что видит клиент">
        {d.nodes?.length ? (
          <div className="table-wrap">
            <table className="table">
              <thead><tr><th>Нода</th><th style={{ width: 200 }}>Сертификат</th><th style={{ width: 120 }}>Осталось</th></tr></thead>
              <tbody>
                {d.nodes.map((n) => (
                  <tr key={n.name}>
                    <td>{n.name}</td>
                    <td>{certState(n.state)}</td>
                    <td className="num small dim">{n.days_left > 0 ? `${n.days_left} дн` : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : <p className="muted small">Входных нод нет.</p>}
        <p className="muted small" style={{ marginTop: 10 }}>
          Отпечаток берётся у самого слушателя DoH/DoT. Если нода отдаёт не тот сертификат,
          панель дошлёт его при ближайшем опросе — без нового обращения к Let’s Encrypt.
        </p>
      </Card>

      <Card title="Выпуск" eyebrow="и рассылка на ноды">
        <div className="grid g2">
          <Field label="Email для Let’s Encrypt" hint="Необязательно — туда придут напоминания.">
            <input className="input mono" value={email} placeholder="you@example.com"
              onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field label="Режим">
            <label className="check" style={{ marginTop: 8 }}>
              <input type="checkbox" checked={staging} onChange={(e) => setStaging(e.target.checked)} />
              Тестовый (staging) — без лимитов, браузеры не доверяют
            </label>
          </Field>
        </div>
        <button className="btn primary" style={{ marginTop: 12 }} onClick={issue}
          disabled={busy !== "" || !d.cloudflare_ready || d.domains_needed.length === 0}>
          {busy === "issue" ? <span className="spin" /> : <IconShield />}Выпустить и разослать
        </button>
        <p className="muted small" style={{ marginTop: 10 }}>
          Выпуск занимает до пары минут — панель создаёт запись в DNS, ждёт её распространения и
          проходит проверку. Продление происходит само за 30 дней до истечения.
        </p>
      </Card>
    </>
  );
}
