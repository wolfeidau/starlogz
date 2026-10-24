import { describe, expect, test } from "bun:test";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { newSinceIndex, readLastSeen, writeLastSeen } from "./last_seen";

const at = (iso: string) => ({ updatedAt: timestampFromDate(new Date(iso)) });

describe("newSinceIndex", () => {
  const seen = new Date("2026-10-21T14:00:00Z");

  test("returns null without a prior visit", () => {
    expect(newSinceIndex([at("2026-10-22T00:00:00Z")], null)).toBeNull();
  });

  test("returns null when all insights are newer", () => {
    const list = [at("2026-10-23T00:00:00Z"), at("2026-10-22T00:00:00Z")];
    expect(newSinceIndex(list, seen)).toBeNull();
  });

  test("returns null when none are newer", () => {
    const list = [at("2026-10-21T14:00:00Z"), at("2026-10-20T00:00:00Z")];
    expect(newSinceIndex(list, seen)).toBeNull();
  });

  test("returns the first older-or-equal index when mixed", () => {
    const list = [
      at("2026-10-23T00:00:00Z"),
      at("2026-10-22T00:00:00Z"),
      at("2026-10-21T14:00:00Z"),
      at("2026-10-20T00:00:00Z"),
    ];
    expect(newSinceIndex(list, seen)).toBe(2);
  });
});

describe("readLastSeen", () => {
  test("keeps the first baseline for the tab session after a write", () => {
    window.sessionStorage.clear();
    const previous = new Date("2026-10-21T14:00:00Z");
    writeLastSeen("pinned", previous);
    expect(readLastSeen("pinned")).toEqual(previous);
    writeLastSeen("pinned", new Date("2026-10-24T09:00:00Z"));
    expect(readLastSeen("pinned")).toEqual(previous);
  });
});
