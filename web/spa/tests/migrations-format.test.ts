import { expect, test } from "vite-plus/test";
import {
  formatDate,
  jobFinished,
  outcomeLabel,
  planIsStale,
  requestPreconditions,
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

test("review requests pin the saved definition, revision and expected version", () => {
  const draft = { revision: 7, definition_hash: "definition-hash", sql_hash: "up-only" } as Draft;
  expect(requestPreconditions(draft, 12, "rollback")).toEqual({
    operation: "rollback",
    expected_revision: 7,
    expected_definition_hash: "definition-hash",
    expected_version: 12,
  });
});

test("stale plans are flagged and only finished jobs stop polling", () => {
  expect(planIsStale("old SQL", "edited SQL")).toBe(true);
  expect(planIsStale("edited SQL", "edited SQL")).toBe(false);
  expect(jobFinished({ status: "queued" })).toBe(false);
  expect(jobFinished({ status: "running" })).toBe(false);
  expect(jobFinished({ status: "failed" })).toBe(true);
  expect(jobFinished(undefined)).toBe(false);
});

test("migration history labels match the server helpers", () => {
  expect(shortHash("1234567890abcdef")).toBe("1234567890ab");
  expect(versionLabel(0)).toBe("none");
  expect(versionLabel(4)).toBe("v4");
  expect(statusLabel("partially_applied")).toBe("Partially applied");
  expect(statusLabel("unknown")).toBe("Outcome unknown");
  expect(outcomeLabel({ operation: "rollback", applied_version: 2 })).toBe("Rolled back as v2");
});
