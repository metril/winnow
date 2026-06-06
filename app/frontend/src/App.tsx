import { useState } from "react";
import { ThemeProvider } from "./theme";
import { ToastProvider } from "./ui";
import { LiveProvider } from "./live";
import { TopLoadingBar } from "./fetch";
import { AppShell, View } from "./components/shell";
import Overview from "./components/Overview";
import Identify from "./components/Identify";
import Meters from "./components/Meters";
import LoadTests from "./components/LoadTests";
import Devices from "./components/Devices";
import Agents from "./components/Agents";
import Maintenance from "./components/Maintenance";
import Settings from "./components/Settings";
import Utility from "./components/Utility";

export default function App() {
  const [view, setView] = useState<View>("overview");
  return (
    <ThemeProvider>
      <ToastProvider>
        <LiveProvider>
          <TopLoadingBar />
          <AppShell view={view} onNav={setView}>
            {view === "overview" && <Overview onNav={setView} />}
            {view === "identify" && <Identify />}
            {view === "meters" && <Meters />}
            {view === "loadtests" && <LoadTests />}
            {view === "utility" && <Utility onNav={setView} />}
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
