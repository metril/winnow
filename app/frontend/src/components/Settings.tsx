import { useEffect, useState } from "react";
import { api, PowerEntity, Status } from "../api";
import { fmt } from "../util";
import { Card, SectionTitle, Button, Input, Field, Badge, Dot, useToast } from "../ui";

export default function Settings() {
  const toast = useToast();
  const [s, setS] = useState<Record<string, any>>({});
  const [test, setTest] = useState<any>(null);
  const load = async () => setS(await api.settings());
  useEffect(() => { load(); }, []);

  const set = (k: string, v: string) => setS((p) => ({ ...p, [k]: v }));
  const field = (k: string) => s[k] ?? "";
  const secret = (k: string) => (s[k + "_set"] ? "•••••• (set — leave blank to keep)" : "");

  const save = () => {
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass", "mqtt_prefix",
      "threshold_w", "default_multiplier", "default_unit", "cost_per_kwh", "currency"].forEach((k) => {
        if (s[k] !== undefined && s[k] !== "") body[k] = String(s[k]);
      });
    return api.putSettings(body).then(load);
  };
  const runTest = () => {
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass"].forEach((k) => { if (s[k]) body[k] = String(s[k]); });
    return api.testIntegrations(body).then(setTest);
  };

  return (
    <div className="space-y-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <SectionTitle sub="Read your monitored devices (REST) and live state (WebSocket).">Home Assistant</SectionTitle>
          <div className="space-y-3">
            <Field label="Base URL"><Input value={field("ha_url")} onChange={(e) => set("ha_url", e.target.value)} placeholder="https://ha.example.com" /></Field>
            <Field label="Long-lived token"><Input type="password" value={field("ha_token")} onChange={(e) => set("ha_token", e.target.value)} placeholder={secret("ha_token")} /></Field>
          </div>
        </Card>
        <Card>
          <SectionTitle sub="The same broker Home Assistant listens to (so Discovery entities appear).">MQTT broker</SectionTitle>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Host"><Input value={field("mqtt_host")} onChange={(e) => set("mqtt_host", e.target.value)} placeholder="ha.example.com" /></Field>
            <Field label="Port"><Input value={field("mqtt_port")} onChange={(e) => set("mqtt_port", e.target.value)} placeholder="1883" /></Field>
            <Field label="Username"><Input value={field("mqtt_user")} onChange={(e) => set("mqtt_user", e.target.value)} /></Field>
            <Field label="Password"><Input type="password" value={field("mqtt_pass")} onChange={(e) => set("mqtt_pass", e.target.value)} placeholder={secret("mqtt_pass")} /></Field>
            <Field label="Discovery prefix"><Input value={field("mqtt_prefix")} onChange={(e) => set("mqtt_prefix", e.target.value)} placeholder="homeassistant" /></Field>
          </div>
        </Card>
      </div>

      <MonitoredConsumption />

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <SectionTitle sub="Estimate cost for your published electric meter.">Tariff</SectionTitle>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Cost per kWh"><Input value={field("cost_per_kwh")} onChange={(e) => set("cost_per_kwh", e.target.value)} placeholder="0.18" /></Field>
            <Field label="Currency symbol"><Input value={field("currency")} onChange={(e) => set("currency", e.target.value)} placeholder="$" /></Field>
          </div>
        </Card>
        <Card>
          <SectionTitle sub="Defaults for new published sensors and auto load windows.">Tuning</SectionTitle>
          <div className="grid grid-cols-3 gap-3">
            <Field label="Load threshold (W)"><Input value={field("threshold_w")} onChange={(e) => set("threshold_w", e.target.value)} placeholder="50" /></Field>
            <Field label="Default multiplier"><Input value={field("default_multiplier")} onChange={(e) => set("default_multiplier", e.target.value)} placeholder="1" /></Field>
            <Field label="Default unit"><Input value={field("default_unit")} onChange={(e) => set("default_unit", e.target.value)} placeholder="kWh" /></Field>
          </div>
        </Card>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" success="Settings saved" onClick={save}>Save settings</Button>
        <Button onClick={runTest}>Test connections</Button>
        {test && <>
          <Badge tone={test.ha?.ok ? "good" : "bad"}><Dot ok={test.ha?.ok} /> HA {test.ha?.ok ? "ok" : test.ha?.error}</Badge>
          <Badge tone={test.mqtt?.ok ? "good" : "bad"}><Dot ok={test.mqtt?.ok} /> MQTT {test.mqtt?.ok ? "ok" : test.mqtt?.error}</Badge>
        </>}
      </div>
    </div>
  );
}

function MonitoredConsumption() {
  const toast = useToast();
  const [entities, setEntities] = useState<PowerEntity[]>([]);
  const [status, setStatus] = useState<Status | null>(null);
  const [sel, setSel] = useState<Set<string>>(new Set());

  const loadStatus = async () => { const st = await api.status(); setStatus(st); setSel(new Set(st.monitored_entities || [])); };
  useEffect(() => { loadStatus(); }, []);
  const loadEntities = () => api.powerEntities().then(setEntities);
  const toggle = (id: string) => setSel((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });

  const useSelected = () => api.putSettings({ monitored_entities: JSON.stringify([...sel]) }).then(loadStatus);
  const createHelper = async () => {
    const r = await api.createHelper("winnow monitored power", [...sel]);
    if (r.ok) { toast.show(`Created ${r.entity_id} in HA`, "good"); loadStatus(); }
    else throw new Error(`Couldn't auto-create the helper (${r.error}). Create a Group "sum" helper in HA, then pick it here.`);
  };

  return (
    <Card>
      <SectionTitle right={status && <>
        <Badge tone="brand">{status.monitored_entities?.length || 0} selected</Badge>
        <Badge tone="gold">floor ≈ {fmt(status.monitored_floor_w)} W</Badge>
      </>}
        sub='winnow identifies your meter by how well it tracks the sum of your power-monitored devices. Point it at one pre-aggregated HA sensor (Utility Meter / energy, or a Group "sum"), or select several below.'>
        Monitored consumption (ground truth)
      </SectionTitle>
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={loadEntities}>Load HA sensors</Button>
        {sel.size > 0 && <span className="text-sm text-muted">{sel.size} checked</span>}
      </div>

      {entities.length > 0 && (
        <div className="mt-3 space-y-3">
          <div className="flex gap-2">
            <Button size="sm" variant="ghost" onClick={() => setSel(new Set(entities.map((e) => e.entity_id)))}>select all</Button>
            <Button size="sm" variant="ghost" onClick={() => setSel(new Set())}>clear</Button>
          </div>
          <div className="max-h-64 overflow-y-auto rounded-lg border border-border bg-bg/40 p-2">
            {entities.map((e) => (
              <label key={e.entity_id} className="flex items-center gap-2 rounded px-2 py-1 text-sm hover:bg-surface2">
                <input type="checkbox" className="accent-brand" checked={sel.has(e.entity_id)} onChange={() => toggle(e.entity_id)} />
                <span>{e.name}</span>
                <span className="mono text-xs text-faint">{e.entity_id}</span>
                <Badge tone={e.kind === "energy" ? "info" : "gold"}>{e.kind}</Badge>
                <span className="ml-auto text-xs text-muted">{e.state}{e.unit}</span>
              </label>
            ))}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" disabled={!sel.size} success="winnow will sum these" onClick={useSelected}>Use selected (winnow sums)</Button>
            <Button variant="gold" disabled={!sel.size} onClick={createHelper}>Create HA sum helper & use</Button>
          </div>
        </div>
      )}
    </Card>
  );
}
