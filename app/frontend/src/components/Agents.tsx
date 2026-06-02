import { useState } from "react";
import { Satellite, KeyRound, ShieldCheck, Copy, Check, Trash2, Terminal, Plug } from "lucide-react";
import { api, AgentsResp } from "../api";
import { useLive } from "../live";
import { useFetch } from "../fetch";
import { shortTs } from "../util";
import { Page } from "./shell";
import { Card, CardHeader, CardBody, Button, Input, Field, Badge, Dot, InfoHint, EmptyState, Skeleton, useToast } from "../ui";

function Copyable({ value, mono = true }: { value: string; mono?: boolean }) {
  const [done, setDone] = useState(false);
  const copy = () => navigator.clipboard.writeText(value).then(() => { setDone(true); setTimeout(() => setDone(false), 1500); });
  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-app/40 px-2.5 py-1.5">
      <span className={"min-w-0 flex-1 truncate text-small " + (mono ? "mono text-secondary" : "text-text")}>{value}</span>
      <button type="button" onClick={copy} className="shrink-0 text-tertiary transition hover:text-text" aria-label="Copy">
        {done ? <Check size={14} className="text-good" /> : <Copy size={14} />}
      </button>
    </div>
  );
}

export default function Agents() {
  const { configVersion } = useLive();
  const { data, reload } = useFetch(api.agents, [configVersion]);
  return (
    <Page title="Remote agents" breadcrumb="System">
      {!data ? <Card><CardBody><Skeleton className="h-40" /></CardBody></Card> : <Inner data={data} reload={reload} />}
    </Page>
  );
}

function Inner({ data, reload }: { data: AgentsResp; reload: () => void }) {
  const toast = useToast();
  const [label, setLabel] = useState("");
  const [pubkey, setPubkey] = useState("");

  const host = window.location.hostname;
  const url = `wss://${host}:8443/api/agent/ws`;
  const runCmd = [
    "docker run -d --name winnow-agent --restart unless-stopped \\",
    "  --device /dev/bus/usb:/dev/bus/usb -v winnow-agent-key:/data \\",
    `  -e AGENT_URL=${url} \\`,
    `  -e AGENT_SERVER_KEY=${data.server_key} \\`,
    "  -e AGENT_NAME=garage \\",
    "  winnow-capture",
  ].join("\n");

  const authorize = () => {
    if (!pubkey.trim()) return Promise.reject(new Error("paste the agent's public key"));
    return api.authorizeAgent(label.trim() || "agent", pubkey.trim())
      .then(() => { setLabel(""); setPubkey(""); reload(); });
  };
  const revoke = (pk: string, lbl: string) =>
    api.revokeAgent(pk).then(() => { toast.show(`Revoked ${lbl}`, "good"); reload(); });

  return (
    <>
      <Card>
        <CardHeader title="This server" icon={<ShieldCheck size={16} />}
          subtitle="A remote agent decodes locally and streams readings here over an encrypted, mutually-authenticated channel. Share the public key below — never a secret." />
        <CardBody className="grid gap-4 lg:grid-cols-2">
          <div className="space-y-1.5">
            <div className="label flex items-center gap-1.5">Server public key <InfoHint>The agent verifies the server with this Curve25519 key (AGENT_SERVER_KEY). It's public — safe to share. The data stream is encrypted end-to-end with it, so even a TLS-terminating proxy only sees ciphertext.</InfoHint></div>
            <Copyable value={data.server_key} />
          </div>
          <div className="space-y-1.5">
            <div className="label flex items-center gap-1.5">Agent URL <InfoHint>Where agents connect (the encrypted :8443 listener). If you later add a reverse proxy, point agents at it instead and drop the cert fingerprint — auth is unaffected.</InfoHint></div>
            <Copyable value={url} />
          </div>
          <div className="space-y-1.5 lg:col-span-2">
            <div className="label flex items-center gap-1.5">TLS cert fingerprint (SHA-256) <InfoHint>Optional outer-TLS pin: set AGENT_SERVER_FINGERPRINT on the agent to this value to pin the self-signed cert. Optional because the channel is already authenticated by the server key.</InfoHint></div>
            <Copyable value={data.tls_fingerprint || "—"} />
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Run an agent" icon={<Terminal size={16} />}
          subtitle="On the remote host: build the capture image from this repo (capture/Dockerfile, tag it winnow-capture), then run it. On first start it prints its public key — authorize it below." />
        <CardBody>
          <pre className="overflow-x-auto rounded-md border border-border bg-app/40 p-3 text-micro leading-relaxed text-secondary">{runCmd}</pre>
          <div className="mt-2"><Copyable value={runCmd} mono={false} /></div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Authorized agents" icon={<KeyRound size={16} />}
          subtitle="Only listed public keys may connect (the SSH authorized_keys model). Paste the key an agent printed on first start." />
        <CardBody className="space-y-4">
          <div className="flex flex-wrap items-end gap-3">
            <Field label="Label"><Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="garage Pi" className="w-44" /></Field>
            <Field label="Agent public key"><Input value={pubkey} onChange={(e) => setPubkey(e.target.value)} placeholder="base64 key from the agent log" className="w-[22rem]" /></Field>
            <Button variant="primary" onClick={authorize} success="Authorized">Authorize</Button>
          </div>
          {data.authorized.length === 0 ? <EmptyState icon={<KeyRound size={20} />} title="No agents authorized">Paste a key above to allow a remote agent.</EmptyState>
            : <div className="space-y-2">
              {data.authorized.map((a) => (
                <div key={a.pubkey} className="flex items-center gap-3 rounded-md border border-border bg-app/40 px-3 py-2">
                  <Badge tone="brand">{a.label}</Badge>
                  <span className="mono truncate text-micro text-tertiary">{a.fingerprint}</span>
                  <button type="button" onClick={() => revoke(a.pubkey, a.label)} className="ml-auto inline-flex items-center gap-1 text-micro text-bad hover:underline"><Trash2 size={12} /> revoke</button>
                </div>
              ))}
            </div>}
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Connected dongles" icon={<Plug size={16} />} subtitle="Remote dongles reporting through agents. They appear in the inventory and coverage like local ones." />
        <CardBody>
          {data.remotes.length === 0 ? <EmptyState icon={<Satellite size={20} />} title="No remote dongles yet">Authorize an agent and it will appear here once it connects.</EmptyState>
            : <div className="space-y-2">
              {data.remotes.map((r) => (
                <div key={r.source} className="flex items-center gap-2 rounded-md border border-border bg-app/40 px-3 py-2 text-small">
                  <Dot tone={r.alive ? "good" : "bad"} />
                  <span className="text-text">{r.label}</span>
                  <span className="mono text-micro text-tertiary">{r.source}</span>
                  <span className="ml-auto text-micro text-tertiary">{r.alive ? "live" : "idle"} · {r.last_seen ? shortTs(r.last_seen) : "never"}</span>
                </div>
              ))}
            </div>}
        </CardBody>
      </Card>
    </>
  );
}
