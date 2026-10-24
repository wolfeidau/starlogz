import { describe, expect, test } from "bun:test";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { toGroups } from "./log_groups";

const now = new Date(2026, 9, 24, 15, 30);

function at(id: string, date?: Date): Insight {
  return {
    id,
    updatedAt: date ? timestampFromDate(date) : undefined,
  } as Insight;
}

describe("toGroups", () => {
  test("groups consecutive same-day items with their list index", () => {
    const groups = toGroups(
      [
        at("a", new Date(2026, 9, 24, 14, 0)),
        at("b", new Date(2026, 9, 24, 9, 0)),
      ],
      false,
      now,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0].label).toBe("Today");
    expect(groups[0].items.map(([i, n]) => [i.id, n])).toEqual([
      ["a", 0],
      ["b", 1],
    ]);
  });

  test("labels Today and Yesterday and starts a group when the day changes", () => {
    const groups = toGroups(
      [
        at("a", new Date(2026, 9, 24, 14, 0)),
        at("b", new Date(2026, 9, 23, 22, 0)),
        at("c", new Date(2026, 9, 20, 8, 0)),
      ],
      false,
      now,
    );
    expect(groups.map((g) => g.label)).toEqual([
      "Today",
      "Yesterday",
      "Oct 20",
    ]);
    expect(groups.map((g) => g.items[0][1])).toEqual([0, 1, 2]);
  });

  test("puts undated items in an unknown-date group", () => {
    const groups = toGroups(
      [at("a", new Date(2026, 9, 24, 14, 0)), at("b"), at("c")],
      false,
      now,
    );
    expect(groups.map((g) => g.label)).toEqual(["Today", "Unknown date"]);
    expect(groups[1].items).toHaveLength(2);
  });

  test("search mode returns one ungrouped group in order", () => {
    const groups = toGroups(
      [
        at("a", new Date(2026, 9, 20, 14, 0)),
        at("b", new Date(2026, 9, 24, 9, 0)),
      ],
      true,
      now,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0].label).toBe("");
    expect(groups[0].items.map(([i]) => i.id)).toEqual(["a", "b"]);
  });
});
