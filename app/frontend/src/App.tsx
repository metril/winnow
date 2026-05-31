import { useState } from "react";
import Health from "./components/Health";
import Meters from "./components/Meters";
import LoadTest from "./components/LoadTest";

type View = "meters" | "loadtest";

export default function App() {
  const [view, setView] = useState<View>("meters");
  return (
    <>
      <header className="topbar">
        <h1>⚡ meterfinder</h1>
        <nav>
          <button className={view === "meters" ? "active" : ""} onClick={() => setView("meters")}>
            Meters
          </button>
          <button className={view === "loadtest" ? "active" : ""} onClick={() => setView("loadtest")}>
            Load Test
          </button>
        </nav>
      </header>
      <main>
        <Health />
        {view === "meters" ? <Meters /> : <LoadTest />}
      </main>
    </>
  );
}
