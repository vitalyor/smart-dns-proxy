import { useCallback, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, setCsrf } from "./api";
import { Spinner, useToast } from "./ui";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Nodes from "./pages/Nodes";
import { GroupsPage } from "./pages/Groups";
import Services from "./pages/Services";
import RuleSets from "./pages/RuleSets";
import RuleSetDetail from "./pages/RuleSetDetail";
import Revisions from "./pages/Revisions";
import Users from "./pages/Users";
import SubscriptionPage from "./pages/SubscriptionPage";
import Certificates from "./pages/Certificates";
import Health from "./pages/Health";
import Logs from "./pages/Logs";
import Audit from "./pages/Audit";
import Settings from "./pages/Settings";
import Setup from "./pages/Setup";
import {
  IconGauge, IconServer, IconArrowIn, IconGrid, IconList, IconGlobe,
  IconLayers, IconPhone, IconPulse, IconShield, IconSliders, IconLogout,
  IconMoon, IconSun, IconPlay, IconMenu, IconDesktop,
} from "./icons";

// Тема: три состояния, а не два. «Системная» — значение по умолчанию, потому
// что человек уже выбрал светлое или тёмное на уровне ОС, и панель не должна
// спорить с этим выбором.
type Theme = "system" | "light" | "dark";
const THEMES: { key: Theme; label: string; Icon: (p: { size?: number }) => JSX.Element }[] = [
  { key: "system", label: "Как в системе", Icon: IconDesktop },
  { key: "light", label: "Светлая", Icon: IconSun },
  { key: "dark", label: "Тёмная", Icon: IconMoon },
];

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
  { to: "/groups", label: "Точки входа и выхода", Icon: IconArrowIn },
  { group: "Конфигурация" },
  { to: "/services", label: "Сервисы", Icon: IconGrid },
  // Общие списки живут внутри сервиса; страница /rule-sets остаётся доступной по
  // прямой ссылке для редкого случая «один список на несколько сервисов».
  { to: "/revisions", label: "Конфигурации", Icon: IconLayers },
  { to: "/users", label: "Пользователи", Icon: IconPhone },
  { to: "/subscription-page", label: "Страница подписки", Icon: IconGlobe },
  { to: "/certificates", label: "Сертификат", Icon: IconShield },
  { group: "Наблюдение" },
  { to: "/logs", label: "Логи запросов", Icon: IconList },
  { to: "/health", label: "Здоровье и события", Icon: IconPulse },
  { to: "/audit", label: "Журнал аудита", Icon: IconShield },
  { to: "/settings", label: "Настройки", Icon: IconSliders },
] as const;

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [ready, setReady] = useState(false);
  const [theme, setTheme] = useState<Theme>(
    () => (localStorage.getItem("sdns-theme") as Theme) || "system");
  const [navOpen, setNavOpen] = useState(false);
  const toast = useToast();
  const loc = useLocation();

  useEffect(() => {
    // Системную тему выражаем отсутствием атрибута: дальше решает CSS по
    // prefers-color-scheme, без слушателей и перерисовок.
    if (theme === "system") delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = theme;
    localStorage.setItem("sdns-theme", theme);
  }, [theme]);

  // Переход по ссылке на телефоне обязан закрывать выехавшую навигацию.
  useEffect(() => { setNavOpen(false); }, [loc.pathname]);

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
    <div className={`shell${navOpen ? " nav-open" : ""}`}>
      <div className="nav-scrim" onClick={() => setNavOpen(false)} aria-hidden="true" />
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

        <div className="nav-list">
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
        </div>

        <div className="rail-foot">
          <div className="small" style={{ fontWeight: 550 }}>{me.user.email}</div>
          <div className="tiny dim" style={{ marginBottom: 10 }}>роль: {me.user.role} · v{me.version}</div>
          <div className="seg wide tone-neutral" role="group" aria-label="Тема оформления"
            style={{ marginBottom: 8 }}>
            {THEMES.map((t) => (
              <button key={t.key} className={`seg-btn${theme === t.key ? " sel" : ""}`}
                onClick={() => setTheme(t.key)} title={t.label} aria-label={t.label}
                aria-pressed={theme === t.key}>
                <t.Icon />
              </button>
            ))}
          </div>
          <button className="btn sm ghost" style={{ width: "100%" }} onClick={signOut}>
            <IconLogout />Выйти
          </button>
        </div>
      </nav>

      <main className="main">
        <header className="topbar">
          <button className="btn ghost icon nav-toggle" onClick={() => setNavOpen(true)}
            aria-label="Открыть навигацию" aria-expanded={navOpen}><IconMenu /></button>
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
            <Route path="/groups" element={<GroupsPage />} />
            <Route path="/ingress-groups" element={<Navigate to="/groups" replace />} />
            <Route path="/egress-groups" element={<Navigate to="/groups" replace />} />
            <Route path="/services" element={<Services />} />
            <Route path="/rule-sets" element={<RuleSets />} />
            <Route path="/rule-sets/:id" element={<RuleSetDetail />} />
            <Route path="/revisions" element={<Revisions />} />
            <Route path="/users" element={<Users />} />
            <Route path="/subscription-page" element={<SubscriptionPage me={me} />} />
            {/* старые адреса из закладок */}
            <Route path="/devices" element={<Navigate to="/users" replace />} />
            <Route path="/subscribers" element={<Navigate to="/users" replace />} />
            <Route path="/instructions" element={<Navigate to="/subscription-page" replace />} />
            <Route path="/certificates" element={<Certificates />} />
            <Route path="/logs" element={<Logs />} />
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
