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
        succeeded: "bg-emerald-50 text-emerald-900 ring-emerald-200",
        failed: "bg-rose-50 text-rose-900 ring-rose-200",
        running: "bg-cyan-50 text-cyan-900 ring-cyan-200",
        partially_applied: "bg-amber-50 text-amber-950 ring-amber-300",
        unknown: "bg-amber-50 text-amber-950 ring-amber-300",
      } as Record<string, string>
    )[status] ?? "bg-slate-100 text-slate-700 ring-slate-200"
  );
}

export function executePreconditions(draft: Draft, currentVersion: number) {
  return {
    expected_revision: draft.revision,
    expected_version: currentVersion,
    expected_sql_hash: draft.sql_hash,
  };
}

export function isAttempt(value: unknown): value is Attempt {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as Attempt).id === "string" &&
    typeof (value as Attempt).status === "string"
  );
}

export function planIsStale(plannedSQL: string | undefined, currentSQL: string): boolean {
  return plannedSQL !== undefined && plannedSQL !== currentSQL;
}
import type { Attempt, Draft } from "./types";
