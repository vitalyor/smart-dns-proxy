import { useCallback, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, setCsrf } from "./api";
import { Spinner, useToast } from "./ui";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Nodes from "./pages/Nodes";
import Groups from "./pages/Groups";
import Services from "./pages/Services";
import RuleSets from "./pages/RuleSets";
import RuleSetDetail from "./pages/RuleSetDetail";
import Revisions from "./pages/Revisions";
import Devices from "./pages/Devices";
import Health from "./pages/Health";
import Audit from "./pages/Audit";
import Settings from "./pages/Settings";
import Setup from "./pages/Setup";
import {
  IconGauge, IconServer, IconArrowIn, IconArrowOut, IconGrid, IconList,
  IconLayers, IconPhone, IconPulse, IconShield, IconSliders, IconLogout,
  IconMoon, IconSun, IconPlay,
} from "./icons";

export type Me = {
  user: { id: string; email: string; role: string; display_name: string; totp_enabled: boolean };
  csrf_token: string;
  lab_mode: boolean;
  version: string;
};

const NAV = [
  { to: "/", label: "Обзор", Icon: IconGauge, end: true },
  { to: "/setup", label: "Быстрый старт", Icon: IconPlay },
  { group: "Инфраструктура" },
  { to: "/nodes", label: "Ноды", Icon: IconServer },
  { to: "/ingress-groups", label: "Точки входа", Icon: IconArrowIn },
  { to: "/egress-groups", label: "Точки выхода", Icon: IconArrowOut },
  { group: "Конфигурация" },
  { to: "/services", label: "Сервисы", Icon: IconGrid },
  { to: "/rule-sets", label: "Общие списки", Icon: IconList },
  { to: "/revisions", label: "Конфигурации", Icon: IconLayers },
  { to: "/devices", label: "Устройства", Icon: IconPhone },
  { group: "Наблюдение" },
  { to: "/health", label: "Здоровье и события", Icon: IconPulse },
  { to: "/audit", label: "Журнал аудита", Icon: IconShield },
  { to: "/settings", label: "Настройки", Icon: IconSliders },
] as const;

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [ready, setReady] = useState(false);
  const [theme, setTheme] = useState(() => localStorage.getItem("sdns-theme") ?? "dark");
  const toast = useToast();
  const loc = useLocation();

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("sdns-theme", theme);
  }, [theme]);

  const load = useCallback(async () => {
    try {
      const v = await api<Me>("/auth/me");
      setCsrf(v.csrf_token);
      setMe(v);
    } catch {
      setMe(null);
    } finally {
      setReady(true);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  if (!ready) return <div className="login-wrap"><Spinner /></div>;
  if (!me) return <Login onSignedIn={load} />;

  const signOut = async () => {
    try { await api("/auth/logout", { method: "POST" }); } catch { /* the session is gone either way */ }
    setMe(null);
    toast({ kind: "ok", title: "Вы вышли из панели" });
  };

  return (
    <div className="shell">
      <nav className="rail" aria-label="Основная навигация">
        <div className="brand">
          <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="var(--surface-2)" stroke="var(--line)" />
            <path d="M5 16h7" stroke="var(--direct)" strokeWidth="2" strokeLinecap="round" />
            <path d="M20 16h7" stroke="var(--managed)" strokeWidth="2" strokeLinecap="round" />
            <circle cx="16" cy="16" r="3.5" fill="var(--managed)" />
          </svg>
          <div>
            <div className="brand-name">SmartDNS</div>
            <div className="brand-sub">control plane</div>
          </div>
        </div>

        {NAV.map((item, i) =>
          "group" in item ? (
            <div key={i} className="nav-group eyebrow">{item.group}</div>
          ) : (
            <NavLink key={item.to} to={item.to} end={"end" in item ? item.end : false}
              className={({ isActive }) => `nav-item${isActive ? " active" : ""}`}>
              <span className="nav-dot" />
              <item.Icon />
              {item.label}
            </NavLink>
          )
        )}

        <div className="rail-foot">
          <div className="small" style={{ fontWeight: 550 }}>{me.user.email}</div>
          <div className="tiny dim" style={{ marginBottom: 10 }}>роль: {me.user.role} · v{me.version}</div>
          <div className="row" style={{ gap: 6 }}>
            <button className="btn sm ghost" onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
              aria-label={theme === "dark" ? "Светлая тема" : "Тёмная тема"}>
              {theme === "dark" ? <IconSun /> : <IconMoon />}
              {theme === "dark" ? "Светлая" : "Тёмная"}
            </button>
            <button className="btn sm ghost" onClick={signOut}><IconLogout />Выйти</button>
          </div>
        </div>
      </nav>

      <main className="main">
        <header className="topbar">
          <div className="eyebrow">{titleOf(loc.pathname)}</div>
          <div className="spacer" />
          {me.lab_mode && (
            <span className="badge warn" title="Серверы выхода могут обращаться к приватным адресам. Только для стенда.">
              лабораторный режим
            </span>
          )}
        </header>

        <div className="page">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/setup" element={<Setup />} />
            <Route path="/nodes" element={<Nodes />} />
            <Route path="/ingress-groups" element={<Groups kind="ingress" />} />
            <Route path="/egress-groups" element={<Groups kind="egress" />} />
            <Route path="/services" element={<Services />} />
            <Route path="/rule-sets" element={<RuleSets />} />
            <Route path="/rule-sets/:id" element={<RuleSetDetail />} />
            <Route path="/revisions" element={<Revisions />} />
            <Route path="/devices" element={<Devices />} />
            <Route path="/health" element={<Health />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/settings" element={<Settings me={me} onChanged={load} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </main>
    </div>
  );
}

function titleOf(path: string): string {
  const hit = NAV.find((n) => "to" in n && (n.to === path || (n.to !== "/" && path.startsWith(n.to))));
  return hit && "label" in hit ? hit.label : "Обзор";
}
