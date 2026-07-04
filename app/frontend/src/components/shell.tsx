import { ReactNode, useState } from "react";
import {
  LayoutDashboard, Crosshair, Zap, Gauge, RadioTower, Settings as SettingsIcon,
  ChevronLeft, ChevronRight, Radio, Database, Satellite, Receipt, BarChart3,
} from "lucide-react";
import { api } from "../api";
import { useLive, perMin } from "../live";
import { useFetch } from "../fetch";
import { ThemeToggle } from "../theme";
import { cx, Dot } from "../ui";
import { fmt } from "../util";

export type View = "overview" | "usage" | "identify" | "meters" | "loadtests" | "devices" | "agents" | "maintenance" | "settings" | "utility";

const GROUPS: { label: string | null; items: { id: View; label: string; icon: any }[] }[] = [
  { label: null, items: [{ id: "overview", label: "Overview", icon: LayoutDashboard }, { id: "usage", label: "Usage", icon: BarChart3 }] },
  { label: "Find my meter", items: [{ id: "identify", label: "Identify", icon: Crosshair }, { id: "loadtests", label: "Load tests", icon: Zap }] },
  { label: "Inventory", items: [{ id: "meters", label: "Meters", icon: Gauge }, { id: "devices", label: "Devices", icon: RadioTower }] },
  { label: "Billing", items: [{ id: "utility", label: "Utility bill", icon: Receipt }] },
  { label: "System", items: [{ id: "agents", label: "Remote agents", icon: Satellite }, { id: "maintenance", label: "Maintenance", icon: Database }] },
];

export function AppShell({ view, onNav, children }: { view: View; onNav: (v: View) => void; children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);
  return (
    <div className="grid min-h-screen" style={{ gridTemplateColumns: `${collapsed ? 64 : 240}px 1fr` }}>
      <Sidebar view={view} onNav={onNav} collapsed={collapsed} onToggle={() => setCollapsed((c) => !c)} />
      <div className="flex min-w-0 flex-col">{children}</div>
    </div>
  );
}

function Sidebar({ view, onNav, collapsed, onToggle }:
  { view: View; onNav: (v: View) => void; collapsed: boolean; onToggle: () => void }) {
  return (
    <aside className="sticky top-0 flex h-screen flex-col border-r border-border bg-sidebar">
      <div className="flex items-center gap-2.5 px-4 py-4">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-brand/15 text-brand"><Radio size={18} /></span>
        {!collapsed && <span className="text-h3 tracking-tight">winnow</span>}
      </div>

      <nav className="flex-1 overflow-y-auto px-2.5 py-2">
        {GROUPS.map((g, gi) => (
          <div key={gi} className="mb-3">
            {g.label && !collapsed && <div className="px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-tertiary">{g.label}</div>}
            {g.items.map((it) => <NavItem key={it.id} item={it} active={view === it.id} onClick={() => onNav(it.id)} collapsed={collapsed} />)}
          </div>
        ))}
      </nav>

      <div className="border-t border-border px-2.5 py-2">
        <NavItem item={{ label: "Settings", icon: SettingsIcon }} active={view === "settings"} onClick={() => onNav("settings")} collapsed={collapsed} />
        {!collapsed && <div className="px-1 pt-1.5"><ThemeToggle className="w-full justify-center" /></div>}
      </div>

      {!collapsed && <StatusRail />}

      <button onClick={onToggle} className="m-2.5 flex items-center justify-center rounded-lg border border-border py-1.5 text-tertiary hover:text-secondary">
        {collapsed ? <ChevronRight size={16} /> : <ChevronLeft size={16} />}
      </button>
    </aside>
  );
}

function NavItem({ item, active, onClick, collapsed }:
  { item: { label: string; icon: any }; active: boolean; onClick: () => void; collapsed: boolean }) {
  const Icon = item.icon;
  return (
    <button onClick={onClick} title={collapsed ? item.label : undefined}
      className={cx("relative flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-small transition",
        active ? "bg-brand/10 text-text" : "text-secondary hover:bg-raised hover:text-text", collapsed && "justify-center")}>
      {active && <span className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-brand" />}
      <Icon size={17} className={active ? "text-brand" : ""} />
      {!collapsed && <span className="truncate">{item.label}</span>}
    </button>
  );
}

function StatusRail() {
  const { readings, configVersion, connectedAt } = useLive();
  const health = useFetch(api.health, [configVersion]);
  const status = useFetch(api.status, [configVersion]);
  // Floor the live SSE rate with the server's last-minute count for the first
  // minute after connecting, so a dashboard refresh (empty SSE buffer) shows the
  // real rate immediately instead of ramping up from ~1.
  const warming = Date.now() - connectedAt < 60_000;
  const rate = warming ? Math.max(perMin(readings), health.data?.packets_last_min ?? 0) : perMin(readings);
  const capAlive = rate > 0 || (health.data?.alive ?? false);
  const row = (tone: "good" | "bad" | "warn" | "off", label: string, value: ReactNode) => (
    <div className="flex items-center gap-2 px-2 py-1 text-micro">
      <Dot tone={tone} /><span className="text-secondary">{label}</span><span className="ml-auto tabular-nums text-tertiary">{value}</span>
    </div>
  );
  return (
    <div className="mx-2.5 mb-1 rounded-lg border border-border bg-surface/60 p-1">
      {row(capAlive ? "good" : "bad", "Capture", `${rate}/min`)}
      {row(status.data?.ha_ok ? "good" : "off", "HA", status.data?.ha_ok ? "ok" : "—")}
      {/* reachable-but-not-connected is a warn: the worker's session is what
          actually publishes, a TCP dial succeeding means nothing by itself */}
      {row(status.data?.mqtt_connected ? "good" : status.data?.mqtt_ok ? "warn" : "off", "MQTT",
        status.data?.mqtt_connected ? "ok" : status.data?.mqtt_ok ? "no session" : "—")}
      {row("off", "Meters seen", fmt(health.data?.unique_meters ?? 0))}
    </div>
  );
}

export function Page({ title, breadcrumb, actions, children }:
  { title: ReactNode; breadcrumb?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <>
      <header className="sticky top-0 z-20 flex flex-wrap items-center gap-3 border-b border-border bg-app/80 px-6 py-3.5 backdrop-blur-md">
        <div className="min-w-0">
          {breadcrumb && <div className="text-micro text-tertiary">{breadcrumb}</div>}
          <h1 className="text-h1 text-text">{title}</h1>
        </div>
        {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
      </header>
      <main className="mx-auto w-full max-w-[1400px] space-y-6 px-6 py-6">{children}</main>
    </>
  );
}
