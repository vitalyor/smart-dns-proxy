import { useState } from "react";
import { api, setCsrf } from "../api";
import { Field, errText } from "../ui";

export default function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [needTotp, setNeedTotp] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api<{ csrf_token: string }>("/auth/login", {
        method: "POST",
        body: { email, password, totp: totp || undefined },
      });
      setCsrf(r.csrf_token);
      onSignedIn();
    } catch (err: any) {
      if (err?.code === "totp_required") {
        setNeedTotp(true);
        setError("Введите код из приложения-аутентификатора.");
      } else {
        setError(errText(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login" onSubmit={submit}>
        <div className="eyebrow">smartdns control plane</div>
        <h1 style={{ marginTop: 8 }}>Вход в панель</h1>

        {/* The product's thesis, drawn once on the way in. */}
        <div className="login-flow" aria-hidden="true">
          <div className="hop" style={{ minWidth: 0, padding: "6px 10px" }}>
            <span className="hop-name">устройство</span>
          </div>
          <span className="wire managed live" style={{ width: 34 }} />
          <div className="hop" style={{ minWidth: 0, padding: "6px 10px" }}>
            <span className="hop-name">ingress</span>
          </div>
          <span className="wire managed live" style={{ width: 34 }} />
          <div className="hop" style={{ minWidth: 0, padding: "6px 10px" }}>
            <span className="hop-name">egress</span>
          </div>
        </div>

        <div className="col" style={{ gap: 14 }}>
          <Field label="Email">
            <input className="input" type="email" autoComplete="username" required autoFocus
              value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field label="Пароль">
            <input className="input" type="password" autoComplete="current-password" required
              value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          {needTotp && (
            <Field label="Код подтверждения" hint="Шесть цифр из приложения-аутентификатора">
              <input className="input mono" inputMode="numeric" pattern="[0-9]*" maxLength={6}
                autoComplete="one-time-code" value={totp} onChange={(e) => setTotp(e.target.value)} />
            </Field>
          )}
          {error && (
            <div className="notice bad" role="alert">
              <span className="notice-bar" />
              <div className="n-body">{error}</div>
            </div>
          )}
          <button className="btn primary" type="submit" disabled={busy} style={{ width: "100%" }}>
            {busy ? <span className="spin" /> : null}Войти
          </button>
        </div>

        <p className="tiny dim" style={{ marginTop: 18, marginBottom: 0 }}>
          Пароль владельца печатается один раз в журнале panel-api при первом запуске.
        </p>
      </form>
    </div>
  );
}
