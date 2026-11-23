import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import { styles } from "../../components/styles";
import { WorkspaceShell } from "../../components/workspace";
import { PlanPanel } from "../queryplan/views";
import type { ExplainRequest, QueryPlan } from "../queryplan/types";
import type {
  Attempt,
  BatchTemplate,
  Detail,
  Draft,
  History,
  Job,
  Operation,
  Proposal,
  Request,
} from "./types";
import {
  formatDate,
  jobFinished,
  isRecovery,
  operationLabel,
  outcomeLabel,
  requestTitle,
  planIsStale,
  requestPreconditions,
  requestStatusLabel,
  shortHash,
  statusClass,
  statusLabel,
  versionLabel,
} from "./format";

const snapshotExample = `{
  "dialect": "postgresql",
  "schema": "public",
  "tables": [
    {
      "name": "customers",
      "primaryKey": ["id"],
      "columns": [
        {"name": "id", "type": "bigint", "nullable": false},
        {"name": "email", "type": "varchar(320)", "nullable": false},
        {"name": "created_at", "type": "timestamptz", "nullable": false, "default": "now()"}
      ]
    }
  ]
}`;

const path = (db: string, tail: string) => `/databases/${encodeURIComponent(db)}/${tail}`;
const statusChip =
  "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ring-1";
const keys = {
  // Prefix of every history query, whichever rollback filter it uses.
  history: (db: string) => ["migrations", db] as const,
  detail: (db: string, id: string) => ["migration", db, id] as const,
  attempt: (db: string, id: string, attempt: string) =>
    ["migration-attempt", db, id, attempt] as const,
};
function ErrorBox({ error, className = "mt-4" }: { error: unknown; className?: string }) {
  if (!error) return null;
  return (
    <p role="alert" className={`${styles.alert} ${className}`}>
      {error instanceof ApiError
        ? error.message
        : "The request could not be completed. Check your connection and try again."}
    </p>
  );
}
function Status({ value }: { value: string }) {
  return <span className={`${statusChip} ${statusClass(value)}`}>{statusLabel(value)}</span>;
}
function SQL({ children }: { children: string }) {
  return (
    <pre className="max-h-[32rem] max-w-full overflow-auto whitespace-pre rounded-lg border border-slate-200 bg-slate-50 p-4 font-mono text-sm leading-6 text-slate-900">
      <code>{children}</code>
    </pre>
  );
}
function Shell({
  workspace,
  active,
  title,
  children,
}: {
  workspace: History["workspace"];
  active: "migrations" | "schema-diff";
  title: string;
  children: ReactNode;
}) {
  const { session } = useAuthenticatedContext();
  useEffect(() => {
    document.title = `${title} | ${workspace.name} | sqlwise`;
  }, [title, workspace.name]);
  return (
    <WorkspaceShell workspace={workspace} username={session.user.username} active={active}>
      {children}
    </WorkspaceShell>
  );
}
function useHistory(db: string, hideRollbacks = false) {
  const { api } = useAuthenticatedContext();
  return useQuery({
    queryKey: [...keys.history(db), { hideRollbacks }],
    queryFn: ({ signal }) =>
      api.request<History>(
        `/databases/${encodeURIComponent(db)}/migrations${hideRollbacks ? "?hide_rollback=true" : ""}`,
        { signal },
      ),
    placeholderData: (previous) => previous,
  });
}
const emptyBatch: BatchTemplate = {
  schema: "public",
  table: "",
  key: "id",
  column: "",
  value: "",
  batch_size: 1000,
};
// Large-data mode: one reversible UPDATE run as checkpointed batches.
function BatchFields({
  value,
  onChange,
}: {
  value: BatchTemplate | null;
  onChange: (value: BatchTemplate | null) => void;
}) {
  const field = (name: keyof BatchTemplate, label: string, help?: string) => (
    <div className="min-w-0 space-y-1.5">
      <label className={styles.label} htmlFor={`batch-${name}`}>
        {label}
      </label>
      <input
        id={`batch-${name}`}
        required={name !== "value"}
        type={name === "batch_size" ? "number" : "text"}
        min={name === "batch_size" ? 1 : undefined}
        max={name === "batch_size" ? 100000 : undefined}
        spellCheck={false}
        autoCapitalize="none"
        value={value?.[name] ?? ""}
        onChange={(e) =>
          value &&
          onChange({
            ...value,
            [name]: name === "batch_size" ? Number(e.target.value) : e.target.value,
          })
        }
        aria-describedby={help ? `batch-${name}-help` : undefined}
        className={styles.input}
      />
      {help && (
        <p id={`batch-${name}-help`} className="text-xs leading-5 text-slate-600">
          {help}
        </p>
      )}
    </div>
  );
  return (
    <fieldset className="space-y-3">
      <legend className={styles.label}>Large data</legend>
      <label className="flex min-h-11 items-center gap-2 text-sm text-slate-800">
        <input
          type="checkbox"
          className="size-4 accent-cyan-700"
          checked={value !== null}
          onChange={(e) => onChange(e.target.checked ? emptyBatch : null)}
        />
        Run as checkpointed batches (UPDATE one column)
      </label>
      {value && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {field("schema", "Schema")}
          {field("table", "Table")}
          {field("key", "Batch key", "A NOT NULL, uniquely indexed integer column.")}
          {field("column", "Column to update")}
          {field("value", "New value", "Cast to the column type on the target.")}
          {field("batch_size", "Rows per batch")}
        </div>
      )}
    </fieldset>
  );
}
type Rollback = Pick<Draft, "sql_down" | "irreversible" | "irreversible_reason">;
const noRollback: Rollback = { sql_down: "", irreversible: false, irreversible_reason: "" };
const codeArea =
  "w-full rounded-lg border border-slate-300 bg-slate-50 p-3 font-mono text-sm leading-6 text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700";
function RollbackFields({
  id,
  value,
  onChange,
}: {
  id: string;
  value: Rollback;
  onChange: (value: Rollback) => void;
}) {
  return (
    <fieldset className="space-y-3">
      <legend className={styles.label}>Rollback</legend>
      <label className="flex min-h-11 items-center gap-2 text-sm text-slate-800">
        <input
          type="checkbox"
          className="size-4 accent-cyan-700"
          checked={value.irreversible}
          onChange={(e) => onChange({ ...value, irreversible: e.target.checked })}
        />
        This migration cannot be rolled back
      </label>
      {value.irreversible ? (
        <div className="space-y-1.5">
          <label className={styles.label} htmlFor={`${id}-reason`}>
            Why it cannot be rolled back
          </label>
          <input
            id={`${id}-reason`}
            required
            maxLength={500}
            value={value.irreversible_reason}
            onChange={(e) => onChange({ ...value, irreversible_reason: e.target.value })}
            className={styles.input}
          />
        </div>
      ) : (
        <div className="space-y-1.5">
          <label className={styles.label} htmlFor={`${id}-down`}>
            SQL down
          </label>
          <p id={`${id}-down-help`} className="text-xs leading-5 text-slate-600">
            Runs when this migration is rolled back. Required before execution is requested.
          </p>
          <textarea
            id={`${id}-down`}
            aria-describedby={`${id}-down-help`}
            rows={6}
            maxLength={262144}
            spellCheck={false}
            autoCapitalize="none"
            value={value.sql_down}
            onChange={(e) => onChange({ ...value, sql_down: e.target.value })}
            className={codeArea}
          />
        </div>
      )}
    </fieldset>
  );
}
function AttemptsTable({
  db,
  attempts,
  showMigration,
  empty = "No executions yet.",
}: {
  db: string;
  attempts: Attempt[];
  showMigration: boolean;
  empty?: string;
}) {
  if (!attempts.length)
    return (
      <p className="mt-3 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
        {empty}
      </p>
    );
  return (
    <div className={`${styles.panel} mt-3 overflow-x-auto`}>
      <table className="w-full min-w-[44rem] text-left text-sm tabular-nums">
        <thead>
          <tr>
            {[
              "Version",
              "Operation",
              "Status",
              ...(showMigration ? ["Migration"] : []),
              "Executor",
              "Started",
              "Finished",
              "Details",
            ].map((h) => (
              <th
                key={h}
                scope="col"
                className="relative whitespace-nowrap border-b border-slate-200 bg-slate-50 px-4 py-2.5 text-xs font-semibold text-slate-600"
              >
                {h === "Details" ? <span className="sr-only">{h}</span> : h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {attempts.map((a) => (
            <tr key={a.id}>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top whitespace-nowrap font-mono text-slate-900">
                {a.applied_version > 0 ? (
                  versionLabel(a.applied_version)
                ) : (
                  <span className="text-slate-600">
                    candidate {versionLabel(a.candidate_version)}
                  </span>
                )}
              </td>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top whitespace-nowrap text-slate-900">
                {operationLabel(a.operation)}
              </td>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top">
                <Status value={a.status} />
                {a.error && (
                  <p className="mt-1.5 max-w-md break-words text-xs leading-5 text-slate-600">
                    {a.error}
                  </p>
                )}
              </td>
              {showMigration && (
                <td className="border-b border-slate-100 px-4 py-2.5 align-top break-words">
                  <Link
                    className={styles.link}
                    to={path(db, `migrations/${encodeURIComponent(a.migration_id)}`) as never}
                  >
                    {a.migration_title}
                  </Link>
                </td>
              )}
              <td className="border-b border-slate-100 px-4 py-2.5 align-top break-all text-slate-900">
                {a.executor_name}
              </td>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top whitespace-nowrap font-mono text-slate-700">
                {formatDate(a.started_at)}
              </td>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top whitespace-nowrap font-mono text-slate-700">
                {formatDate(a.finished_at)}
              </td>
              <td className="border-b border-slate-100 px-4 py-2.5 align-top whitespace-nowrap">
                <Link
                  className={styles.link}
                  to={
                    path(
                      db,
                      `migrations/${encodeURIComponent(a.migration_id)}/attempts/${encodeURIComponent(a.id)}`,
                    ) as never
                  }
                >
                  SQL and details
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function MigrationHistoryPage({ db }: { db: string }) {
  const { api } = useAuthenticatedContext();
  const [hideRollbacks, setHideRollbacks] = useState(false);
  const query = useHistory(db, hideRollbacks);
  const [title, setTitle] = useState("");
  const [sql, setSQL] = useState("");
  const [rollback, setRollback] = useState(noRollback);
  const [batch, setBatch] = useState<BatchTemplate | null>(null);
  const [error, setError] = useState<unknown>();
  const navigate = useNavigate();
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: (input: { title: string; sql: string; batch: BatchTemplate | null } & Rollback) =>
      api.request<Draft>(`/databases/${encodeURIComponent(db)}/migrations`, {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: async (draft) => {
      setError(undefined);
      setTitle("");
      setSQL("");
      setRollback(noRollback);
      setBatch(null);
      await client.invalidateQueries({ queryKey: keys.history(db) });
      await navigate({ to: path(db, `migrations/${encodeURIComponent(draft.id)}`) as never });
    },
    onError: setError,
  });
  if (query.isPending)
    return (
      <main className="mx-auto max-w-7xl p-6" role="status">
        Loading migrations…
      </main>
    );
  if (query.isError)
    return (
      <main className="mx-auto max-w-7xl p-6">
        <h1 className={styles.heading}>Migrations</h1>
        <ErrorBox error={query.error} />
        <button className={`${styles.button} mt-4`} onClick={() => void query.refetch()}>
          Retry
        </button>
      </main>
    );
  const data = query.data;
  return (
    <Shell workspace={data.workspace} active="migrations" title="Migrations">
      <div>
        <h1 className={styles.heading}>Migrations</h1>
        <p className={`${styles.muted} mt-1`}>
          Current applied version:{" "}
          <strong className="font-mono font-semibold text-slate-900">
            {versionLabel(data.current_version)}
          </strong>
        </p>
      </div>
      {data.workspace.can_edit && (
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section
            aria-labelledby="new-heading"
            className={`${styles.panel} min-w-0 flex-[999_1_560px] p-5 sm:p-6`}
          >
            <h2 id="new-heading" className="text-lg font-semibold text-slate-900">
              New migration
            </h2>
            <p className={`${styles.muted} mt-1 leading-6`}>
              Saving creates a draft. Nothing runs until a review request is approved and executed.
            </p>
            <form
              className="mt-5 space-y-4"
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                setError(undefined);
                create.mutate({ title, sql: batch ? "" : sql, batch, ...rollback });
              }}
            >
              <div className="space-y-1.5">
                <label className={styles.label} htmlFor="new-title">
                  Title
                </label>
                <input
                  id="new-title"
                  required
                  maxLength={200}
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  className={styles.input}
                />
              </div>
              <BatchFields value={batch} onChange={setBatch} />
              {!batch && (
                <div className="space-y-1.5">
                  <label className={styles.label} htmlFor="new-sql">
                    SQL
                  </label>
                  <textarea
                    id="new-sql"
                    required
                    rows={14}
                    maxLength={262144}
                    spellCheck={false}
                    autoCapitalize="none"
                    value={sql}
                    onChange={(e) => setSQL(e.target.value)}
                    className={codeArea}
                  />
                </div>
              )}
              <RollbackFields id="new" value={rollback} onChange={setRollback} />
              <ErrorBox error={error} />
              <button className={styles.primary} disabled={create.isPending}>
                {create.isPending ? "Saving…" : "Save draft"}
              </button>
            </form>
          </section>
          <section aria-labelledby="drafts-heading" className="min-w-0 flex-[1_1_360px]">
            <h2 id="drafts-heading" className="text-lg font-semibold text-slate-900">
              Drafts
            </h2>
            {data.drafts.length === 0 ? (
              <p className="mt-3 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
                No migrations yet.
              </p>
            ) : (
              <ul className={`${styles.panel} mt-3 divide-y divide-slate-100`}>
                {data.drafts.map((d) => (
                  <li
                    key={d.id}
                    className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2 px-4 py-3"
                  >
                    <div className="min-w-0">
                      <Link
                        to={path(db, `migrations/${encodeURIComponent(d.id)}`) as never}
                        className={`${styles.link} break-words font-medium`}
                      >
                        {d.title}
                      </Link>
                      <p className="mt-1 text-xs leading-5 text-slate-600">
                        Revision {d.revision} · updated {formatDate(d.updated_at)} by{" "}
                        {d.updated_by_name}
                      </p>
                    </div>
                    <span
                      className={`${statusChip} ${statusClass(d.applied_version > 0 ? "succeeded" : d.last_status)}`}
                    >
                      {d.applied_version > 0
                        ? `Applied as ${versionLabel(d.applied_version)}`
                        : d.executed && d.last_status
                          ? `Draft, last attempt: ${statusLabel(d.last_status)}`
                          : "Draft"}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      )}
      {data.head.pending.length > 0 && (
        <p className="mt-6 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950">
          {data.head.pending.length} rolled-back{" "}
          {data.head.pending.length === 1 ? "migration waits" : "migrations wait"} to be forwarded.
          No new migration can be applied until then.
        </p>
      )}
      <section aria-labelledby="committed-heading" className="mt-10">
        <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
          <div>
            <h2 id="committed-heading" className="text-lg font-semibold text-slate-900">
              Committed history
            </h2>
            <p className={`${styles.muted} mt-1`}>
              Successful executions in order. Rollbacks are recorded as new versions, never by
              deleting history.
            </p>
          </div>
          <label className="flex min-h-11 items-center gap-2 text-sm text-slate-800">
            <input
              type="checkbox"
              className="size-4 accent-cyan-700"
              checked={hideRollbacks}
              onChange={(e) => setHideRollbacks(e.target.checked)}
            />
            Hide rollbacks
          </label>
        </div>
        <AttemptsTable
          db={db}
          attempts={[...data.committed].reverse()}
          showMigration
          empty={
            hideRollbacks && data.attempts.some((a) => a.status === "succeeded")
              ? "Every committed migration was rolled back."
              : "No committed migrations yet."
          }
        />
      </section>
      <section aria-labelledby="history-heading" className="mt-10">
        <h2 id="history-heading" className="text-lg font-semibold text-slate-900">
          Execution history
        </h2>
        <p className={`${styles.muted} mt-1`}>
          Every attempt is kept, newest first, including failed ones.
        </p>
        <AttemptsTable db={db} attempts={data.attempts} showMigration />
      </section>
    </Shell>
  );
}

function Result({
  attempt,
  message,
  db,
  migration,
}: {
  attempt?: Attempt;
  message?: string;
  db: string;
  migration: string;
}) {
  if (attempt?.status === "succeeded")
    return (
      <div
        role="status"
        className="mt-6 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-emerald-950"
      >
        <p className="font-semibold">{outcomeLabel(attempt)}.</p>
        <p>
          {attempt.statement_count} statements committed.{" "}
          <Link
            className="underline"
            to={
              path(
                db,
                `migrations/${encodeURIComponent(migration)}/attempts/${encodeURIComponent(attempt.id)}`,
              ) as never
            }
          >
            View attempt
          </Link>
        </p>
      </div>
    );
  return (
    <div role="alert" className={`${styles.alert} mt-6`}>
      <p className="font-semibold">
        {attempt && (
          <>
            <Status value={attempt.status} /> Candidate {versionLabel(attempt.candidate_version)}{" "}
            ·{" "}
          </>
        )}
        {message ?? attempt?.error ?? "The operation could not be completed."}
      </p>
      {attempt && (
        <p className="mt-2">
          <Link
            className={styles.link}
            to={
              path(
                db,
                `migrations/${encodeURIComponent(migration)}/attempts/${encodeURIComponent(attempt.id)}`,
              ) as never
            }
          >
            View attempt
          </Link>{" "}
          ·{" "}
          <Link
            className={styles.link}
            to={path(db, `migrations/${encodeURIComponent(migration)}`) as never}
          >
            Reload migration
          </Link>
        </p>
      )}
      {!attempt && (
        <p className="mt-2">
          <Link
            className={styles.link}
            to={path(db, `migrations/${encodeURIComponent(migration)}`) as never}
          >
            Reload migration
          </Link>
        </p>
      )}
    </div>
  );
}

const requestTone: Record<string, string> = {
  approved: "succeeded",
  rejected: "failed",
  queued: "running",
};
function ReviewPanel({
  data,
  onRequest,
  onAction,
  onExecute,
  busy,
  executing,
  error,
}: {
  data: Detail;
  onRequest: (operation: Operation) => void;
  onAction: (request: Request, action: "submit" | "approve" | "reject", body?: object) => void;
  onExecute: (request: Request) => void;
  busy: boolean;
  executing: boolean;
  error: unknown;
}) {
  const d = data.draft;
  const canEdit = data.workspace.can_edit;
  if (d.dialect !== "postgresql")
    return (
      <section aria-labelledby="review-heading" className={`${styles.panel} p-5 sm:p-6`}>
        <h2 id="review-heading" className="text-lg font-semibold text-slate-900">
          Review and execution
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-700">
          Reviewed execution is available for PostgreSQL targets only.
        </p>
      </section>
    );
  return (
    <section aria-labelledby="review-heading" className={`${styles.panel} p-5 sm:p-6`}>
      <h2 id="review-heading" className="text-lg font-semibold text-slate-900">
        Review and execution
      </h2>
      <p className="mt-2 text-sm leading-6 text-slate-700">
        Every execution needs a request approved by the leader of each group with access to this
        database. Requests use saved revision {d.revision} (definition SHA-256{" "}
        <code className="font-mono text-slate-900">{shortHash(d.definition_hash)}</code>);{" "}
        {d.batch ? (
          <>
            Approved apply requests run in the background as checkpointed batches. Each batch
            commits independently, so earlier batches remain applied if a later batch fails. Other
            operations use their normal transaction behavior.
          </>
        ) : (
          <>Approved requests run in the background in one transaction.</>
        )}
      </p>
      {canEdit &&
        (data.eligible.length > 0 ? (
          <div className="mt-4 flex flex-wrap gap-3">
            {data.eligible.map((operation) => (
              <button
                key={operation}
                type="button"
                disabled={busy || !!data.blocked}
                onClick={() => onRequest(operation)}
                className={styles.button}
              >
                Request {operationLabel(operation).toLowerCase()} as{" "}
                {versionLabel(data.current_version + 1)}
              </button>
            ))}
          </div>
        ) : (
          !d.irreversible &&
          !d.sql_down && (
            <p className="mt-4 text-sm leading-6 text-slate-700">
              Write SQL down, or mark the migration as irreversible, before requesting execution.
            </p>
          )
        ))}
      <ErrorBox error={error} />
      {data.requests.length > 0 && (
        <ul className="mt-5 space-y-4">
          {data.requests.map((r) => (
            <li key={r.id} className="rounded-lg border border-slate-200 p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="font-medium text-slate-900">{requestTitle(r)}</p>
                <span className={`${statusChip} ${statusClass(requestTone[r.status] ?? "")}`}>
                  {requestStatusLabel(r.status)}
                </span>
              </div>
              <p className="mt-1 text-xs leading-5 text-slate-600">
                Requested by {r.requested_by_name} on {formatDate(r.created_at)}
                {r.review_round > 0 && ` · review round ${r.review_round}`}
              </p>
              {r.stale && (
                <p className="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm leading-6 text-amber-950">
                  {r.stale}
                </p>
              )}
              {r.status !== "draft" && r.status !== "queued" && (
                <ul className="mt-3 divide-y divide-slate-100 text-sm">
                  {r.approvals.map((a) => (
                    <li
                      key={a.group_id}
                      className="flex flex-wrap items-center justify-between gap-2 py-2"
                    >
                      <span className="min-w-0 break-words text-slate-900">
                        {a.group_name}{" "}
                        <span className="text-slate-600">
                          ({a.permission}) ·{" "}
                          {a.leader_active && a.leader_name
                            ? `leader ${a.leader_name}`
                            : "no active leader"}
                        </span>
                      </span>
                      {a.can_decide ? (
                        <span className="flex gap-2">
                          <button
                            type="button"
                            disabled={busy}
                            onClick={() => onAction(r, "approve", { group_id: a.group_id })}
                            className={styles.button}
                          >
                            Approve<span className="sr-only"> for {a.group_name}</span>
                          </button>
                          <button
                            type="button"
                            disabled={busy}
                            onClick={() => onAction(r, "reject", { group_id: a.group_id })}
                            className={styles.button}
                          >
                            Reject<span className="sr-only"> for {a.group_name}</span>
                          </button>
                        </span>
                      ) : (
                        <span className="text-slate-700">
                          {a.decision === "approved"
                            ? `Approved ${formatDate(a.decided_at)}`
                            : a.decision === "rejected"
                              ? `Rejected ${formatDate(a.decided_at)}`
                              : "Waiting for the leader"}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {canEdit &&
                (r.status === "draft" ||
                  r.status === "rejected" ||
                  (!!r.stale && (r.status === "pending_review" || r.status === "approved"))) && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => onAction(r, "submit")}
                    className={`${styles.button} mt-3`}
                  >
                    {r.status === "draft" ? "Submit for review" : "Submit for review again"}
                  </button>
                )}
              {canEdit && r.status === "approved" && !r.stale && (
                <button
                  type="button"
                  disabled={busy || (!!data.blocked && !isRecovery(r.operation))}
                  onClick={() => onExecute(r)}
                  className={`${styles.primary} mt-3`}
                >
                  {executing
                    ? "Running in the background…"
                    : isRecovery(r.operation)
                      ? `Execute ${operationLabel(r.operation).toLowerCase()}`
                      : `Execute ${operationLabel(r.operation).toLowerCase()} as ${versionLabel(r.expected_version + 1)}`}
                </button>
              )}
              {r.status === "queued" && (
                <p className="mt-3 flex flex-wrap items-center gap-2 text-sm text-slate-700">
                  {r.execution ? (
                    <>
                      <Status value={r.execution.status} />
                      <Link
                        className={styles.link}
                        to={
                          path(
                            data.workspace.id,
                            `migrations/${encodeURIComponent(r.migration_id)}/attempts/${encodeURIComponent(r.execution.id)}`,
                          ) as never
                        }
                      >
                        View execution
                      </Link>
                    </>
                  ) : (
                    "Queued. The worker runs it in the background."
                  )}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function MigrationDetailPage({ db, id }: { db: string; id: string }) {
  const { api } = useAuthenticatedContext();
  const client = useQueryClient();
  const navigate = useNavigate();
  const query = useQuery({
    queryKey: keys.detail(db, id),
    queryFn: ({ signal }) =>
      api.request<Detail>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}`,
        { signal },
      ),
  });
  const [title, setTitle] = useState<string>();
  const [sql, setSQL] = useState<string>();
  const [rollback, setRollback] = useState<Rollback>();
  const [baseRevision, setBaseRevision] = useState<number>();
  const [editError, setEditError] = useState<unknown>();
  const [result, setResult] = useState<{ attempt?: Attempt; message?: string }>();
  const [plan, setPlan] = useState<{
    result?: QueryPlan;
    requestSQL?: string;
    pending: boolean;
    error?: string;
  }>({ pending: false });
  const routeKey = useRef(`${db}\0${id}`);
  useEffect(() => {
    const next = `${db}\0${id}`;
    if (routeKey.current !== next) {
      routeKey.current = next;
      setTitle(undefined);
      setSQL(undefined);
      setRollback(undefined);
      setBaseRevision(undefined);
      setEditError(undefined);
      setResult(undefined);
      setPlan({ pending: false });
    }
  }, [db, id]);
  const draft = query.data?.draft;
  const editTitle = title ?? draft?.title ?? "";
  const editSQL = sql ?? draft?.sql ?? "";
  const editRollback = rollback ?? draft ?? noRollback;
  // Any execution, even a failed one, freezes the definition.
  const isEditable = !!draft && query.data!.workspace.can_edit && !draft.executed;
  const invalidate = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: keys.history(db) }),
      client.invalidateQueries({ queryKey: keys.detail(db, id) }),
    ]);
  };
  const save = useMutation({
    mutationFn: (
      input: {
        title: string;
        sql: string;
        expected_revision: number;
        batch?: BatchTemplate | null;
      } & Rollback,
    ) =>
      api.request<Draft>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}`,
        { method: "PATCH", body: JSON.stringify(input) },
      ),
    onSuccess: async (saved) => {
      setTitle(undefined);
      setSQL(undefined);
      setRollback(undefined);
      setBaseRevision(undefined);
      setEditError(undefined);
      client.setQueryData<Detail>(keys.detail(db, id), (current) =>
        current ? { ...current, draft: saved } : current,
      );
      await invalidate();
      if (saved.id !== id)
        await navigate({ to: path(db, `migrations/${encodeURIComponent(saved.id)}`) as never });
    },
    onError: async (error) => {
      setEditError(error);
      if (error instanceof ApiError && error.status === 409)
        await client.invalidateQueries({ queryKey: keys.detail(db, id) });
    },
  });
  const review = useMutation({
    mutationFn: ({ tail, body = {} }: { tail: string; body?: object }) =>
      api.request<Request>(`/databases/${encodeURIComponent(db)}/${tail}`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSettled: invalidate,
  });
  const execute = useMutation({
    // The execute endpoint only enqueues; the worker runs the SQL and the job is polled.
    mutationFn: async (request: Request) => {
      let job = await api.request<Job>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}/execute`,
        {
          method: "POST",
          body: JSON.stringify({ request_id: request.id, idempotency_key: crypto.randomUUID() }),
        },
      );
      await invalidate();
      while (!jobFinished(job)) {
        await new Promise((resolve) => setTimeout(resolve, 1000));
        job = await api.request<Job>(
          `/databases/${encodeURIComponent(db)}/jobs/${encodeURIComponent(job.job_id)}`,
        );
      }
      const done = await api.request<Request>(
        `/databases/${encodeURIComponent(db)}/requests/${encodeURIComponent(request.id)}`,
      );
      return { job, attempt: done.execution ?? undefined };
    },
    onMutate: () => setResult(undefined),
    onSuccess: ({ job, attempt }) =>
      setResult({ attempt, message: job.status === "failed" ? job.error : undefined }),
    onError: (error) =>
      setResult({ message: error instanceof ApiError ? error.message : undefined }),
    onSettled: invalidate,
  });
  const reconcile = useMutation({
    mutationFn: (attempt: Attempt) =>
      api.request<Attempt>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}/attempts/${encodeURIComponent(attempt.id)}/reconcile`,
        { method: "POST", body: JSON.stringify({}) },
      ),
    onSuccess: async () => {
      await invalidate();
    },
  });
  const explain = useMutation({
    mutationFn: (request: ExplainRequest) =>
      api.request<QueryPlan>(`/databases/${encodeURIComponent(db)}/explain`, {
        method: "POST",
        body: JSON.stringify(request),
      }),
    onMutate: () => {
      setPlan((old) => ({ ...old, pending: true, error: undefined }));
    },
    onSuccess: (result, request) => setPlan({ result, requestSQL: request.sql, pending: false }),
    onError: (error) =>
      setPlan((old) => ({
        ...old,
        pending: false,
        error: error instanceof ApiError ? error.message : "Unable to show the query plan.",
      })),
  });
  if (query.isPending)
    return (
      <main className="mx-auto max-w-7xl p-6" role="status">
        Loading migration…
      </main>
    );
  if (query.isError)
    return (
      <main className="mx-auto max-w-7xl p-6">
        <h1 className={styles.heading}>Migration</h1>
        <ErrorBox error={query.error} />
        <button className={`${styles.button} mt-4`} onClick={() => void query.refetch()}>
          Retry
        </button>
      </main>
    );
  const data = query.data;
  const d = data.draft;
  const planInput = isEditable ? editSQL : d.sql;
  return (
    <Shell workspace={data.workspace} active="migrations" title={d.title}>
      <div className="flex flex-wrap items-start gap-6">
        <div className="min-w-0 flex-[999_1_560px] space-y-6">
          <div>
            <Link
              to={path(db, "migrations") as never}
              className={`${styles.link} inline-flex min-h-11 items-center text-sm`}
            >
              All migrations
            </Link>
            <h1 className={`${styles.heading} break-words`}>{d.title}</h1>
            <dl className="mt-3 grid gap-x-6 gap-y-1.5 text-sm text-slate-600 sm:grid-cols-2">
              <div>
                <dt className="inline">Status: </dt>
                <dd className="inline text-slate-900">
                  {d.applied_version > 0
                    ? `Applied as ${versionLabel(d.applied_version)} (read-only)`
                    : d.executed
                      ? `Executed revision ${d.revision} (read-only)`
                      : `Draft revision ${d.revision}`}
                </dd>
              </div>
              <div>
                <dt className="inline">Dialect: </dt>
                <dd className="inline text-slate-900">{d.dialect}</dd>
              </div>
              <div>
                <dt className="inline">Created: </dt>
                <dd className="inline text-slate-900">
                  {formatDate(d.created_at)} by {d.created_by_name}
                </dd>
              </div>
              <div>
                <dt className="inline">Updated: </dt>
                <dd className="inline text-slate-900">
                  {formatDate(d.updated_at)} by {d.updated_by_name}
                </dd>
              </div>
            </dl>
            {data.blocked && (
              <div className="mt-6 rounded-lg border border-amber-300 bg-amber-50 p-4 text-amber-950">
                <p role="alert" className="text-base font-semibold leading-6">
                  {data.blocked}
                </p>
                {data.blocked_by && (
                  <div className="mt-3 space-y-3">
                    <p>
                      Check the target first: statements that ran before the failure stay applied.
                      Then lift the block; the attempt stays in history.
                    </p>
                    {data.workspace.can_edit && (
                      <button
                        type="button"
                        disabled={reconcile.isPending || data.blocked_by.reconciled_at != null}
                        onClick={() => reconcile.mutate(data.blocked_by!)}
                        className={styles.button}
                      >
                        {reconcile.isPending
                          ? "Reconciling…"
                          : "I checked the target, mark as reconciled"}
                      </button>
                    )}
                    <ErrorBox error={reconcile.error} />
                  </div>
                )}
              </div>
            )}
            {result && <Result {...result} db={db} migration={id} />}
          </div>
          {isEditable ? (
            <section aria-labelledby="edit-heading" className={`${styles.panel} p-5 sm:p-6`}>
              <h2 id="edit-heading" className="text-lg font-semibold text-slate-900">
                Edit SQL
              </h2>
              <form
                className="mt-4 space-y-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  setEditError(undefined);
                  save.mutate({
                    title: editTitle,
                    sql: d.batch ? d.sql : editSQL,
                    sql_down: editRollback.sql_down,
                    irreversible: editRollback.irreversible,
                    irreversible_reason: editRollback.irreversible_reason,
                    expected_revision: baseRevision ?? d.revision,
                    ...(d.batch ? { batch: d.batch } : {}),
                  });
                }}
              >
                <div className="space-y-1.5">
                  <label htmlFor="edit-title" className={styles.label}>
                    Title
                  </label>
                  <input
                    id="edit-title"
                    required
                    maxLength={200}
                    className={styles.input}
                    value={editTitle}
                    onChange={(e) => {
                      setBaseRevision((revision) => revision ?? d.revision);
                      setTitle(e.target.value);
                    }}
                  />
                </div>
                {d.batch ? (
                  <div>
                    <h3 className={styles.label}>Checkpointed batch template (read-only)</h3>
                    <dl className="mt-2 grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-2">
                      {Object.entries({
                        Schema: d.batch.schema,
                        Table: d.batch.table,
                        "Batch key": d.batch.key,
                        "Column to update": d.batch.column,
                        "New value": d.batch.value,
                        "Rows per batch": d.batch.batch_size,
                      }).map(([label, value]) => (
                        <div key={label}>
                          <dt className="inline text-slate-600">{label}: </dt>
                          <dd className="inline break-words text-slate-900">{value}</dd>
                        </div>
                      ))}
                    </dl>
                  </div>
                ) : (
                  <>
                    <div className="space-y-1.5">
                      <label htmlFor="edit-sql" className={styles.label}>
                        SQL
                      </label>
                      <textarea
                        id="edit-sql"
                        required
                        rows={14}
                        maxLength={262144}
                        spellCheck={false}
                        autoCapitalize="none"
                        value={editSQL}
                        onChange={(e) => {
                          setBaseRevision((revision) => revision ?? d.revision);
                          setSQL(e.target.value);
                        }}
                        className={codeArea}
                      />
                    </div>
                    <RollbackFields
                      id="edit"
                      value={editRollback}
                      onChange={(value) => {
                        setBaseRevision((revision) => revision ?? d.revision);
                        setRollback(value);
                      }}
                    />
                  </>
                )}
                {baseRevision !== undefined && baseRevision !== d.revision && (
                  <div className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950">
                    This draft changed on the server after you began editing. Your SQL is preserved,
                    and saving uses revision {baseRevision} so the server can reject an outdated
                    edit.
                    <button
                      type="button"
                      className={`${styles.button} mt-3`}
                      onClick={() => {
                        setTitle(d.title);
                        setSQL(d.sql);
                        setRollback(undefined);
                        setBaseRevision(d.revision);
                        setEditError(undefined);
                      }}
                    >
                      Reload latest revision
                    </button>
                  </div>
                )}
                <ErrorBox error={editError} />
                {editError instanceof ApiError && editError.status === 409 && (
                  <div className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950">
                    Resolve this revision conflict before saving again.
                    <button
                      type="button"
                      className={`${styles.button} mt-3`}
                      onClick={() => {
                        setTitle(d.title);
                        setSQL(d.sql);
                        setRollback(undefined);
                        setBaseRevision(d.revision);
                        setEditError(undefined);
                      }}
                    >
                      Reload latest revision
                    </button>
                  </div>
                )}
                <button disabled={save.isPending} className={styles.button}>
                  {save.isPending ? "Saving…" : `Save revision ${d.revision + 1}`}
                </button>
              </form>
            </section>
          ) : (
            <section aria-labelledby="sql-heading" className={`${styles.panel} p-5 sm:p-6`}>
              <h2 id="sql-heading" className="text-lg font-semibold text-slate-900">
                SQL
              </h2>
              <div className="mt-4">
                <SQL>{d.sql}</SQL>
              </div>
              <h3 className="mt-6 text-base font-semibold text-slate-900">SQL down</h3>
              <div className="mt-3">
                {d.irreversible ? (
                  <p className="text-sm leading-6 text-slate-700">
                    Irreversible: {d.irreversible_reason}
                  </p>
                ) : d.sql_down ? (
                  <SQL>{d.sql_down}</SQL>
                ) : (
                  <p className="text-sm leading-6 text-slate-700">Not written yet.</p>
                )}
              </div>
            </section>
          )}
          <ReviewPanel
            data={data}
            onRequest={(operation) =>
              review.mutate({
                tail: `migrations/${encodeURIComponent(id)}/requests`,
                body: requestPreconditions(d, data.current_version, operation),
              })
            }
            onAction={(request, action, body) =>
              review.mutate({ tail: `requests/${encodeURIComponent(request.id)}/${action}`, body })
            }
            onExecute={(request) => execute.mutate(request)}
            busy={review.isPending || execute.isPending}
            executing={execute.isPending}
            error={review.error}
          />
        </div>
        <div className="min-w-0 flex-[1_1_420px] *:mt-0">
          <PlanPanel
            sql={planInput}
            onExplain={(request) => explain.mutate(request)}
            pending={plan.pending}
            error={plan.error}
            result={plan.result}
            stale={!!plan.result && planIsStale(plan.requestSQL, planInput)}
          />
        </div>
      </div>
      <section aria-labelledby="attempts-heading" className="mt-10">
        <h2 id="attempts-heading" className="text-lg font-semibold text-slate-900">
          Attempts
        </h2>
        <AttemptsTable db={db} attempts={data.attempts} showMigration={false} />
      </section>
    </Shell>
  );
}

export function AttemptDetailPage({
  db,
  id,
  attemptID,
}: {
  db: string;
  id: string;
  attemptID: string;
}) {
  const { api } = useAuthenticatedContext();
  const history = useHistory(db);
  const query = useQuery({
    queryKey: keys.attempt(db, id, attemptID),
    queryFn: ({ signal }) =>
      api.request<Attempt>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}/attempts/${encodeURIComponent(attemptID)}`,
        { signal },
      ),
  });
  if (history.isPending || query.isPending)
    return (
      <main className="mx-auto max-w-7xl p-6" role="status">
        Loading attempt…
      </main>
    );
  if (history.isError || query.isError)
    return (
      <main className="mx-auto max-w-7xl p-6">
        <h1 className={styles.heading}>Attempt</h1>
        <ErrorBox error={history.error ?? query.error} />
      </main>
    );
  const w = history.data.workspace;
  const a = query.data;
  return (
    <Shell workspace={w} active="migrations" title="Attempt">
      <Link
        to={path(db, `migrations/${encodeURIComponent(id)}`) as never}
        className={`${styles.link} inline-flex min-h-11 items-center break-words text-sm`}
      >
        Back to {a.migration_title}
      </Link>
      <h1 className={`${styles.heading} flex flex-wrap items-center gap-3`}>
        {a.applied_version > 0
          ? `Version ${a.applied_version}`
          : `Candidate version ${a.candidate_version}`}{" "}
        <Status value={a.status} />
      </h1>
      <dl
        className={`${styles.panel} mt-6 grid gap-x-6 gap-y-4 p-5 text-sm sm:grid-cols-2 lg:grid-cols-3`}
      >
        {(
          [
            ["Operation", operationLabel(a.operation)],
            ["Executor", a.executor_name],
            ["Draft revision", a.draft_revision],
            ["Statements", a.statement_count],
            ["Started", formatDate(a.started_at)],
            ["Finished", formatDate(a.finished_at)],
            ["SQL SHA-256", a.sql_hash],
          ] as const
        ).map(([label, value]) => (
          <div key={label} className={label === "SQL SHA-256" ? "min-w-0" : undefined}>
            <dt className="text-slate-600">{label}</dt>
            <dd
              className={
                label === "Executor"
                  ? "mt-0.5 break-all text-slate-900"
                  : label === "SQL SHA-256"
                    ? "mt-0.5 break-all font-mono text-xs leading-5 text-slate-900"
                    : "mt-0.5 font-mono text-slate-900"
              }
            >
              {value}
            </dd>
          </div>
        ))}
        {a.reconciled_at && (
          <div className="sm:col-span-2 lg:col-span-3">
            <dt className="text-slate-600">Reconciliation</dt>
            <dd className="mt-0.5 text-slate-900">
              Outcome confirmed from the target execution receipt.
            </dd>
          </div>
        )}
      </dl>
      {a.error && (
        <p role="alert" className={`${styles.alert} mt-6 break-words`}>
          {a.error}
        </p>
      )}
      <section aria-labelledby="executed-sql-heading" className={`${styles.panel} mt-6 p-5 sm:p-6`}>
        <h2 id="executed-sql-heading" className="text-lg font-semibold text-slate-900">
          Executed SQL (immutable)
        </h2>
        <div className="mt-4">
          <SQL>{a.sql}</SQL>
        </div>
      </section>
    </Shell>
  );
}

export function SchemaDiffPage({ db }: { db: string }) {
  const { api } = useAuthenticatedContext();
  const client = useQueryClient();
  const navigate = useNavigate();
  const history = useHistory(db);
  const [desired, setDesired] = useState(snapshotExample);
  const [title, setTitle] = useState("");
  const [error, setError] = useState<unknown>();
  const [proposal, setProposal] = useState<Proposal>();
  const [reviewFor, setReviewFor] = useState<string>();
  const preview = useMutation({
    mutationFn: (input: { desired: string }) =>
      api.request<Proposal>(`/databases/${encodeURIComponent(db)}/schema-diff/preview`, {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: (value, input) => {
      setProposal(value);
      setReviewFor(input.desired);
      setError(undefined);
    },
    onError: setError,
  });
  const useDraft = useMutation({
    mutationFn: (input: { desired: string; source_fingerprint: string; title: string }) =>
      api.request<Draft>(`/databases/${encodeURIComponent(db)}/schema-diff/draft`, {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: async (draft) => {
      await client.invalidateQueries({ queryKey: keys.history(db) });
      await navigate({ to: path(db, `migrations/${encodeURIComponent(draft.id)}`) as never });
    },
    onError: setError,
  });
  if (history.isPending)
    return (
      <main className="mx-auto max-w-7xl p-6" role="status">
        Loading schema diff…
      </main>
    );
  if (history.isError)
    return (
      <main className="mx-auto max-w-7xl p-6">
        <h1 className={styles.heading}>Schema diff</h1>
        <ErrorBox error={history.error} />
      </main>
    );
  const w = history.data.workspace;
  const fresh = !!proposal && reviewFor === desired;
  return (
    <Shell workspace={w} active="schema-diff" title="Schema diff">
      <h1 className={styles.heading}>Schema diff</h1>
      <p className={`${styles.muted} mt-1 max-w-3xl leading-6`}>
        Compare the live schema with a desired snapshot. Preview never changes the database.
        Supported additions become a draft you can review and edit before executing.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setError(undefined);
          preview.mutate({ desired });
        }}
        className={`${styles.panel} mt-6 space-y-4 p-5 sm:p-6`}
      >
        <div className="space-y-1.5">
          <label htmlFor="desired" className={styles.label}>
            Desired snapshot (JSON)
          </label>
          <p id="desired-help" className="text-sm leading-6 text-slate-600">
            One schema per snapshot. Each column needs name, type and nullable; default is optional.
            Generation supports CREATE TABLE and ADD COLUMN with common types; other changes are
            reported as warnings.
          </p>
          <textarea
            id="desired"
            required
            rows={18}
            spellCheck={false}
            value={desired}
            onChange={(e) => setDesired(e.target.value)}
            aria-describedby="desired-help"
            className="w-full rounded-lg border border-slate-300 bg-slate-50 p-3 font-mono text-sm leading-6 text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
          />
        </div>
        <div className="flex flex-wrap items-center gap-4">
          <button disabled={preview.isPending} className={styles.primary}>
            Preview changes
          </button>
          <span
            className={`text-sm text-slate-600 ${preview.isPending ? "" : "invisible"}`}
            role="status"
          >
            Reading live schema…
          </span>
        </div>
      </form>
      <div aria-live="polite" className="mt-8 space-y-6">
        <ErrorBox error={error} className="" />
        {proposal && (
          <section aria-labelledby="changes-heading" className={`${styles.panel} p-5 sm:p-6`}>
            <h2 id="changes-heading" className="break-words text-lg font-semibold text-slate-900">
              Changes in schema <span className="font-mono">{proposal.schema}</span>
            </h2>
            {!fresh && (
              <p
                role="status"
                className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-950"
              >
                The desired snapshot changed. Preview it again before creating a draft.
              </p>
            )}
            {proposal.changes.length === 0 ? (
              <p className="mt-3 text-sm text-slate-700">
                The live schema already matches the desired snapshot.
              </p>
            ) : (
              <ul className="mt-3 divide-y divide-slate-100 text-sm">
                {proposal.changes.map((change, i) => (
                  <li key={i} className="flex flex-wrap items-start gap-2 break-words py-2">
                    {change.destructive && (
                      <span className={`${statusChip} bg-amber-50 text-amber-900 ring-amber-300`}>
                        Destructive
                      </span>
                    )}
                    <span className="min-w-0 text-slate-900">{change.text}</span>
                  </li>
                ))}
              </ul>
            )}
            {proposal.warnings.length > 0 && (
              <div>
                <h3 className="mt-6 text-base font-semibold text-slate-900">Warnings</h3>
                <div className="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950">
                  <ul className="list-disc space-y-2 pl-5">
                    {proposal.warnings.map((warning, i) => (
                      <li key={i} className="break-words">
                        {warning}
                      </li>
                    ))}
                  </ul>
                </div>
              </div>
            )}
            {proposal.sql && (
              <>
                <div>
                  <h3 className="mt-6 text-base font-semibold text-slate-900">Candidate SQL</h3>
                  <div className="mt-3">
                    <SQL>{proposal.sql}</SQL>
                  </div>
                </div>
                {w.can_edit ? (
                  <>
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        setError(undefined);
                        useDraft.mutate({
                          desired,
                          source_fingerprint: proposal.source_fingerprint,
                          title,
                        });
                      }}
                      className="mt-6 flex flex-wrap items-end gap-4 border-t border-slate-200 pt-5"
                    >
                      <div className="min-w-64 flex-1 space-y-1.5">
                        <label htmlFor="draft-title" className={styles.label}>
                          Draft title
                        </label>
                        <input
                          id="draft-title"
                          maxLength={200}
                          value={title}
                          placeholder={`Schema diff for ${proposal.schema}`}
                          onChange={(e) => setTitle(e.target.value)}
                          className={styles.input}
                        />
                      </div>
                      <button disabled={!fresh || useDraft.isPending} className={styles.primary}>
                        {useDraft.isPending ? "Creating draft…" : "Use as draft"}
                      </button>
                    </form>
                    <p className="mt-3 text-sm text-slate-600">
                      Creates an editable draft only. It is not executed.
                    </p>
                  </>
                ) : (
                  <p className="mt-6 text-sm text-slate-600">
                    View + Edit access is required to turn this into a draft.
                  </p>
                )}
              </>
            )}
            {!proposal.sql && proposal.changes.length > 0 && (
              <p className="mt-6 text-sm text-slate-600">
                No SQL was generated for these changes. Write a migration manually if they are
                intended.
              </p>
            )}
          </section>
        )}
      </div>
    </Shell>
  );
}
