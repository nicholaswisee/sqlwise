export function formatDate(value: string | null | undefined): string {
  if (!value) return "N/A";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "N/A";
  return `${date.toISOString().slice(0, 19).replace("T", " ")} UTC`;
}

export function shortHash(value: string): string {
  return value.slice(0, 12);
}
export function versionLabel(value: number): string {
  return value === 0 ? "none" : `v${value}`;
}
export function statusLabel(status: string): string {
  return (
    (
      {
        running: "Running",
        succeeded: "Succeeded",
        failed: "Failed, rolled back",
        partially_applied: "Partially applied",
        unknown: "Outcome unknown",
      } as Record<string, string>
    )[status] ?? "Not executed"
  );
}
export function statusClass(status: string): string {
  return (
    (
      {
        succeeded: "bg-emerald-50 text-emerald-800 ring-emerald-200",
        failed: "bg-rose-50 text-rose-800 ring-rose-200",
        running: "bg-sky-50 text-sky-800 ring-sky-200",
        partially_applied: "bg-amber-50 text-amber-900 ring-amber-300",
        unknown: "bg-amber-50 text-amber-900 ring-amber-300",
      } as Record<string, string>
    )[status] ?? "bg-slate-100 text-slate-700 ring-slate-200"
  );
}

export function requestPreconditions(draft: Draft, currentVersion: number, operation: Operation) {
  return {
    operation,
    expected_revision: draft.revision,
    expected_definition_hash: draft.definition_hash,
    expected_version: currentVersion,
  };
}

export function operationLabel(operation: Operation): string {
  return { up: "Apply", rollback: "Roll back", forward: "Forward" }[operation];
}
export function outcomeLabel(attempt: Pick<Attempt, "operation" | "applied_version">): string {
  const verb = { up: "Applied", rollback: "Rolled back", forward: "Forwarded" }[attempt.operation];
  return `${verb} as ${versionLabel(attempt.applied_version)}`;
}
export function requestStatusLabel(status: Request["status"]): string {
  return {
    draft: "Not submitted",
    pending_review: "Waiting for review",
    approved: "Approved",
    rejected: "Rejected",
    queued: "Queued for execution",
  }[status];
}
export function jobFinished(job: Pick<Job, "status"> | undefined): boolean {
  // A blocked job needs recovery or reconciliation; polling it here never ends.
  return job?.status === "succeeded" || job?.status === "failed" || job?.status === "blocked";
}

export function planIsStale(plannedSQL: string | undefined, currentSQL: string): boolean {
  return plannedSQL !== undefined && plannedSQL !== currentSQL;
}
import type { Attempt, Draft, Job, Operation, Request } from "./types";
