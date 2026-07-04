import { ThemeProvider } from "./theme";
import { ToastProvider } from "./ui";
import { LiveProvider } from "./live";
import { TopLoadingBar } from "./fetch";
import { AppShell } from "./components/shell";
import { useHashRoute } from "./route";
import Overview from "./components/Overview";
import Identify from "./components/Identify";
import Meters from "./components/Meters";
import LoadTests from "./components/LoadTests";
import Devices from "./components/Devices";
import Agents from "./components/Agents";
import Maintenance from "./components/Maintenance";
import Settings from "./components/Settings";
import Utility from "./components/Utility";
import Usage from "./components/Usage";

export default function App() {
  const [route, nav] = useHashRoute();
  const { view, params } = route;
  return (
    <ThemeProvider>
      <ToastProvider>
        <LiveProvider>
          <TopLoadingBar />
          <AppShell view={view} onNav={nav}>
            {view === "overview" && <Overview onNav={nav} />}
            {view === "usage" && <Usage params={params} onNav={nav} />}
            {view === "identify" && <Identify onNav={nav} />}
            {view === "meters" && <Meters initialDetail={params[0] ? Number(params[0]) : null} />}
            {view === "loadtests" && <LoadTests />}
            {view === "utility" && <Utility onNav={nav} />}
            {view === "devices" && <Devices />}
            {view === "agents" && <Agents />}
            {view === "maintenance" && <Maintenance />}
            {view === "settings" && <Settings />}
          </AppShell>
        </LiveProvider>
      </ToastProvider>
    </ThemeProvider>
  );
}
