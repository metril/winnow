import { useEffect, useState } from "react";
import { api, PowerEntity } from "../api";

// Settings: all HA + MQTT + identification config lives here (stored in the DB).
export default function Settings() {
  const [s, setS] = useState<Record<string, any>>({});
  const [entities, setEntities] = useState<PowerEntity[]>([]);
  const [test, setTest] = useState<any>(null);
  const [saved, setSaved] = useState(false);

  const load = async () => setS(await api.settings());
  useEffect(() => { load(); }, []);

  const set = (k: string, v: string) => setS((p) => ({ ...p, [k]: v }));
  const field = (k: string) => s[k] ?? "";
  const secretPlaceholder = (k: string) => (s[k + "_set"] ? "•••••• (set — leave blank to keep)" : "");

  const save = async () => {
    // only send non-empty values; secrets left blank are preserved server-side
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass", "mqtt_prefix",
     "reference_entity", "threshold_w", "default_multiplier", "default_unit"].forEach((k) => {
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

  const loadEntities = async () => {
    try { setEntities(await api.powerEntities()); } catch (e) { alert(String(e)); }
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

      <div className="panel">
        <h2>Identification</h2>
        <div className="form">
          <label>Reference plug</label>
          <div className="controls" style={{ margin: 0 }}>
            <input type="text" value={field("reference_entity")} onChange={(e) => set("reference_entity", e.target.value)} placeholder="sensor.heater_plug_power" style={{ flex: 1 }} />
            <button className="btn alt" onClick={loadEntities}>List power sensors</button>
          </div>
          {entities.length > 0 && (
            <select value={field("reference_entity")} onChange={(e) => set("reference_entity", e.target.value)}>
              <option value="">— pick —</option>
              {entities.map((e) => <option key={e.entity_id} value={e.entity_id}>{e.name} ({e.entity_id}) {e.state}{e.unit}</option>)}
            </select>
          )}
          <label>On/off threshold (W)</label>
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
