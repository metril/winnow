import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { Button, Segmented } from "../ui";

export type PeriodView = "day" | "week" | "month" | "year";

const VIEWS: { value: PeriodView; label: string }[] = [
  { value: "day", label: "Day" },
  { value: "week", label: "Week" },
  { value: "month", label: "Month" },
  { value: "year", label: "Year" },
];

// PeriodNav is purely presentational: the browser above it owns view/anchor
// state; the server owns cursor validity (a nil anchor disables the arrow).
export default function PeriodNav({ view, label, prevDisabled, nextDisabled, onView, onPrev, onNext, onToday }: {
  view: PeriodView; label: string; prevDisabled: boolean; nextDisabled: boolean;
  onView: (v: PeriodView) => void; onPrev: () => void; onNext: () => void; onToday: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Segmented options={VIEWS} value={view} onChange={onView} />
      <div className="inline-flex items-center gap-0.5 rounded-lg border border-border bg-surface p-0.5">
        <Button size="sm" variant="ghost" onClick={onPrev} disabled={prevDisabled}
          aria-label="Previous period" icon={<ChevronLeft size={14} />} />
        <span className="min-w-[130px] px-1 text-center text-small text-text tabular-nums">{label}</span>
        <Button size="sm" variant="ghost" onClick={onNext} disabled={nextDisabled}
          aria-label="Next period" icon={<ChevronRight size={14} />} />
      </div>
      <Button size="sm" variant="ghost" onClick={onToday} disabled={nextDisabled}
        icon={<CalendarDays size={13} />}>Today</Button>
    </div>
  );
}
