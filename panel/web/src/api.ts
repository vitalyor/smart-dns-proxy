// Thin API client. The UI talks to the same public /api/v1 surface as any
// other client; there is no private back channel.

export class ApiError extends Error {
  code: string;
  status: number;
  details: unknown;
  requestId: string;
  constructor(status: number, body: any) {
    super(body?.message || `HTTP ${status}`);
    this.code = body?.code || "http_error";
    this.status = status;
    this.details = body?.details;
    this.requestId = body?.request_id || "";
  }
}

let csrf = "";
export const setCsrf = (t: string) => { csrf = t; };

type Opts = { method?: string; body?: unknown; headers?: Record<string, string> };

export async function api<T = any>(path: string, opts: Opts = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { ...(opts.headers ?? {}) };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && method !== "HEAD") headers["X-CSRF-Token"] = csrf;

  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  });

  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = undefined;
  try { data = text ? JSON.parse(text) : undefined; } catch { data = { message: text }; }
  if (!res.ok) throw new ApiError(res.status, data);
  return data as T;
}

export const download = (path: string) => {
  window.location.href = `/api/v1${path}`;
};

/** POST that returns a file, then saves it using the server's filename. */
export async function apiDownload(path: string, body?: unknown): Promise<void> {
  const res = await fetch(`/api/v1${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    credentials: "same-origin",
    body: JSON.stringify(body ?? {}),
  });
  if (!res.ok) {
    const t = await res.text();
    let d: any; try { d = JSON.parse(t); } catch { d = { message: t }; }
    throw new ApiError(res.status, d);
  }
  const blob = await res.blob();
  const cd = res.headers.get("Content-Disposition") || "";
  const m = cd.match(/filename="?([^"]+)"?/);
  const name = m ? m[1] : "smartdns-backup.tar";
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  URL.revokeObjectURL(url);
}

/** Multipart POST for file uploads (restore). */
export async function apiUpload<T = any>(path: string, form: FormData): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrf },
    credentials: "same-origin",
    body: form,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any; try { data = text ? JSON.parse(text) : undefined; } catch { data = { message: text }; }
  if (!res.ok) throw new ApiError(res.status, data);
  return data as T;
}

// --- formatting helpers -----------------------------------------------------

const RU = new Intl.DateTimeFormat("ru-RU", {
  day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit",
});

export function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return RU.format(d);
}

/** Local time for reading, UTC in the tooltip — both are always available. */
export function timeTitle(iso?: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `UTC: ${d.toISOString().replace("T", " ").slice(0, 19)}`;
}

export function ago(iso?: string | null): string {
  if (!iso) return "никогда";
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return "только что";
  if (s < 60) return `${s} с назад`;
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`;
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`;
  return `${Math.floor(s / 86400)} дн назад`;
}

export function plural(n: number, one: string, few: string, many: string): string {
  const m10 = n % 10, m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return `${n} ${one}`;
  if (m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20)) return `${n} ${few}`;
  return `${n} ${many}`;
}

export const shortHash = (h?: string | null) => (h ? h.slice(0, 12) : "—");
