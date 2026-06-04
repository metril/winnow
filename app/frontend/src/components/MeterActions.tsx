// Shared meter toggles so the affordance is identical everywhere (Identify and
// Meters). Both render an icon toggle and gate the change behind an in-app
// confirmation Dialog — never a browser confirm() popup.
import { useState } from "react";
import { Star, Radio } from "lucide-react";
import { api } from "../api";
import { IconButton, Button, Dialog, useToast } from "../ui";

// TrackStar — toggles a meter's tracked (is_mine) state. Filled gold when tracked.
export function TrackStar({ id, isMine, onChange }: { id: number; isMine: boolean; onChange?: () => void }) {
  const toast = useToast();
  const [ask, setAsk] = useState(false);
  const next = !isMine;
  const confirm = () =>
    api.patchMeter(id, next ? { is_mine: true, is_candidate: true } : { is_mine: false })
      .then(() => { setAsk(false); toast.show(next ? "Tracked" : "Untracked", "good"); onChange?.(); })
      .catch((e) => toast.show(String(e), "bad"));
  return (
    <>
      <IconButton label={isMine ? "untrack" : "track"} onClick={() => setAsk(true)}>
        <Star size={15} className={isMine ? "fill-gold text-gold" : ""} />
      </IconButton>
      <Dialog open={ask} onClose={() => setAsk(false)} title={`${next ? "Track" : "Untrack"} meter #${id}`}
        footer={<>
          <Button variant="ghost" onClick={() => setAsk(false)}>Cancel</Button>
          <Button variant="primary" onClick={confirm}>{next ? "Track" : "Untrack"}</Button>
        </>}>
        {next ? "Mark this meter as yours so it shows in your tracked set."
              : "Stop tracking this meter. Stored readings are kept."}
      </Dialog>
    </>
  );
}

// PublishToggle — toggles whether a meter is published to Home Assistant.
export function PublishToggle({ id, publish, onChange }: { id: number; publish: boolean; onChange?: () => void }) {
  const toast = useToast();
  const [ask, setAsk] = useState(false);
  const next = !publish;
  const confirm = () =>
    api.patchMeter(id, { publish: next, is_mine: true })
      .then(() => { setAsk(false); toast.show(next ? "Publishing to HA" : "Unpublished", "good"); onChange?.(); })
      .catch((e) => toast.show(String(e), "bad"));
  return (
    <>
      <IconButton label={publish ? "stop publishing" : "publish"} onClick={() => setAsk(true)}>
        <Radio size={15} className={publish ? "text-gold" : ""} />
      </IconButton>
      <Dialog open={ask} onClose={() => setAsk(false)} title={`${next ? "Publish" : "Stop publishing"} meter #${id}`}
        footer={<>
          <Button variant="ghost" onClick={() => setAsk(false)}>Cancel</Button>
          <Button variant={next ? "gold" : "danger"} onClick={confirm}>{next ? "Publish" : "Stop publishing"}</Button>
        </>}>
        {next ? "Create Home Assistant sensors for this meter (energy, power, signal) via MQTT Discovery. Tracking is enabled too."
              : "Remove this meter's Home Assistant sensors. Tracking is kept."}
      </Dialog>
    </>
  );
}
