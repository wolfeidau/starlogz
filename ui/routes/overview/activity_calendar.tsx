import { max } from "d3-array";
import { scaleQuantize } from "d3-scale";
import { useEffect, useRef, useState } from "react";
import type { ActivityBucket } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Metric } from "../../components/metric";
import { dayKey } from "../../lib/time";
import "./overview.css";

const CELL = 11;
const GAP = 3;
const STEP = CELL + GAP;
const LEFT = 28;
const TOP = 18;
const WEEKS = 53;
const DAY_MS = 86_400_000;

export interface CalendarCell {
  date: string;
  col: number;
  row: number;
  count: number;
  level: number;
}

export interface CalendarStats {
  total: number;
  activeDays: number;
  streak: number;
  busiest?: { date: string; count: number };
}

// UTC noon keeps day arithmetic immune to DST shifts.
function utcNoon(key: string): number {
  const [y, m, d] = key.split("-").map(Number);
  return Date.UTC(y, m - 1, d, 12);
}

function keyOf(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10);
}

export function buildCells(
  buckets: ActivityBucket[],
  today: Date,
): CalendarCell[] {
  const todayMs = utcNoon(dayKey(today));
  const dow = new Date(todayMs).getUTCDay();
  const startMs = todayMs - (dow + 364) * DAY_MS;
  const counts = new Map(buckets.map((b) => [b.date, b.count]));
  const peak = max(buckets, (b) => b.count) ?? 0;
  const level = scaleQuantize<number>()
    .domain([0, Math.max(peak, 1)])
    .range([1, 2, 3, 4]);
  const cells: CalendarCell[] = [];
  const days = (todayMs - startMs) / DAY_MS;
  for (let i = 0; i <= days; i++) {
    const date = keyOf(startMs + i * DAY_MS);
    const count = counts.get(date) ?? 0;
    cells.push({
      date,
      col: Math.floor(i / 7),
      row: i % 7,
      count,
      level: count > 0 ? level(count) : 0,
    });
  }
  return cells;
}

export function computeStats(
  cells: CalendarCell[],
  today: Date,
): CalendarStats {
  let total = 0;
  let activeDays = 0;
  let busiest: CalendarStats["busiest"];
  for (const c of cells) {
    total += c.count;
    if (c.count > 0) activeDays++;
    if (c.count > 0 && (!busiest || c.count >= busiest.count)) {
      busiest = { date: c.date, count: c.count };
    }
  }
  const counts = new Map(cells.map((c) => [c.date, c.count]));
  let ms = utcNoon(dayKey(today));
  if (!counts.get(keyOf(ms))) ms -= DAY_MS;
  let streak = 0;
  while (counts.get(keyOf(ms))) {
    streak++;
    ms -= DAY_MS;
  }
  return { total, activeDays, streak, busiest };
}

const dateFormat = (opts: Intl.DateTimeFormatOptions) =>
  new Intl.DateTimeFormat("en-US", { timeZone: "UTC", ...opts });
const longDay = dateFormat({
  weekday: "short",
  month: "short",
  day: "numeric",
});
const shortDay = dateFormat({ month: "short", day: "numeric" });
const monthName = dateFormat({ month: "short" });

function writes(count: number): string {
  return `${count} ${count === 1 ? "write" : "writes"}`;
}

export function cellLabel(cell: CalendarCell): string {
  const day = longDay.format(utcNoon(cell.date));
  return cell.count === 0
    ? `No writes on ${day}`
    : `${writes(cell.count)} on ${day}`;
}

export function monthLabels(
  cells: CalendarCell[],
): { col: number; text: string }[] {
  const labels: { col: number; text: string }[] = [];
  let prevMonth = -1;
  for (const c of cells) {
    if (c.row !== 0) continue;
    const month = Number(c.date.slice(5, 7));
    if (month === prevMonth) continue;
    prevMonth = month;
    const last = labels[labels.length - 1];
    // A crowded label is always the partial leading month; the full month wins.
    if (last && c.col - last.col < 3) labels.pop();
    labels.push({ col: c.col, text: monthName.format(utcNoon(c.date)) });
  }
  return labels;
}

const DAY_LABELS = [
  { row: 1, text: "Mon" },
  { row: 3, text: "Wed" },
  { row: 5, text: "Fri" },
];

export function ActivityCalendar({
  buckets,
  today = new Date(),
}: {
  buckets: ActivityBucket[];
  today?: Date;
}) {
  const [hovered, setHovered] = useState<CalendarCell | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const cells = buildCells(buckets, today);
  const stats = computeStats(cells, today);
  const todayKey = dayKey(today);
  const width = LEFT + WEEKS * STEP - GAP;
  const height = TOP + 7 * STEP - GAP;

  useEffect(() => {
    const el = scroller.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, []);

  return (
    <section className="panel activity-panel">
      <h2>Activity</h2>
      <div className="activity-body">
        <div className="activity-calendar">
          <div className="activity-scroll" ref={scroller}>
            <svg
              role="img"
              aria-label={`Activity calendar: ${writes(stats.total)} in the last year`}
              width={width}
              height={height}
              viewBox={`0 0 ${width} ${height}`}
              onMouseLeave={() => setHovered(null)}
            >
              {monthLabels(cells).map((l) => (
                <text
                  className="activity-label"
                  key={`m${l.col}`}
                  x={LEFT + l.col * STEP}
                  y={10}
                >
                  {l.text}
                </text>
              ))}
              {DAY_LABELS.map((l) => (
                <text
                  className="activity-label"
                  key={l.text}
                  x={0}
                  y={TOP + l.row * STEP + CELL - 1}
                >
                  {l.text}
                </text>
              ))}
              {cells.map((c) => (
                // biome-ignore lint/a11y/noStaticElementInteractions: hover readout only; cells are not focusable
                <rect
                  key={c.date}
                  className={`activity-cell activity-level-${c.level}${c.date === todayKey ? " activity-cell-today" : ""}`}
                  data-date={c.date}
                  data-level={c.level}
                  x={LEFT + c.col * STEP}
                  y={TOP + c.row * STEP}
                  width={CELL}
                  height={CELL}
                  rx={2}
                  onMouseEnter={() => setHovered(c)}
                >
                  <title>{cellLabel(c)}</title>
                </rect>
              ))}
            </svg>
          </div>
          <div className="activity-footer">
            <span className="activity-readout">
              {hovered ? cellLabel(hovered) : ""}
            </span>
            <span className="activity-legend">
              Less
              {[0, 1, 2, 3, 4].map((l) => (
                <svg key={l} width={CELL} height={CELL} aria-hidden="true">
                  <rect
                    className={`activity-cell activity-level-${l}`}
                    width={CELL}
                    height={CELL}
                    rx={2}
                  />
                </svg>
              ))}
              More
            </span>
          </div>
        </div>
        <div className="activity-stats">
          <Metric label="Writes (last year)" value={stats.total} />
          <Metric label="Active days" value={stats.activeDays} />
          <Metric label="Current streak" value={stats.streak} />
          <Metric
            label="Busiest day"
            value={
              stats.busiest
                ? `${shortDay.format(utcNoon(stats.busiest.date))} (${stats.busiest.count})`
                : "-"
            }
          />
        </div>
      </div>
    </section>
  );
}
