import { expect, test } from "vite-plus/test";
import {
  executePreconditions,
  formatDate,
  isAttempt,
  planIsStale,
  shortHash,
  statusLabel,
  versionLabel,
} from "../src/features/migrations/format";
import type { Draft } from "../src/features/migrations/types";

test("migration dates use the server display format and unfinished dates stay explicit", () => {
  expect(formatDate("2026-11-05T09:07:06Z")).toBe("2026-11-05 09:07:06 UTC");
  expect(formatDate(null)).toBe("N/A");
  expect(formatDate("not a date")).toBe("N/A");
});

test("execution preconditions preserve the API field names and saved SQL hash", () => {
  const draft = { revision: 7, sql_hash: "saved-hash" } as Draft;
  expect(executePreconditions(draft, 12)).toEqual({
    expected_revision: 7,
    expected_version: 12,
    expected_sql_hash: "saved-hash",
  });
});

test("failed plan requests cannot make a prior plan appear current and error attempts are narrowed", () => {
  expect(planIsStale("old SQL", "edited SQL")).toBe(true);
  expect(planIsStale("edited SQL", "edited SQL")).toBe(false);
  expect(isAttempt({ id: "attempt", status: "failed" })).toBe(true);
  expect(isAttempt({ id: 3, status: "failed" })).toBe(false);
  expect(isAttempt(undefined)).toBe(false);
});

test("migration history labels match the server helpers", () => {
  expect(shortHash("1234567890abcdef")).toBe("1234567890ab");
  expect(versionLabel(0)).toBe("none");
  expect(versionLabel(4)).toBe("v4");
  expect(statusLabel("partially_applied")).toBe("Partially applied");
  expect(statusLabel("unknown")).toBe("Outcome unknown");
});
