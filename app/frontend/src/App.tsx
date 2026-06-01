import { useState } from "react";
import { ToastProvider } from "./ui";
import { LiveProvider } from "./live";
import { TopLoadingBar } from "./fetch";
import { AppShell, View } from "./components/shell";
import Overview from "./components/Overview";
import Identify from "./components/Identify";
import Meters from "./components/Meters";
import LoadTests from "./components/LoadTests";
import Devices from "./components/Devices";
import Settings from "./components/Settings";

export default function App() {
  const [view, setView] = useState<View>("overview");
  return (
    <ToastProvider>
      <LiveProvider>
        <TopLoadingBar />
        <AppShell view={view} onNav={setView}>
          {view === "overview" && <Overview onNav={setView} />}
          {view === "identify" && <Identify />}
          {view === "meters" && <Meters />}
          {view === "loadtests" && <LoadTests />}
          {view === "devices" && <Devices />}
          {view === "settings" && <Settings />}
        </AppShell>
      </LiveProvider>
    </ToastProvider>
  );
}
