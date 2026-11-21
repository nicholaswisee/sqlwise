import { expect, test } from "vite-plus/test";
import {
  effectLabel,
  jobActive,
  jobStatusLabel,
  jobSummary,
  progressLabel,
  progressPercent,
} from "../src/features/jobs/format";
import type { JobView } from "../src/features/jobs/types";

const job = (over: Partial<JobView> = {}): JobView => ({
  job_id: "job-1",
  database_id: "db-1",
  migration_id: "m-1",
  request_id: "r-1",
  execution_id: "e-1",
  operation: "up",
  status: "queued",
  effect: "none",
  terminal: false,
  attempts: 0,
  max_attempts: 3,
  committed_batches: 0,
  committed_rows: 0,
  total_rows: null,
  recovery: [],
  queued_at: "2026-11-20T10:00:00Z",
  updated_at: "2026-11-20T10:00:00Z",
  ...over,
});

test("polling follows the server's terminal flag, not the status name", () => {
  expect(jobActive(job())).toBe(true);
  expect(jobActive(job({ status: "blocked", effect: "unknown", terminal: false }))).toBe(true);
  expect(jobActive(job({ status: "blocked", effect: "partial", terminal: true }))).toBe(false);
  expect(jobActive(undefined)).toBe(false);
});

test("an accepted job never reads as success", () => {
  expect(jobStatusLabel("queued", "none")).toBe("Queued");
  expect(jobSummary(job())).toContain("Nothing has run yet");
  expect(jobSummary(job({ attempts: 1 }))).toContain("attempt 2 of 3");
  expect(jobSummary(job({ blocked_by: "job-0" }))).toContain("blocked");
});

test("partial and unknown effects are named apart from failure", () => {
  expect(jobStatusLabel("blocked", "partial")).toBe("Partially applied");
  expect(jobStatusLabel("blocked", "unknown")).toContain("unknown");
  expect(effectLabel("partial")).toBe("Some batches committed");
  expect(jobSummary(job({ status: "failed", effect: "none" }))).toContain("unchanged");
});

test("batch progress comes from committed counts", () => {
  expect(progressPercent(job())).toBeUndefined();
  expect(progressPercent(job({ committed_rows: 200, total_rows: 500 }))).toBe(40);
  expect(progressPercent(job({ committed_rows: 0, total_rows: 0 }))).toBe(100);
  expect(progressLabel(job({ committed_batches: 2, committed_rows: 2000, total_rows: 5000 }))).toBe(
    "2 batches · 2,000 of 5,000 rows",
  );
  expect(progressLabel(job({ committed_batches: 1, committed_rows: 7 }))).toBe("1 batch · 7 rows");
});
