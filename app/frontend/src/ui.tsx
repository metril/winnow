// winnow UI primitives — Pro Observability Dark. The Button auto-manages a
// pending spinner and toasts when its onClick returns a promise.
import {
  createContext, useContext, useState, useCallback, ReactNode,
  ButtonHTMLAttributes, InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes,
} from "react";
import clsx from "clsx";
import { Check, X, Info, Loader2, ArrowUp, ArrowDown, Minus, HelpCircle } from "lucide-react";

export const cx = clsx;
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
  const Icon = { good: Check, bad: X, info: Info };
  return (
    <ToastCtx.Provider value={{ show }}>
      {children}
      <div className="fixed bottom-5 right-5 z-50 flex w-[min(92vw,380px)] flex-col gap-2">
        {toasts.map((t) => {
          const I = Icon[t.kind];
          return (
            <div key={t.id} className={cx(
              "animate-slide-in flex items-start gap-2.5 rounded-xl border px-3.5 py-3 text-small shadow-overlay backdrop-blur",
              t.kind === "good" && "border-good/30 bg-good/10 text-good",
              t.kind === "bad" && "border-bad/30 bg-bad/10 text-bad",
              t.kind === "info" && "border-border-strong bg-overlay text-text")}>
              <I size={16} className="mt-px shrink-0" />
              <span className="flex-1 break-words">{t.msg}</span>
            </div>
          );
        })}
      </div>
    </ToastCtx.Provider>
  );
}

export function Spinner({ size = 14, className = "" }: { size?: number; className?: string }) {
  return <Loader2 size={size} className={cx("animate-[spin_0.7s_linear_infinite]", className)} />;
}

/* -------------------------------- button --------------------------------- */
type Variant = "primary" | "default" | "ghost" | "gold" | "danger";
type Size = "sm" | "md";
interface BtnProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> {
  variant?: Variant; size?: Size; success?: string; icon?: ReactNode;
  onClick?: (e: React.MouseEvent) => void | Promise<any>;
}
const variants: Record<Variant, string> = {
  primary: "bg-brand text-on-brand hover:bg-brand/90 border-transparent font-semibold",
  default: "bg-surface text-text hover:bg-raised border-border",
  ghost: "bg-transparent text-secondary hover:text-text hover:bg-raised border-transparent",
  gold: "bg-gold text-on-gold hover:bg-gold/90 border-transparent font-semibold",
  danger: "bg-bad/12 text-bad hover:bg-bad/20 border-bad/30",
};
export function Button({ variant = "default", size = "md", success, icon, onClick, children, className, disabled, ...rest }: BtnProps) {
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
      className={cx("inline-flex items-center justify-center gap-1.5 rounded-md border transition select-none whitespace-nowrap",
        size === "sm" ? "px-2.5 py-1 text-micro" : "px-3.5 py-1.5 text-small",
        variants[variant], (disabled || busy) && "opacity-50 cursor-not-allowed", className)} {...rest}>
      {busy ? <Spinner /> : icon}{children}
    </button>
  );
}

export function IconButton({ label, onClick, children, danger }: { label: string; onClick?: (e: React.MouseEvent) => void | Promise<any>; children: ReactNode; danger?: boolean }) {
  return <Button variant="ghost" size="sm" title={label} aria-label={label} onClick={onClick}
    className={cx("!px-1.5", danger && "hover:text-bad")}>{children}</Button>;
}

/* ------------------------------ containers ------------------------------- */
type CardVariant = "default" | "interactive" | "accent" | "alert";
const cardVariants: Record<CardVariant, string> = {
  default: "border-border",
  interactive: "border-border hover:border-border-strong hover:bg-raised hover:-translate-y-px cursor-pointer",
  accent: "border-gold/30 bg-gradient-to-b from-gold/[0.04] to-transparent",
  alert: "border-bad/30 bg-bad/[0.04]",
};
export function Card({ children, className, variant = "default", onClick }:
  { children: ReactNode; className?: string; variant?: CardVariant; onClick?: () => void }) {
  return (
    <div onClick={onClick}
      className={cx("rounded-lg border bg-surface shadow-card transition-[transform,background,border]", cardVariants[variant], className)}>
      {children}
    </div>
  );
}
export function CardHeader({ title, subtitle, actions, icon }:
  { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-3.5">
      <div className="flex items-start gap-2.5 min-w-0">
        {icon && <span className="mt-0.5 text-secondary">{icon}</span>}
        <div className="min-w-0">
          <div className="text-h3 text-text">{title}</div>
          {subtitle && <div className="mt-0.5 text-small text-secondary">{subtitle}</div>}
        </div>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}
export function CardBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("p-5", className)}>{children}</div>;
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cx("animate-pulse2 rounded-md bg-raised", className)} />;
}
export function EmptyState({ icon, title, children, action }:
  { icon?: ReactNode; title?: ReactNode; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 py-10 text-center">
      {icon && <div className="text-tertiary">{icon}</div>}
      {title && <div className="text-h3 text-text">{title}</div>}
      {children && <div className="max-w-md text-small text-secondary">{children}</div>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}

/* -------------------------------- stat ----------------------------------- */
export function StatCard({ label, value, unit, icon, delta, tone = "default", spark }:
  { label: ReactNode; value: ReactNode; unit?: string; icon?: ReactNode; delta?: { dir: "up" | "down" | "flat"; text: string };
    tone?: "default" | "brand" | "gold" | "good"; spark?: ReactNode }) {
  const valTone = tone === "brand" ? "text-brand" : tone === "gold" ? "text-gold" : tone === "good" ? "text-good" : "text-text";
  const iconTone = tone === "brand" ? "bg-brand/12 text-brand" : tone === "gold" ? "bg-gold/12 text-gold" : tone === "good" ? "bg-good/12 text-good" : "bg-raised text-secondary";
  return (
    <div className="rounded-lg border border-border bg-surface p-4 shadow-card">
      <div className="flex items-center justify-between">
        <span className="label">{label}</span>
        {icon && <span className={cx("grid h-7 w-7 place-items-center rounded-lg", iconTone)}>{icon}</span>}
      </div>
      <div className="mt-2 flex items-baseline gap-1">
        <span className={cx("text-kpi tabular-nums", valTone)}>{value}</span>
        {unit && <span className="text-small text-tertiary">{unit}</span>}
      </div>
      <div className="mt-1 flex items-center justify-between">
        {delta ? (
          <span className={cx("inline-flex items-center gap-1 text-micro tabular-nums", delta.dir === "up" ? "text-good" : delta.dir === "down" ? "text-bad" : "text-tertiary")}>
            {delta.dir === "up" ? <ArrowUp size={12} /> : delta.dir === "down" ? <ArrowDown size={12} /> : <Minus size={12} />} {delta.text}
          </span>
        ) : <span />}
      </div>
      {spark && <div className="mt-1 -mx-1">{spark}</div>}
    </div>
  );
}

/* ------------------------------- badges ---------------------------------- */
export function Badge({ children, tone = "default", className }:
  { children: ReactNode; tone?: "default" | "brand" | "gold" | "good" | "bad" | "info"; className?: string }) {
  const tones = {
    default: "border-border bg-raised text-secondary",
    brand: "border-brand/30 bg-brand/10 text-brand",
    gold: "border-gold/30 bg-gold/10 text-gold",
    good: "border-good/30 bg-good/10 text-good",
    bad: "border-bad/30 bg-bad/10 text-bad",
    info: "border-info/30 bg-info/10 text-info",
  };
  return <span className={cx("inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-micro font-medium", tones[tone], className)}>{children}</span>;
}
export function Dot({ tone = "good" }: { tone?: "good" | "bad" | "warn" | "off" }) {
  const c = tone === "good" ? "bg-good shadow-[0_0_6px] shadow-good/60" : tone === "bad" ? "bg-bad" : tone === "warn" ? "bg-warn" : "bg-tertiary";
  return <span className={cx("inline-block h-2 w-2 shrink-0 rounded-full", c)} />;
}

/* ------------------------------- inputs ---------------------------------- */
export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx("input", props.className)} />;
}
export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx("input", props.className)} />;
}
export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cx("input cursor-pointer pr-8", props.className)} />;
}
export function Field({ label, children, hint }: { label: ReactNode; children: ReactNode; hint?: string }) {
  return <label className="flex flex-col gap-1.5"><span className="label flex items-center gap-1.5">{label}</span>{children}{hint && <span className="text-micro text-tertiary">{hint}</span>}</label>;
}

/* ------------------------------ tooltip ---------------------------------- */
// Dependency-free hover/focus tooltip. Wraps any trigger; the bubble is
// absolutely positioned and revealed on group-hover/focus-within.
export function Tooltip({ children, content, className }:
  { children: ReactNode; content: ReactNode; className?: string }) {
  return (
    <span className={cx("group/tt relative inline-flex", className)}>
      {children}
      <span role="tooltip"
        className="pointer-events-none absolute bottom-full left-1/2 z-50 mb-1.5 hidden w-max max-w-[16rem] -translate-x-1/2 rounded-md border border-border-strong bg-overlay px-2.5 py-1.5 text-micro font-normal leading-snug text-secondary shadow-overlay group-hover/tt:block group-focus-within/tt:block">
        {content}
      </span>
    </span>
  );
}
// InfoHint is a small "?" affordance carrying a Tooltip — for inline jargon.
export function InfoHint({ children }: { children: ReactNode }) {
  return (
    <Tooltip content={children}>
      <button type="button" tabIndex={0} aria-label="More info"
        className="text-tertiary transition hover:text-secondary focus:outline-none focus-visible:text-secondary">
        <HelpCircle size={13} />
      </button>
    </Tooltip>
  );
}
export function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: string }) {
  return (
    <button type="button" onClick={() => onChange(!checked)} className="inline-flex items-center gap-2 text-small">
      <span className={cx("relative h-5 w-9 rounded-full transition", checked ? "bg-brand" : "bg-raised border border-border-strong")}>
        <span className={cx("absolute top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition", checked ? "left-[18px]" : "left-0.5")} />
      </span>
      {label && <span className="text-secondary">{label}</span>}
    </button>
  );
}

/* ---------------------------- segmented control -------------------------- */
export function Segmented<T extends string | number>({ options, value, onChange }:
  { options: { value: T; label: ReactNode }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="inline-flex rounded-lg border border-border bg-surface p-0.5">
      {options.map((o) => (
        <button key={String(o.value)} onClick={() => onChange(o.value)}
          className={cx("rounded-md px-2.5 py-1 text-micro transition", value === o.value ? "bg-raised text-text shadow-card" : "text-tertiary hover:text-secondary")}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

/* ------------------------------- tabs ------------------------------------ */
export function Tabs<T extends string>({ tabs, value, onChange }:
  { tabs: { id: T; label: ReactNode }[]; value: T; onChange: (t: T) => void }) {
  return (
    <div className="inline-flex gap-1 rounded-lg border border-border bg-surface p-1">
      {tabs.map((t) => (
        <button key={t.id} onClick={() => onChange(t.id)}
          className={cx("rounded-md px-3 py-1.5 text-small transition", value === t.id ? "bg-brand/15 text-brand font-medium" : "text-secondary hover:text-text")}>
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
      <div className="relative z-10 w-[min(92vw,480px)] animate-fade-in rounded-xl border border-border-strong bg-overlay text-text shadow-overlay" onClick={(e) => e.stopPropagation()}>
        <div className="border-b border-border px-5 py-3.5 text-h3">{title}</div>
        <div className="px-5 py-4 text-small text-secondary">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-border px-5 py-3.5">{footer}</div>}
      </div>
    </div>
  );
}

/* ----------------------------- data table -------------------------------- */
export function Table({ children, className }: { children: ReactNode; className?: string }) {
  return <div className="overflow-x-auto"><table className={cx("w-full border-collapse text-small", className)}>{children}</table></div>;
}
export function Th({ children, className, num }: { children?: ReactNode; className?: string; num?: boolean }) {
  return <th className={cx("sticky top-0 z-10 bg-raised px-3 py-2 text-micro font-medium uppercase tracking-wide text-tertiary", num ? "text-right" : "text-left", className)}>{children}</th>;
}
export function Td({ children, className, num }: { children?: ReactNode; className?: string; num?: boolean }) {
  return <td className={cx("px-3 py-2.5", num && "text-right tabular-nums", className)}>{children}</td>;
}
