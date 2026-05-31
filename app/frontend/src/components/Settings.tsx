import { useEffect, useState } from "react";
import { api, PowerEntity, Status } from "../api";
import { fmt } from "../util";

// Settings: HA + MQTT connection, and the "monitored consumption" set that winnow
// uses as ground truth (a single HA aggregate sensor, or several winnow sums).
export default function Settings() {
  const [s, setS] = useState<Record<string, any>>({});
  const [test, setTest] = useState<any>(null);
  const [saved, setSaved] = useState(false);

  const load = async () => setS(await api.settings());
  useEffect(() => { load(); }, []);

  const set = (k: string, v: string) => setS((p) => ({ ...p, [k]: v }));
  const field = (k: string) => s[k] ?? "";
  const secretPlaceholder = (k: string) => (s[k + "_set"] ? "•••••• (set — leave blank to keep)" : "");

  const save = async () => {
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass", "mqtt_prefix",
     "threshold_w", "default_multiplier", "default_unit"].forEach((k) => {
      if (s[k] !== undefined && s[k] !== "") body[k] = String(s[k]);
    });
    await api.putSettings(body);
    setSaved(true); setTimeout(() => setSaved(false), 1500);
    load();
  };

  const runTest = async () => {
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass"].forEach((k) => {
      if (s[k]) body[k] = String(s[k]);
    });
    setTest(await api.testIntegrations(body));
  };

  return (
    <div>
      <div className="panel">
        <h2>Home Assistant</h2>
        <div className="form">
          <label>Base URL</label>
          <input type="text" value={field("ha_url")} onChange={(e) => set("ha_url", e.target.value)} placeholder="https://ha.example.com" />
          <label>Long-lived token</label>
          <input type="password" value={field("ha_token")} onChange={(e) => set("ha_token", e.target.value)} placeholder={secretPlaceholder("ha_token")} />
        </div>
      </div>

      <div className="panel">
        <h2>MQTT broker (EMQX)</h2>
        <div className="form">
          <label>Host</label>
          <input type="text" value={field("mqtt_host")} onChange={(e) => set("mqtt_host", e.target.value)} placeholder="ha.example.com" />
          <label>Port</label>
          <input type="text" value={field("mqtt_port")} onChange={(e) => set("mqtt_port", e.target.value)} placeholder="1883" />
          <label>Username</label>
          <input type="text" value={field("mqtt_user")} onChange={(e) => set("mqtt_user", e.target.value)} />
          <label>Password</label>
          <input type="password" value={field("mqtt_pass")} onChange={(e) => set("mqtt_pass", e.target.value)} placeholder={secretPlaceholder("mqtt_pass")} />
          <label>Discovery prefix</label>
          <input type="text" value={field("mqtt_prefix")} onChange={(e) => set("mqtt_prefix", e.target.value)} placeholder="homeassistant" />
        </div>
      </div>

      <MonitoredConsumption />

      <div className="panel">
        <h2>Tuning</h2>
        <div className="form">
          <label>Load-on threshold (W)</label>
          <input type="text" value={field("threshold_w")} onChange={(e) => set("threshold_w", e.target.value)} placeholder="50" />
          <label>Default multiplier</label>
          <input type="text" value={field("default_multiplier")} onChange={(e) => set("default_multiplier", e.target.value)} placeholder="1" />
          <label>Default unit</label>
          <input type="text" value={field("default_unit")} onChange={(e) => set("default_unit", e.target.value)} placeholder="kWh" />
        </div>
      </div>

      <div className="controls">
        <button className="btn" onClick={save}>{saved ? "Saved ✓" : "Save settings"}</button>
        <button className="btn alt" onClick={runTest}>Test connections</button>
        {test && (
          <>
            <span className="badge"><span className={"dot " + (test.ha?.ok ? "ok" : "bad")} /> HA {test.ha?.ok ? "ok" : test.ha?.error}</span>
            <span className="badge"><span className={"dot " + (test.mqtt?.ok ? "ok" : "bad")} /> MQTT {test.mqtt?.ok ? "ok" : test.mqtt?.error}</span>
          </>
        )}
      </div>
    </div>
  );
}

// MonitoredConsumption: the set of HA sensors whose summed power is the ground
// truth. Pick one pre-aggregated sensor, or several that winnow sums / that
// winnow turns into a HA Group(sum) helper.
function MonitoredConsumption() {
  const [entities, setEntities] = useState<PowerEntity[]>([]);
  const [status, setStatus] = useState<Status | null>(null);
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const loadStatus = async () => {
    const st = await api.status();
    setStatus(st);
    setSel(new Set(st.monitored_entities || []));
  };
  useEffect(() => { loadStatus(); }, []);

  const loadEntities = async () => {
    try { setEntities(await api.powerEntities()); }
    catch (e) { setMsg(String(e)); }
  };

  const toggle = (id: string) => setSel((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });

  const useSelected = async () => {
    await api.putSettings({ monitored_entities: JSON.stringify([...sel]) });
    setMsg(`winnow will sum ${sel.size} sensor(s).`); loadStatus();
  };
  const createHelper = async () => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.createHelper("winnow monitored power", [...sel]);
      if (r.ok) { setMsg(`Created ${r.entity_id} in HA and selected it.`); loadStatus(); }
      else setMsg(`Couldn't auto-create the helper (${r.error}). Create a Group/Min-Max "sum" helper in HA → Settings → Devices & Services → Helpers, then pick it here.`);
    } catch (e) { setMsg(String(e)); }
    setBusy(false);
  };

  return (
    <div className="panel">
      <h2>Monitored consumption (ground truth)</h2>
      <p className="muted">
        winnow identifies your meter by how well it tracks the <strong>sum of your
        power-monitored devices</strong> — and your meter can never draw below that
        sum's floor. Point it at one pre-aggregated HA sensor (a Utility Meter /
        energy sensor, or a Group "sum" of device power), or select several below.
      </p>
      <div className="controls">
        <button className="btn alt" onClick={loadEntities}>Load HA sensors</button>
        {status && (
          <>
            <span className="badge">{status.monitored_entities?.length || 0} selected</span>
            <span className="badge">floor ≈ {fmt(status.monitored_floor_w)} W</span>
          </>
        )}
        {sel.size > 0 && <span className="muted">{sel.size} checked</span>}
      </div>

      {entities.length > 0 && (
        <>
          <div className="controls">
            <button className="btn alt" onClick={() => setSel(new Set(entities.map((e) => e.entity_id)))}>select all</button>
            <button className="btn alt" onClick={() => setSel(new Set())}>clear</button>
          </div>
          <div className="panel" style={{ background: "#1e2530", maxHeight: 260, overflowY: "auto" }}>
            {entities.map((e) => (
              <label key={e.entity_id} style={{ display: "block", padding: "2px 0" }}>
                <input type="checkbox" checked={sel.has(e.entity_id)} onChange={() => toggle(e.entity_id)} />{" "}
                {e.name} <span className="muted">{e.entity_id}</span>{" "}
                <span className={"chip " + (e.kind === "energy" ? "" : "electric")}>{e.kind}</span>{" "}
                <span className="muted">{e.state}{e.unit}</span>
              </label>
            ))}
          </div>
          <div className="controls">
            <button className="btn" disabled={!sel.size} onClick={useSelected}>Use selected (winnow sums)</button>
            <button className="btn gold" disabled={!sel.size || busy} onClick={createHelper}>{busy ? "Creating…" : "Create HA sum helper & use"}</button>
          </div>
        </>
      )}
      {msg && <div className={msg.startsWith("Couldn't") ? "error" : "muted"}>{msg}</div>}
    </div>
  );
}
