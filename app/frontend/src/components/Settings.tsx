import { useEffect, useState } from "react";
import { Server, Send, Activity, DollarSign, Sliders, RefreshCw } from "lucide-react";
import { api, PowerEntity, Status } from "../api";
import { fmt } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Input, Field, Badge, Dot, useToast } from "../ui";

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
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass", "mqtt_prefix", "threshold_w", "default_multiplier", "default_unit", "cost_per_kwh", "currency"]
      .forEach((k) => { if (s[k] !== undefined && s[k] !== "") body[k] = String(s[k]); });
    return api.putSettings(body).then(load);
  };
  const runTest = () => {
    const body: Record<string, string> = {};
    ["ha_url", "ha_token", "mqtt_host", "mqtt_port", "mqtt_user", "mqtt_pass"].forEach((k) => { if (s[k]) body[k] = String(s[k]); });
    return api.testIntegrations(body).then(setTest);
  };

  return (
    <Page title="Settings" actions={<>
      <Button variant="primary" onClick={save} success="Settings saved">Save settings</Button>
      <Button variant="default" icon={<RefreshCw size={15} />} onClick={runTest}>Test connections</Button>
      {test && <>
        <Badge tone={test.ha?.ok ? "good" : "bad"}><Dot tone={test.ha?.ok ? "good" : "bad"} /> HA {test.ha?.ok ? "ok" : test.ha?.error}</Badge>
        <Badge tone={test.mqtt?.ok ? "good" : "bad"}><Dot tone={test.mqtt?.ok ? "good" : "bad"} /> MQTT {test.mqtt?.ok ? "ok" : test.mqtt?.error}</Badge>
      </>}
    </>}>
      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader title="Home Assistant" icon={<Server size={16} />} subtitle="Reads your monitored devices (REST) and live state (WebSocket)." />
          <CardBody className="space-y-3">
            <Field label="Base URL"><Input value={field("ha_url")} onChange={(e) => set("ha_url", e.target.value)} placeholder="https://ha.example.com" /></Field>
            <Field label="Long-lived token"><Input type="password" value={field("ha_token")} onChange={(e) => set("ha_token", e.target.value)} placeholder={secret("ha_token")} /></Field>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="MQTT broker" icon={<Send size={16} />} subtitle="The same broker Home Assistant listens to, so Discovery entities appear." />
          <CardBody className="grid grid-cols-2 gap-3">
            <Field label="Host"><Input value={field("mqtt_host")} onChange={(e) => set("mqtt_host", e.target.value)} placeholder="ha.example.com" /></Field>
            <Field label="Port"><Input value={field("mqtt_port")} onChange={(e) => set("mqtt_port", e.target.value)} placeholder="1883" /></Field>
            <Field label="Username"><Input value={field("mqtt_user")} onChange={(e) => set("mqtt_user", e.target.value)} /></Field>
            <Field label="Password"><Input type="password" value={field("mqtt_pass")} onChange={(e) => set("mqtt_pass", e.target.value)} placeholder={secret("mqtt_pass")} /></Field>
            <Field label="Discovery prefix"><Input value={field("mqtt_prefix")} onChange={(e) => set("mqtt_prefix", e.target.value)} placeholder="homeassistant" /></Field>
          </CardBody>
        </Card>
      </div>

      <MonitoredConsumption />

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader title="Tariff" icon={<DollarSign size={16} />} subtitle="Estimate cost for your published electric meter." />
          <CardBody className="grid grid-cols-2 gap-3">
            <Field label="Cost per kWh"><Input value={field("cost_per_kwh")} onChange={(e) => set("cost_per_kwh", e.target.value)} placeholder="0.18" /></Field>
            <Field label="Currency symbol"><Input value={field("currency")} onChange={(e) => set("currency", e.target.value)} placeholder="$" /></Field>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Tuning" icon={<Sliders size={16} />} subtitle="Defaults for new published sensors and auto load windows." />
          <CardBody className="grid grid-cols-3 gap-3">
            <Field label="Load threshold (W)"><Input value={field("threshold_w")} onChange={(e) => set("threshold_w", e.target.value)} placeholder="50" /></Field>
            <Field label="Default multiplier"><Input value={field("default_multiplier")} onChange={(e) => set("default_multiplier", e.target.value)} placeholder="1" /></Field>
            <Field label="Default unit"><Input value={field("default_unit")} onChange={(e) => set("default_unit", e.target.value)} placeholder="kWh" /></Field>
          </CardBody>
        </Card>
      </div>
    </Page>
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
      <CardHeader title="Monitored consumption" icon={<Activity size={16} />}
        subtitle='Ground truth: the sum of your power-monitored devices. Pick one pre-aggregated sensor, or several that winnow sums.'
        actions={status && <>
          <Badge tone="brand">{status.monitored_entities?.length || 0} selected</Badge>
          <Badge tone="gold">floor ≈ {fmt(status.monitored_floor_w)} W</Badge>
        </>} />
      <CardBody>
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={loadEntities}>Load HA sensors</Button>
          {sel.size > 0 && <span className="text-small text-tertiary">{sel.size} checked</span>}
        </div>
        {entities.length > 0 && (
          <div className="mt-3 space-y-3">
            <div className="flex gap-2">
              <Button size="sm" variant="ghost" onClick={() => setSel(new Set(entities.map((e) => e.entity_id)))}>select all</Button>
              <Button size="sm" variant="ghost" onClick={() => setSel(new Set())}>clear</Button>
            </div>
            <div className="max-h-64 overflow-y-auto rounded-lg border border-border bg-app/40 p-2">
              {entities.map((e) => (
                <label key={e.entity_id} className="flex items-center gap-2 rounded px-2 py-1 text-small hover:bg-raised">
                  <input type="checkbox" className="accent-brand" checked={sel.has(e.entity_id)} onChange={() => toggle(e.entity_id)} />
                  <span>{e.name}</span><span className="mono text-micro text-tertiary">{e.entity_id}</span>
                  <Badge tone={e.kind === "energy" ? "info" : "gold"}>{e.kind}</Badge>
                  <span className="ml-auto text-micro text-tertiary">{e.state}{e.unit}</span>
                </label>
              ))}
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="primary" disabled={!sel.size} onClick={useSelected} success="winnow will sum these">Use selected (winnow sums)</Button>
              <Button variant="gold" disabled={!sel.size} onClick={createHelper}>Create HA sum helper & use</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
