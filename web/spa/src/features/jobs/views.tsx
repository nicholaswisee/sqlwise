import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import { styles } from "../../components/styles";
import { WorkspaceShell } from "../../components/workspace";
import { formatDate } from "../migrations/format";
import type { History } from "../migrations/types";
import type { JobList, JobView, RecoveryOperation } from "./types";
import {
  effectLabel,
  jobActive,
  jobStatusClass,
  jobStatusLabel,
  jobSummary,
  operationName,
  progressLabel,
  progressPercent,
  recoveryLabel,
  shortID,
} from "./format";

const base = (db: string) => `/databases/${encodeURIComponent(db)}`;
const chip =
  "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ring-1";
const cell = "border-b border-slate-100 px-4 py-2.5 align-top";

// Polling intervals; WebSockets are not needed for job progress.
export const listPollMs = 2000;
export const detailPollMs = 1000;

function StatusChip({ job }: { job: JobView }) {
  return (
    <span className={`${chip} ${jobStatusClass(job.status)}`}>
      {jobStatusLabel(job.status, job.effect)}
    </span>
  );
}

function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p role="alert" className={`${styles.alert} mt-4`}>
      {error instanceof ApiError
        ? error.message
        : "The request could not be completed. Check your connection and try again."}
    </p>
  );
}

// Workspace header and migration titles come from the migration history.
function useWorkspace(db: string) {
  const { api } = useAuthenticatedContext();
  return useQuery({
    queryKey: ["jobs-workspace", db],
    queryFn: ({ signal }) => api.request<History>(`${base(db)}/migrations`, { signal }),
  });
}

function Shell({
  workspace,
  title,
  children,
}: {
  workspace: History["workspace"];
  title: string;
  children: ReactNode;
}) {
  const { session } = useAuthenticatedContext();
  useEffect(() => {
    document.title = `${title} | ${workspace.name} | sqlwise`;
  }, [title, workspace.name]);
  return (
    <WorkspaceShell workspace={workspace} username={session.user.username} active="jobs">
      {children}
    </WorkspaceShell>
  );
}

function Failure({ title, error, retry }: { title: string; error: unknown; retry: () => void }) {
  const missing = error instanceof ApiError && (error.status === 404 || error.status === 403);
  return (
    <main className="mx-auto max-w-7xl p-4 sm:p-6">
      <h1 className={styles.heading}>{title}</h1>
      <ErrorBox error={error} />
      <div className="mt-4 flex flex-wrap gap-3">
        {!missing && (
          <button className={styles.button} onClick={retry}>
            Retry
          </button>
        )}
        <Link to="/databases" className={styles.button}>
          Back to databases
        </Link>
      </div>
    </main>
  );
}

function Progress({ job }: { job: JobView }) {
  const percent = progressPercent(job);
  if (percent === undefined && job.committed_batches === 0) return null;
  return (
    <div className="space-y-1.5">
      <p className="text-sm text-slate-800">{progressLabel(job)}</p>
      {percent !== undefined && (
        <div
          role="progressbar"
          aria-label="Committed rows"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="h-2 w-full max-w-md overflow-hidden rounded-full bg-slate-200"
        >
          <div
            className={`h-full ${job.status === "blocked" ? "bg-amber-500" : "bg-cyan-700"}`}
            style={{ width: `${percent}%` }}
          />
        </div>
      )}
    </div>
  );
}

export function JobsPage({ db }: { db: string }) {
  const { api } = useAuthenticatedContext();
  const workspace = useWorkspace(db);
  const list = useQuery({
    queryKey: ["jobs", db],
    queryFn: ({ signal }) => api.request<JobList>(`${base(db)}/jobs`, { signal }),
    refetchInterval: (query) => (query.state.data?.jobs.some(jobActive) ? listPollMs : false),
  });
  if (workspace.isPending || list.isPending)
    return (
      <main className="mx-auto max-w-7xl p-4 sm:p-6" role="status">
        Loading jobs…
      </main>
    );
  if (workspace.isError || list.isError)
    return (
      <Failure
        title="Jobs"
        error={workspace.error ?? list.error}
        retry={() => {
          void workspace.refetch();
          void list.refetch();
        }}
      />
    );
  const titles = new Map(workspace.data.drafts.map((d) => [d.id, d.title]));
  const jobs = list.data.jobs;
  return (
    <Shell workspace={workspace.data.workspace} title="Jobs">
      <h1 className={styles.heading}>Jobs</h1>
      <p className={`${styles.muted} mt-1 max-w-3xl leading-6`}>
        Approved executions run here, one at a time per database. An accepted job is not a success
        until its status says so.
      </p>
      {list.isRefetching && (
        <p className="sr-only" role="status">
          Refreshing jobs
        </p>
      )}
      {jobs.length === 0 ? (
        <p className="mt-6 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
          No jobs yet. Execute an approved request from a migration to queue one.
        </p>
      ) : (
        <div className={`${styles.panel} mt-6 overflow-x-auto`}>
          <table className="w-full min-w-[52rem] text-left text-sm tabular-nums">
            <thead>
              <tr>
                {["Job", "Operation", "Migration", "Status", "Progress", "Attempts", "Queued"].map(
                  (h) => (
                    <th
                      key={h}
                      scope="col"
                      className="whitespace-nowrap border-b border-slate-200 bg-slate-50 px-4 py-2.5 text-xs font-semibold text-slate-600"
                    >
                      {h}
                    </th>
                  ),
                )}
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <tr key={job.job_id}>
                  <td className={`${cell} whitespace-nowrap font-mono`}>
                    <Link
                      to={`${base(db)}/jobs/${encodeURIComponent(job.job_id)}` as never}
                      className={styles.link}
                      aria-label={`Job ${shortID(job.job_id)}`}
                    >
                      {shortID(job.job_id)}
                    </Link>
                  </td>
                  <td className={`${cell} whitespace-nowrap`}>{operationName(job.operation)}</td>
                  <td className={`${cell} break-words`}>
                    {titles.get(job.migration_id) ?? shortID(job.migration_id)}
                  </td>
                  <td className={cell}>
                    <StatusChip job={job} />
                  </td>
                  <td className={`${cell} whitespace-nowrap text-slate-700`}>
                    {job.total_rows == null && job.committed_batches === 0
                      ? effectLabel(job.effect)
                      : progressLabel(job)}
                  </td>
                  <td className={`${cell} whitespace-nowrap`}>
                    {job.attempts} / {job.max_attempts}
                  </td>
                  <td className={`${cell} whitespace-nowrap font-mono text-slate-700`}>
                    {formatDate(job.queued_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Shell>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-1 break-all text-sm text-slate-900">{children}</dd>
    </div>
  );
}

export function JobDetailPage({ db, jobID }: { db: string; jobID: string }) {
  const { api } = useAuthenticatedContext();
  const navigate = useNavigate();
  const workspace = useWorkspace(db);
  const query = useQuery({
    queryKey: ["job", db, jobID],
    queryFn: ({ signal }) =>
      api.request<JobView>(`${base(db)}/jobs/${encodeURIComponent(jobID)}`, { signal }),
    refetchInterval: (q) => (jobActive(q.state.data) ? detailPollMs : false),
    retry: (count, error) =>
      !(error instanceof ApiError && [401, 403, 404].includes(error.status)) && count < 2,
  });
  const [error, setError] = useState<unknown>();
  // Recovery is a new review request; approval still happens in the migration workflow.
  const recover = useMutation({
    mutationFn: async (operation: RecoveryOperation) => {
      const job = query.data!;
      const draft = workspace.data?.drafts.find((d) => d.id === job.migration_id);
      if (!draft) throw new Error("The migration of this job is no longer available.");
      return api.request<{ id: string }>(
        `${base(db)}/migrations/${encodeURIComponent(job.migration_id)}/requests`,
        {
          method: "POST",
          body: JSON.stringify({
            operation,
            expected_revision: draft.revision,
            expected_definition_hash: draft.definition_hash,
            expected_version: workspace.data!.current_version,
            recovers_execution_id: job.recovery_execution_id,
          }),
        },
      );
    },
    onSuccess: async () => {
      setError(undefined);
      await navigate({
        to: `${base(db)}/migrations/${encodeURIComponent(query.data!.migration_id)}` as never,
      });
    },
    onError: setError,
  });
  if (workspace.isPending || query.isPending)
    return (
      <main className="mx-auto max-w-7xl p-4 sm:p-6" role="status">
        Loading job…
      </main>
    );
  if (workspace.isError || query.isError)
    return (
      <Failure
        title="Job"
        error={query.error ?? workspace.error}
        retry={() => {
          void workspace.refetch();
          void query.refetch();
        }}
      />
    );
  const job = query.data;
  const canEdit = workspace.data.workspace.can_edit;
  const jobLink = (id: string) => (
    <Link to={`${base(db)}/jobs/${encodeURIComponent(id)}` as never} className={styles.link}>
      {shortID(id)}
    </Link>
  );
  const title = workspace.data.drafts.find((d) => d.id === job.migration_id)?.title;
  return (
    <Shell workspace={workspace.data.workspace} title={`Job ${shortID(job.job_id)}`}>
      <Link to={`${base(db)}/jobs` as never} className={styles.link}>
        All jobs
      </Link>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <h1 className={`${styles.heading} break-all`}>Job {shortID(job.job_id)}</h1>
        <StatusChip job={job} />
      </div>
      <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-700" role="status">
        {jobSummary(job)}
      </p>
      {job.error && (
        <p role="alert" className={`${styles.alert} mt-4 max-w-3xl break-words`}>
          {job.error}
        </p>
      )}
      {job.blocked_by && (
        <p className="mt-3 text-sm text-slate-700">Blocked by job {jobLink(job.blocked_by)}.</p>
      )}
      {job.resolved_by && (
        <p className="mt-3 text-sm text-slate-700">
          Recovery was handed to job {jobLink(job.resolved_by)}.
        </p>
      )}
      <section aria-labelledby="progress-heading" className={`${styles.panel} mt-6 p-5 sm:p-6`}>
        <h2 id="progress-heading" className="text-lg font-semibold text-slate-900">
          Target effect
        </h2>
        <p className="mt-1 text-sm text-slate-700">{effectLabel(job.effect)}</p>
        <div className="mt-4">
          <Progress job={job} />
        </div>
      </section>
      <section aria-labelledby="details-heading" className={`${styles.panel} mt-6 p-5 sm:p-6`}>
        <h2 id="details-heading" className="text-lg font-semibold text-slate-900">
          Details
        </h2>
        <dl className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Field label="Operation">{operationName(job.operation)}</Field>
          <Field label="Migration">
            <Link
              to={`${base(db)}/migrations/${encodeURIComponent(job.migration_id)}` as never}
              className={styles.link}
            >
              {title ?? shortID(job.migration_id)}
            </Link>
          </Field>
          <Field label="Attempts">
            {job.attempts} of {job.max_attempts}
          </Field>
          <Field label="Job ID">
            <span className="font-mono">{job.job_id}</span>
          </Field>
          <Field label="Request ID">
            <span className="font-mono">{job.request_id}</span>
          </Field>
          <Field label="Execution ID">
            <span className="font-mono">{job.execution_id}</span>
          </Field>
          {job.recovers_execution_id && (
            <Field label="Recovers execution">
              <span className="font-mono">{job.recovers_execution_id}</span>
            </Field>
          )}
          <Field label="Queued">{formatDate(job.queued_at)}</Field>
          <Field label="Started">{formatDate(job.started_at)}</Field>
          <Field label="Finished">{formatDate(job.finished_at)}</Field>
          {job.next_run_at && <Field label="Next delivery">{formatDate(job.next_run_at)}</Field>}
        </dl>
      </section>
      {job.recovery.length > 0 && (
        <section aria-labelledby="recovery-heading" className={`${styles.panel} mt-6 p-5 sm:p-6`}>
          <h2 id="recovery-heading" className="text-lg font-semibold text-slate-900">
            Recovery
          </h2>
          <p className={`${styles.muted} mt-1 max-w-3xl leading-6`}>
            Resume continues the uncommitted batches; compensation reverts committed batches in
            reverse order. Either creates a review request that every authorized group must approve
            before it is queued.
          </p>
          {canEdit ? (
            <div className="mt-4 flex flex-wrap gap-3">
              {job.recovery.map((op) => (
                <button
                  key={op}
                  className={op === "resume" ? styles.primary : styles.button}
                  disabled={recover.isPending}
                  onClick={() => recover.mutate(op)}
                >
                  {recoveryLabel(op)}
                </button>
              ))}
            </div>
          ) : (
            <p className="mt-4 text-sm text-slate-700">
              Ask an editor of this database to request recovery.
            </p>
          )}
          <ErrorBox error={error} />
        </section>
      )}
    </Shell>
  );
}
