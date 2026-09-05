import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { IconCheck, IconClose, IconCopy, IconWarn } from "./icons";
import { ApiError } from "./api";

/* ---------- toasts ---------- */

type Toast = { id: number; kind: "ok" | "bad" | "warn"; title: string; body?: string };
const ToastCtx = createContext<(t: Omit<Toast, "id">) => void>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastHost({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const push = useCallback((t: Omit<Toast, "id">) => {
    const id = Date.now() + Math.random();
    setItems((v) => [...v, { ...t, id }]);
    setTimeout(() => setItems((v) => v.filter((x) => x.id !== id)), 5000);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" aria-live="polite" aria-atomic="false">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind === "ok" ? "" : t.kind}`}>
            <div style={{ fontWeight: 600 }}>{t.title}</div>
            {t.body && <div className="small muted" style={{ marginTop: 2 }}>{t.body}</div>}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/** Turns an API failure into a message that names the cause and the fix. */
export function errText(e: unknown): string {
  if (e instanceof ApiError) {
    if (Array.isArray(e.details) && e.details.length) {
      const first = e.details.slice(0, 3).map((d: any) => d.name ?? d.value ?? JSON.stringify(d));
      return `${e.message}: ${first.join(", ")}`;
    }
    return e.message;
  }
  if (e instanceof Error) return e.message;
  return "Неизвестная ошибка";
}

/* ---------- data loading ---------- */

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const alive = useRef(true);
  const run = useCallback(async () => {
    setLoading(true);
    try {
      const v = await fn();
      if (alive.current) { setData(v); setError(null); }
    } catch (e) {
      if (alive.current) setError(errText(e));
    } finally {
      if (alive.current) setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(() => {
    alive.current = true;
    run();
    return () => { alive.current = false; };
  }, [run]);
  return { data, error, loading, reload: run };
}

/** Re-runs `fn` on an interval; used for live status views. */
export function usePoll<T>(fn: () => Promise<T>, ms: number, deps: unknown[] = []) {
  const state = useAsync(fn, deps);
  useEffect(() => {
    const t = setInterval(() => { state.reload(); }, ms);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms, state.reload]);
  return state;
}

/* ---------- primitives ---------- */

export function Card({ title, eyebrow, actions, children, tight }: {
  title?: ReactNode; eyebrow?: string; actions?: ReactNode; children: ReactNode; tight?: boolean;
}) {
  return (
    <section className="card">
      {(title || actions) && (
        <header className="card-head">
          <div>
            {eyebrow && <div className="eyebrow" style={{ marginBottom: 4 }}>{eyebrow}</div>}
            {title && <h2>{title}</h2>}
          </div>
          <div className="spacer" />
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      <div className={`card-body${tight ? " tight" : ""}`}>{children}</div>
    </section>
  );
}

export function Badge({ kind = "", children }: { kind?: string; children: ReactNode }) {
  return <span className={`badge ${kind}`}>{children}</span>;
}

const STATUS: Record<string, { kind: string; label: string }> = {
  healthy: { kind: "ok", label: "работает" },
  degraded: { kind: "warn", label: "деградация" },
  unhealthy: { kind: "bad", label: "недоступна" },
  maintenance: { kind: "violet", label: "обслуживание" },
  disabled: { kind: "", label: "отключена" },
  unknown: { kind: "", label: "неизвестно" },
};

export function StatusBadge({ status }: { status: string }) {
  const s = STATUS[status] ?? STATUS.unknown;
  return <Badge kind={s.kind}>{s.label}</Badge>;
}

export function Empty({ title, body, action }: { title: string; body?: string; action?: ReactNode }) {
  return (
    <div className="empty">
      <h3>{title}</h3>
      {body && <p className="muted small" style={{ maxWidth: 460, margin: "0 auto" }}>{body}</p>}
      {action && <div style={{ marginTop: 16 }}>{action}</div>}
    </div>
  );
}

export function Notice({ kind = "info", title, children }: { kind?: string; title?: string; children: ReactNode }) {
  return (
    <div className={`notice ${kind}`}>
      <span className="notice-bar" />
      <div>
        {title && <div className="n-title">{title}</div>}
        <div className="n-body">{children}</div>
      </div>
    </div>
  );
}

export function Field({ label, hint, error, children }: {
  label: string; hint?: string; error?: string; children: ReactNode;
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {error ? <span className="hint" style={{ color: "var(--danger)" }} role="alert">{error}</span>
        : hint ? <span className="hint">{hint}</span> : null}
    </div>
  );
}

/**
 * Segmented radio group for a fixed, non-dynamic set of options — clearer and
 * more tappable than a <select>. Shows the selected option's hint underneath.
 */
export function Segmented<T extends string>({ value, onChange, options, wide, tone }: {
  value: T; onChange: (v: T) => void;
  options: { value: T; label: string; hint?: string }[];
  wide?: boolean; tone?: "direct" | "managed" | "neutral";
}) {
  const sel = options.find((o) => o.value === value);
  return (
    <>
      <div className={`seg${wide ? " wide" : ""}${tone ? ` tone-${tone}` : ""}`} role="radiogroup">
        {options.map((o) => (
          <button key={o.value} type="button" role="radio" aria-checked={value === o.value}
            className={`seg-btn${value === o.value ? " sel" : ""}`}
            onClick={() => onChange(o.value)}>{o.label}</button>
        ))}
      </div>
      {sel?.hint && <div className="seg-hint">{sel.hint}</div>}
    </>
  );
}

export function Modal({ title, onClose, children, footer, wide }: {
  title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean;
}) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", h);
    document.body.style.overflow = "hidden";
    return () => { window.removeEventListener("keydown", h); document.body.style.overflow = ""; };
  }, [onClose]);
  return (
    <div className="overlay" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}
           style={wide ? { width: "min(940px, 100%)" } : undefined}>
        <header className="modal-head">
          <h2>{title}</h2>
          <div className="spacer" />
          <button className="btn ghost icon" onClick={onClose} aria-label="Закрыть"><IconClose /></button>
        </header>
        <div className="modal-body">{children}</div>
        {footer && <footer className="modal-foot">{footer}</footer>}
      </div>
    </div>
  );
}

export function Confirm({ title, body, danger, confirmLabel, onConfirm, onClose }: {
  title: string; body: ReactNode; danger?: boolean; confirmLabel: string;
  onConfirm: () => void | Promise<void>; onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  return (
    <Modal title={title} onClose={onClose} footer={
      <>
        <button className="btn" onClick={onClose} disabled={busy}>Отмена</button>
        <button className={`btn ${danger ? "danger" : "primary"}`} disabled={busy}
          onClick={async () => { setBusy(true); try { await onConfirm(); } finally { setBusy(false); } }}>
          {busy ? <span className="spin" /> : null}{confirmLabel}
        </button>
      </>
    }>
      <div className="small">{body}</div>
    </Modal>
  );
}

// navigator.clipboard существует только в защищённом контексте: по https или
// на localhost. Панель, открытая из локальной сети по адресу вида
// http://192.168.x.x:8080, защищённой не считается, и там остаётся старый
// execCommand — некрасивый, но работающий во всех браузерах.
async function copyText(value: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(value); return true; } catch { /* пробуем запасной путь */ }
  }
  const ta = document.createElement("textarea");
  ta.value = value;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;top:0;left:0;opacity:0";
  document.body.appendChild(ta);
  ta.select();
  ta.setSelectionRange(0, value.length);
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  document.body.removeChild(ta);
  return ok;
}

export function Copyable({ value, label }: { value: string; label?: string }) {
  const [done, setDone] = useState(false);
  const toast = useToast();
  return (
    <button className="btn sm" onClick={async () => {
      if (await copyText(value)) {
        setDone(true); setTimeout(() => setDone(false), 1800);
      } else {
        toast({ kind: "warn", title: "Не удалось скопировать", body: "Выделите текст вручную." });
      }
    }}>
      {done ? <IconCheck /> : <IconCopy />}{label ?? (done ? "Скопировано" : "Копировать")}
    </button>
  );
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="row" style={{ padding: 24, justifyContent: "center", color: "var(--text-2)" }}>
      <span className="spin" />{label ?? "Загрузка…"}
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="notice bad" role="alert">
      <span className="notice-bar" />
      <div style={{ flex: 1 }}>
        <div className="n-title row" style={{ gap: 6 }}><IconWarn />Не удалось загрузить данные</div>
        <div className="n-body">{message}</div>
      </div>
      {onRetry && <button className="btn sm" onClick={onRetry}>Повторить</button>}
    </div>
  );
}
