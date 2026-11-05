import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import { styles } from "../../components/styles";
import { WorkspaceShell } from "../../components/workspace";
import { PlanPanel } from "../queryplan/views";
import type { ExplainRequest, QueryPlan } from "../queryplan/types";
import type { Attempt, AttemptError, Detail, Draft, History, Proposal } from "./types";
import {
  executePreconditions,
  formatDate,
  isAttempt,
  planIsStale,
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
const keys = {
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
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ${statusClass(value)}`}
    >
      {statusLabel(value)}
    </span>
  );
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
function useHistory(db: string) {
  const { api } = useAuthenticatedContext();
  return useQuery({
    queryKey: keys.history(db),
    queryFn: ({ signal }) =>
      api.request<History>(`/databases/${encodeURIComponent(db)}/migrations`, { signal }),
  });
}
function AttemptsTable({
  db,
  attempts,
  showMigration,
}: {
  db: string;
  attempts: Attempt[];
  showMigration: boolean;
}) {
  if (!attempts.length)
    return (
      <p className="mt-3 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
        No executions yet.
      </p>
    );
  return (
    <div className={`${styles.panel} mt-3 overflow-x-auto`}>
      <table className="w-full min-w-[44rem] text-left text-sm tabular-nums">
        <thead>
          <tr>
            {[
              "Version",
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
                className="whitespace-nowrap border-b border-slate-200 bg-slate-50 px-4 py-2.5 text-xs font-semibold text-slate-600"
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
  const query = useHistory(db);
  const [title, setTitle] = useState("");
  const [sql, setSQL] = useState("");
  const [error, setError] = useState<unknown>();
  const navigate = useNavigate();
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: (input: { title: string; sql: string }) =>
      api.request<Draft>(`/databases/${encodeURIComponent(db)}/migrations`, {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: async (draft) => {
      setError(undefined);
      setTitle("");
      setSQL("");
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
              Saving creates a draft. Nothing runs until you review and execute it.
            </p>
            <form
              className="mt-5 space-y-4"
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                setError(undefined);
                create.mutate({ title, sql });
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
                  className="w-full rounded-lg border border-slate-300 bg-slate-50 p-3 font-mono text-sm leading-6 text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
                />
              </div>
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
                    <span className="text-xs text-slate-700">
                      {d.applied_version > 0
                        ? `Applied as ${versionLabel(d.applied_version)}`
                        : d.last_status
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
      <section aria-labelledby="history-heading" className="mt-10">
        <h2 id="history-heading" className="text-lg font-semibold text-slate-900">
          Execution history
        </h2>
        <p className={`${styles.muted} mt-1`}>
          Every attempt is kept, newest first. Applied versions increase only on success.
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
        <p className="font-semibold">Applied as {versionLabel(attempt.applied_version)}.</p>
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
      setBaseRevision(undefined);
      setEditError(undefined);
      setResult(undefined);
      setPlan({ pending: false });
    }
  }, [db, id]);
  const draft = query.data?.draft;
  const editTitle = title ?? draft?.title ?? "";
  const editSQL = sql ?? draft?.sql ?? "";
  const isEditable = !!draft && query.data!.workspace.can_edit && draft.applied_version === 0;
  const invalidate = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: keys.history(db) }),
      client.invalidateQueries({ queryKey: keys.detail(db, id) }),
    ]);
  };
  const save = useMutation({
    mutationFn: (input: { title: string; sql: string; expected_revision: number }) =>
      api.request<Draft>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}`,
        { method: "PATCH", body: JSON.stringify(input) },
      ),
    onSuccess: async (saved) => {
      setTitle(undefined);
      setSQL(undefined);
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
  const execute = useMutation({
    mutationFn: (input: {
      expected_revision: number;
      expected_version: number;
      expected_sql_hash: string;
    }) =>
      api.request<Attempt>(
        `/databases/${encodeURIComponent(db)}/migrations/${encodeURIComponent(id)}/execute`,
        { method: "POST", body: JSON.stringify(input) },
      ),
    onSuccess: async (attempt) => {
      setResult({ attempt });
      await invalidate();
    },
    onError: async (error) => {
      const e = error as ApiError & AttemptError;
      setResult({
        attempt: isAttempt(e.attempt) ? e.attempt : undefined,
        message: error instanceof ApiError ? error.message : undefined,
      });
      await invalidate();
    },
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
                    sql: editSQL,
                    expected_revision: baseRevision ?? d.revision,
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
                    className="w-full rounded-lg border border-slate-300 bg-slate-50 p-3 font-mono text-sm leading-6 text-slate-900 hover:border-slate-400 focus-visible:border-cyan-700 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700"
                  />
                </div>
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
            </section>
          )}
          {isEditable && (
            <section aria-labelledby="execute-heading" className={`${styles.panel} p-5 sm:p-6`}>
              <h2 id="execute-heading" className="text-lg font-semibold text-slate-900">
                Execute
              </h2>
              <p className="mt-2 text-sm leading-6 text-slate-700">
                Runs saved revision {d.revision} (SHA-256{" "}
                <code className="font-mono text-slate-900">{shortHash(d.sql_hash)}</code>){" "}
                {d.dialect === "mysql" ? "statement by statement" : "in one transaction"} as{" "}
                <strong className="font-mono font-semibold text-slate-900">
                  {versionLabel(data.current_version + 1)}
                </strong>
                .{" "}
                {d.dialect === "mysql"
                  ? "MySQL commits DDL immediately, so if a later statement fails the earlier ones stay applied."
                  : "Any error rolls back every statement."}{" "}
                Unsaved edits above are not executed.
              </p>
              <details className="mt-3">
                <summary className="cursor-pointer py-2.5 text-sm font-medium text-cyan-700">
                  Review the SQL that will run
                </summary>
                <div className="mt-2">
                  <SQL>{d.sql}</SQL>
                </div>
              </details>
              <button
                type="button"
                disabled={execute.isPending || !!data.blocked}
                onClick={() => {
                  setResult(undefined);
                  execute.mutate(executePreconditions(d, data.current_version));
                }}
                className={`${styles.primary} mt-4`}
              >
                {execute.isPending
                  ? "Executing… this can take up to 10 seconds."
                  : `Execute as ${versionLabel(data.current_version + 1)}`}
              </button>
            </section>
          )}
        </div>
        <div className="min-w-0 flex-[1_1_420px]">
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
        {([
          ["Executor", a.executor_name],
          ["Draft revision", <span className="font-mono">{a.draft_revision}</span>],
          ["Statements", <span className="font-mono">{a.statement_count}</span>],
          ["Started", <span className="font-mono">{formatDate(a.started_at)}</span>],
          ["Finished", <span className="font-mono">{formatDate(a.finished_at)}</span>],
          [
            "SQL SHA-256",
            <span className="break-all font-mono text-xs leading-5">{a.sql_hash}</span>,
          ],
        ] as const).map(([label, value]) => (
          <div key={label}>
            <dt className="text-slate-600">{label}</dt>
            <dd className="mt-0.5 break-all text-slate-900">{value}</dd>
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
                      <span className="rounded-full bg-amber-50 px-2.5 py-0.5 text-xs font-medium text-amber-950 ring-1 ring-amber-300">
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
                <div className="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-4 text-amber-950">
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
