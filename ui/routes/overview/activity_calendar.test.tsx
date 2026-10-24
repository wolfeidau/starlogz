import { afterEach, describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { ActivityBucketSchema } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import {
  ActivityCalendar,
  buildCells,
  cellLabel,
  computeStats,
  zonedDayKey,
} from "./activity_calendar";

afterEach(cleanup);

// Wednesday
const today = new Date(Date.UTC(2025, 9, 22, 15, 30));
const todayKey = "2025-10-22";

function buckets(entries: Record<string, number>) {
  return Object.entries(entries).map(([date, count]) =>
    create(ActivityBucketSchema, { date, count }),
  );
}

describe("buildCells", () => {
  test("covers 53 Sunday-start weeks ending today", () => {
    const cells = buildCells([], todayKey);
    expect(cells.length).toBe(364 + 3 + 1);
    expect(cells[0].date).toBe("2024-10-20");
    expect(new Date(`${cells[0].date}T12:00:00Z`).getUTCDay()).toBe(0);
    expect(cells[cells.length - 1].date).toBe("2025-10-22");
    expect(cells[cells.length - 1].col).toBe(52);
    expect(cells.every((c) => c.level === 0)).toBe(true);
  });

  test("places sparse buckets and assigns levels", () => {
    const cells = buildCells(
      buckets({
        "2025-10-20": 1,
        "2025-10-21": 24,
        "2025-10-22": 100,
        "2025-10-10": 60,
      }),
      todayKey,
    );
    const by = (d: string) => cells.find((c) => c.date === d);
    expect(by("2025-10-20")?.level).toBe(1);
    expect(by("2025-10-21")?.level).toBe(1);
    expect(by("2025-10-10")?.level).toBe(3);
    expect(by("2025-10-22")?.level).toBe(4);
    expect(by("2025-10-19")?.level).toBe(0);
  });

  test("single bucket gets the top level", () => {
    const cells = buildCells(buckets({ "2025-10-22": 1 }), todayKey);
    expect(cells[cells.length - 1].level).toBe(4);
  });
});

describe("computeStats", () => {
  test("counts totals, streak, and busiest day", () => {
    const cells = buildCells(
      buckets({
        "2025-10-22": 2,
        "2025-10-21": 1,
        "2025-10-20": 5,
        "2025-10-01": 3,
      }),
      todayKey,
    );
    expect(computeStats(cells, todayKey)).toEqual({
      total: 11,
      activeDays: 4,
      streak: 3,
      busiest: { date: "2025-10-20", count: 5 },
    });
  });

  test("streak counts from yesterday when today is empty", () => {
    const cells = buildCells(
      buckets({ "2025-10-21": 1, "2025-10-20": 1, "2025-10-18": 1 }),
      todayKey,
    );
    expect(computeStats(cells, todayKey).streak).toBe(2);
  });

  test("empty input yields zeros", () => {
    const stats = computeStats(buildCells([], todayKey), todayKey);
    expect(stats).toEqual({
      total: 0,
      activeDays: 0,
      streak: 0,
      busiest: undefined,
    });
  });
});

describe("zonedDayKey", () => {
  test("uses the given IANA zone", () => {
    const instant = new Date(Date.UTC(2025, 9, 22, 20, 0));
    expect(zonedDayKey(instant, "UTC")).toBe("2025-10-22");
    expect(zonedDayKey(instant, "Australia/Melbourne")).toBe("2025-10-23");
  });
});

describe("cellLabel", () => {
  test("pluralises", () => {
    const c = { date: "2025-10-22", col: 0, row: 0, level: 0 };
    expect(cellLabel({ ...c, count: 0 })).toBe("No changes on Wed, Oct 22");
    expect(cellLabel({ ...c, count: 1 })).toBe("1 change on Wed, Oct 22");
    expect(cellLabel({ ...c, count: 3 })).toBe("3 changes on Wed, Oct 22");
  });
});

describe("ActivityCalendar", () => {
  test("renders grid, stats, and labels", () => {
    const { container, getByText } = render(
      <ActivityCalendar
        buckets={buckets({ "2025-10-22": 2, "2025-10-21": 1 })}
        timeZone="UTC"
        today={today}
      />,
    );
    const todayRect = container.querySelector('rect[data-date="2025-10-22"]');
    expect(todayRect?.getAttribute("class")).toContain("activity-cell-today");
    expect(todayRect?.getAttribute("data-level")).toBe("4");
    expect(container.querySelectorAll("rect[data-date]").length).toBe(368);
    expect(
      container.querySelector("svg[role=img]")?.getAttribute("aria-label"),
    ).toBe("Activity calendar: 3 changes in the last year");
    expect(getByText("Changes (last year)")).toBeTruthy();
    expect(getByText("Current streak")).toBeTruthy();
    expect(getByText("Oct 22 (2)")).toBeTruthy();
    expect(getByText("Nov")).toBeTruthy();
    expect(getByText("Dec")).toBeTruthy();
    expect(
      [...container.querySelectorAll(".activity-label")].filter(
        (l) => l.textContent === "Oct",
      ).length,
    ).toBe(1);
  });

  test("shows hovered cell readout", () => {
    const { container, getByText } = render(
      <ActivityCalendar
        buckets={buckets({ "2025-10-22": 2 })}
        timeZone="UTC"
        today={today}
      />,
    );
    const rect = container.querySelector('rect[data-date="2025-10-22"]');
    expect(rect).not.toBeNull();
    fireEvent.mouseEnter(rect as Element);
    expect(
      getByText("2 changes on Wed, Oct 22", { selector: ".activity-readout" }),
    ).toBeTruthy();
  });

  test("empty project shows dashes and zeros", () => {
    const { getByText } = render(
      <ActivityCalendar buckets={[]} timeZone="UTC" today={today} />,
    );
    expect(getByText("-")).toBeTruthy();
  });
});
