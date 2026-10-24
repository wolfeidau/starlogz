import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { dayKey, formatDayLabel, timestampToDate } from "../../lib/time";

export type LogGroup = {
  key: string;
  label: string;
  items: [Insight, number][];
};

// Search results are relevance-ordered, so they never split by day.
export function toGroups(
  insights: Insight[],
  searching: boolean,
  now: Date,
): LogGroup[] {
  if (searching) {
    return [
      {
        key: "results",
        label: "",
        items: insights.map((insight, index) => [insight, index]),
      },
    ];
  }
  const groups: LogGroup[] = [];
  insights.forEach((insight, index) => {
    const date = timestampToDate(insight.updatedAt);
    const key = date ? dayKey(date) : "unknown";
    const last = groups.at(-1);
    if (last && last.key === key) {
      last.items.push([insight, index]);
    } else {
      groups.push({
        key,
        label: date ? formatDayLabel(date, now) : "Unknown date",
        items: [[insight, index]],
      });
    }
  });
  return groups;
}
