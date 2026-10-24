import { describe, expect, test } from "bun:test";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  dayKey,
  formatDayLabel,
  formatSinceLabel,
  formatTime,
  formatTimestamp,
} from "./time";

describe("time helpers", () => {
  const now = new Date(2026, 9, 24, 15, 30);

  test("dayKey uses the local calendar day", () => {
    expect(dayKey(new Date(2026, 0, 5, 23, 59))).toBe("2026-01-05");
    expect(dayKey(new Date(2026, 11, 31, 0, 0))).toBe("2026-12-31");
  });

  test("formatDayLabel names today and yesterday", () => {
    expect(formatDayLabel(new Date(2026, 9, 24, 1, 0), now)).toBe("Today");
    expect(formatDayLabel(new Date(2026, 9, 23, 23, 0), now)).toBe("Yesterday");
  });

  test("formatDayLabel adds the year only outside the current year", () => {
    expect(formatDayLabel(new Date(2026, 9, 22), now)).toBe("Oct 22");
    expect(formatDayLabel(new Date(2025, 9, 22), now)).toBe("Oct 22, 2025");
  });

  test("formatDayLabel handles yesterday across a month boundary", () => {
    const first = new Date(2026, 10, 1, 9, 0);
    expect(formatDayLabel(new Date(2026, 9, 31, 20, 0), first)).toBe(
      "Yesterday",
    );
  });

  test("formatTime renders 24h local time", () => {
    const date = new Date(2026, 9, 24, 7, 5);
    expect(formatTime(timestampFromDate(date))).toBe("07:05");
    expect(formatTime(undefined)).toBe("-");
  });

  test("formatTimestamp renders month, day and 24h local time", () => {
    const date = new Date(2026, 5, 7, 17, 25);
    expect(formatTimestamp(timestampFromDate(date))).toBe("Jun 7, 17:25");
    expect(formatTimestamp(timestampFromDate(new Date(2026, 5, 7, 0, 5)))).toBe(
      "Jun 7, 00:05",
    );
    expect(formatTimestamp(undefined)).toBe("-");
  });

  test("formatSinceLabel shares the 24h convention", () => {
    expect(formatSinceLabel(new Date(2026, 5, 7, 17, 25))).toBe(
      "New since Jun 7, 17:25",
    );
  });
});
