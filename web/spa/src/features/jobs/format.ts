import type { JobEffect, JobStatus, JobView, RecoveryOperation } from "./types";

// Polling stops only once the worker will not deliver the job again.
export function jobActive(job: Pick<JobView, "terminal"> | undefined): boolean {
  return job !== undefined && !job.terminal;
}

export function jobStatusLabel(status: JobStatus, effect: JobEffect): string {
  if (status === "blocked")
    return effect === "partial" ? "Partially applied" : "Outcome unknown – reconciling";
  return {
    queued: "Queued",
    running: "Running",
    succeeded: "Succeeded",
    failed: "Failed",
  }[status];
}

export function jobStatusClass(status: JobStatus): string {
  return {
    queued: "bg-slate-100 text-slate-700 ring-slate-200",
    running: "bg-cyan-50 text-cyan-900 ring-cyan-200",
    succeeded: "bg-emerald-50 text-emerald-900 ring-emerald-200",
    failed: "bg-rose-50 text-rose-900 ring-rose-200",
    blocked: "bg-amber-50 text-amber-900 ring-amber-300",
  }[status];
}

// What the target actually holds, independent of how delivery ended.
export function effectLabel(effect: JobEffect): string {
  return {
    none: "No target changes",
    committed: "Committed",
    partial: "Some batches committed",
    unknown: "Not yet confirmed",
  }[effect];
}

export function jobSummary(job: JobView): string {
  switch (job.status) {
    case "queued":
      if (job.blocked_by)
        return "Accepted and waiting: this database is blocked until the earlier job is recovered.";
      return job.attempts > 0
        ? `Accepted and waiting to retry (attempt ${job.attempts + 1} of ${job.max_attempts}).`
        : "Accepted and waiting for a worker. Nothing has run yet.";
    case "running":
      return "A worker holds this database and is executing the job.";
    case "succeeded":
      return "The worker confirmed the whole action committed.";
    case "failed":
      return job.effect === "none"
        ? "The job failed and the target was left unchanged."
        : "The job failed.";
    case "blocked":
      return job.effect === "partial"
        ? "Some batches committed before the job stopped. Later migrations on this database wait for an approved resume or compensation."
        : "The worker could not confirm the outcome. It checks the target receipt before anything else runs on this database.";
  }
}

export function progressLabel(
  job: Pick<JobView, "committed_batches" | "committed_rows" | "total_rows">,
): string {
  const batches = `${job.committed_batches.toLocaleString("en-US")} ${job.committed_batches === 1 ? "batch" : "batches"}`;
  const rows =
    job.total_rows == null
      ? `${job.committed_rows.toLocaleString("en-US")} rows`
      : `${job.committed_rows.toLocaleString("en-US")} of ${job.total_rows.toLocaleString("en-US")} rows`;
  return `${batches} · ${rows}`;
}

// Percent of the captured scope committed, or undefined when not a batch job.
export function progressPercent(
  job: Pick<JobView, "committed_rows" | "total_rows">,
): number | undefined {
  if (job.total_rows == null) return undefined;
  if (job.total_rows === 0) return 100;
  return Math.min(100, Math.floor((job.committed_rows / job.total_rows) * 100));
}

export function recoveryLabel(op: RecoveryOperation): string {
  return op === "resume" ? "Request resume" : "Request compensation";
}

export function operationName(op: string): string {
  return (
    {
      up: "Apply",
      rollback: "Rollback",
      forward: "Forward",
      resume: "Resume",
      compensate: "Compensate",
    }[op] ?? op
  );
}

export function shortID(id: string | undefined): string {
  return id ? id.slice(0, 12) : "—";
}
