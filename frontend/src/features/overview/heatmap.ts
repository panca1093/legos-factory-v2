import type { UsageDay } from "../../types/api";

export type HeatMode = "Daily" | "Weekly" | "Cumulative";
export interface HeatCell {
  date: string;
  row: number;
  col: number;
  value: number;
  level: 0 | 1 | 2 | 3 | 4;
  label: string;
}

const dayOf = (date: string) => new Date(`${date}T00:00:00Z`);
const fmt = (date: string, opts: Intl.DateTimeFormatOptions) =>
  new Intl.DateTimeFormat("en-US", { timeZone: "UTC", ...opts }).format(
    dayOf(date),
  );

function level(value: number, max: number): HeatCell["level"] {
  if (!value) return 0;
  const r = value / max;
  return r > 0.75 ? 4 : r > 0.45 ? 3 : r > 0.2 ? 2 : 1;
}

export function heatCells(days: UsageDay[], mode: HeatMode) {
  const offset = days.length ? dayOf(days[0].date).getUTCDay() : 0;
  const pos = days.map((_, i) => ({
    row: (i + offset) % 7,
    col: Math.floor((i + offset) / 7),
  }));
  const weeks = days.length ? pos[pos.length - 1].col + 1 : 0;
  const weekTotal = new Array<number>(weeks).fill(0);
  days.forEach((d, i) => (weekTotal[pos[i].col] += d.count));
  let run = 0;
  const values = days.map((d, i) =>
    mode === "Weekly"
      ? weekTotal[pos[i].col]
      : mode === "Cumulative"
        ? (run += d.count)
        : d.count,
  );
  const max = Math.max(0, ...values) || 1;
  const cells: HeatCell[] = days.map((d, i) => {
    const day = fmt(d.date, {
      weekday: "short",
      month: "short",
      day: "numeric",
    });
    const label =
      mode === "Weekly"
        ? `Week of ${fmt(days[Math.max(0, i - pos[i].row)].date, { month: "short", day: "numeric" })} · ${values[i]} runs`
        : mode === "Cumulative"
          ? `${day} · ${values[i]} total`
          : `${day} · ${d.count ? `${d.count} run${d.count > 1 ? "s" : ""}` : "no activity"}`;
    return {
      date: d.date,
      ...pos[i],
      value: values[i],
      level: level(values[i], max),
      label,
    };
  });
  const months: { col: number; label: string }[] = [];
  let last = "";
  days.forEach((d, i) => {
    const month = d.date.slice(0, 7);
    if (month !== last && (pos[i].row === 0 || i === 0)) {
      last = month;
      months.push({ col: pos[i].col, label: fmt(d.date, { month: "short" }) });
    }
  });
  return { cells, weeks, months };
}
