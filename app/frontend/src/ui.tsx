// winnow UI primitives: a small, modern dark design system on Tailwind.
// The Button auto-manages a pending spinner and toasts when its onClick returns
// a promise — so every action gives visible feedback with no extra wiring.
import {
  createContext, useContext, useState, useCallback, ReactNode,
  ButtonHTMLAttributes, InputHTMLAttributes, SelectHTMLAttributes,
} from "react";

function cx(...xs: (string | false | null | undefined)[]) { return xs.filter(Boolean).join(" "); }
function errMsg(e: any) { return String(e?.message ?? e ?? "error").replace(/^Error:\s*/, ""); }

/* ------------------------------- toasts ---------------------------------- */
type ToastKind = "good" | "bad" | "info";
interface Toast { id: number; msg: string; kind: ToastKind }
interface ToastApi { show: (msg: string, kind?: ToastKind) => void }
const ToastCtx = createContext<ToastApi>({ show: () => {} });
export const useToast = () => useContext(ToastCtx);

let toastSeq = 1;
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const show = useCallback((msg: string, kind: ToastKind = "info") => {
    const id = toastSeq++;
    setToasts((t) => [...t, { id, msg, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4000);
  }, []);
  return (
    <ToastCtx.Provider value={{ show }}>
      {children}
      <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 w-[min(92vw,360px)]">
        {toasts.map((t) => (
          <div key={t.id} className={cx(
            "animate-fade-in rounded-lg border px-3.5 py-2.5 text-sm shadow-pop flex items-start gap-2",
            t.kind === "good" && "border-good/40 bg-good/10 text-good",
            t.kind === "bad" && "border-bad/40 bg-bad/10 text-bad",
            t.kind === "info" && "border-border bg-elevated text-text")}>
            <span className="mt-0.5">{t.kind === "good" ? "✓" : t.kind === "bad" ? "✕" : "›"}</span>
            <span className="flex-1 break-words">{t.msg}</span>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/* ------------------------------- spinner --------------------------------- */
export function Spinner({ className = "" }: { className?: string }) {
  return (
    <svg className={cx("animate-[spin_0.7s_linear_infinite]", className)} width="14" height="14" viewBox="0 0 24 24" fill="none">
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="3" opacity="0.25" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
    </svg>
  );
}

/* -------------------------------- button --------------------------------- */
type Variant = "primary" | "default" | "ghost" | "gold" | "danger";
type Size = "sm" | "md";
interface BtnProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> {
  variant?: Variant; size?: Size; success?: string;
  onClick?: (e: React.MouseEvent) => void | Promise<any>;
}
const variants: Record<Variant, string> = {
  primary: "bg-brand text-bg hover:bg-brand/90 border-transparent font-semibold",
  default: "bg-surface2 text-text hover:bg-elevated border-border",
  ghost: "bg-transparent text-muted hover:text-text hover:bg-surface2 border-transparent",
  gold: "bg-gold text-bg hover:bg-gold/90 border-transparent font-semibold",
  danger: "bg-bad/15 text-bad hover:bg-bad/25 border-bad/30",
};
export function Button({ variant = "default", size = "md", success, onClick, children, className, disabled, ...rest }: BtnProps) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const handle = async (e: React.MouseEvent) => {
    if (!onClick) return;
    const r = onClick(e);
    if (r && typeof (r as any).then === "function") {
      setBusy(true);
      try { await r; if (success) toast.show(success, "good"); }
      catch (err) { toast.show(errMsg(err), "bad"); }
      finally { setBusy(false); }
    }
  };
  return (
    <button onClick={handle} disabled={disabled || busy}
      className={cx("inline-flex items-center justify-center gap-1.5 rounded-lg border transition select-none",
        size === "sm" ? "px-2.5 py-1 text-xs" : "px-3.5 py-1.5 text-sm",
        variants[variant], (disabled || busy) && "opacity-50 cursor-not-allowed", className)} {...rest}>
      {busy && <Spinner />}{children}
    </button>
  );
}

/* ------------------------------ containers ------------------------------- */
export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("card p-4", className)}>{children}</div>;
}
export function SectionTitle({ children, sub, right }: { children: ReactNode; sub?: ReactNode; right?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 mb-3">
      <div>
        <h2 className="text-[15px] text-text">{children}</h2>
        {sub && <p className="text-sm text-muted mt-0.5 max-w-2xl">{sub}</p>}
      </div>
      {right && <div className="flex items-center gap-2 shrink-0">{right}</div>}
    </div>
  );
}
export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="text-sm text-faint italic py-8 text-center">{children}</div>;
}
export function Skeleton({ className }: { className?: string }) {
  return <div className={cx("animate-pulse2 rounded-md bg-surface2", className)} />;
}

/* ------------------------------- badges ---------------------------------- */
export function Badge({ children, tone = "default", className }:
  { children: ReactNode; tone?: "default" | "brand" | "gold" | "good" | "bad" | "info"; className?: string }) {
  const tones = {
    default: "border-border bg-surface2 text-muted",
    brand: "border-brand/30 bg-brand/10 text-brand",
    gold: "border-gold/30 bg-gold/10 text-gold",
    good: "border-good/30 bg-good/10 text-good",
    bad: "border-bad/30 bg-bad/10 text-bad",
    info: "border-info/30 bg-info/10 text-info",
  };
  return <span className={cx("inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium", tones[tone], className)}>{children}</span>;
}
export function Dot({ ok }: { ok: boolean }) {
  return <span className={cx("inline-block h-2 w-2 rounded-full", ok ? "bg-good shadow-[0_0_6px] shadow-good/60" : "bg-bad")} />;
}

/* ------------------------------- inputs ---------------------------------- */
export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx("input", props.className)} />;
}
export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cx("input pr-8 cursor-pointer", props.className)} />;
}
export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="flex flex-col gap-1.5"><span className="label">{label}</span>{children}</label>;
}
export function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: string }) {
  return (
    <button type="button" onClick={() => onChange(!checked)}
      className={cx("inline-flex items-center gap-2 text-sm", label && "")}>
      <span className={cx("relative h-5 w-9 rounded-full transition", checked ? "bg-brand" : "bg-surface2 border border-border")}>
        <span className={cx("absolute top-0.5 h-4 w-4 rounded-full bg-bg transition", checked ? "left-[18px]" : "left-0.5")} />
      </span>
      {label && <span className="text-muted">{label}</span>}
    </button>
  );
}

/* ------------------------------- tabs ------------------------------------ */
export function Tabs<T extends string>({ tabs, value, onChange }:
  { tabs: { id: T; label: ReactNode }[]; value: T; onChange: (t: T) => void }) {
  return (
    <div className="inline-flex gap-1 rounded-lg border border-border bg-surface p-1">
      {tabs.map((t) => (
        <button key={t.id} onClick={() => onChange(t.id)}
          className={cx("rounded-md px-3 py-1.5 text-sm transition",
            value === t.id ? "bg-brand/15 text-brand font-medium" : "text-muted hover:text-text")}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

/* ------------------------------- dialog ---------------------------------- */
export function Dialog({ open, onClose, title, children, footer }:
  { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode }) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" onClick={onClose}>
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" />
      <div className="relative z-10 w-[min(92vw,480px)] animate-fade-in rounded-xl border border-border bg-surface shadow-pop"
        onClick={(e) => e.stopPropagation()}>
        <div className="border-b border-border px-4 py-3 text-[15px] font-semibold">{title}</div>
        <div className="px-4 py-4 text-sm text-muted">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-border px-4 py-3">{footer}</div>}
      </div>
    </div>
  );
}

/* small live stat block */
export function Stat({ label, value, sub, tone }:
  { label: ReactNode; value: ReactNode; sub?: ReactNode; tone?: "brand" | "gold" | "good" }) {
  const t = tone === "brand" ? "text-brand" : tone === "gold" ? "text-gold" : tone === "good" ? "text-good" : "text-text";
  return (
    <div className="rounded-lg border border-border bg-surface2/50 px-3.5 py-3">
      <div className="label">{label}</div>
      <div className={cx("mt-1 text-2xl font-semibold tabular-nums", t)}>{value}</div>
      {sub && <div className="text-xs text-muted mt-0.5">{sub}</div>}
    </div>
  );
}
