import { useEffect, useState, type FormEvent } from "react";
import { styles } from "../../components/styles";
import type { ExplainRequest, PlanNode, QueryPlan } from "./types";

export function PlanPanel({
  sql,
  onExplain,
  pending = false,
  error,
  result,
  stale: staleProp,
}: {
  sql: string;
  onExplain: (request: ExplainRequest) => void | Promise<unknown>;
  pending?: boolean;
  error?: string;
  result?: QueryPlan;
  stale?: boolean;
}) {
  const [mode, setMode] = useState<"explain" | "analyze">("explain");
  const [confirm, setConfirm] = useState(false);
  const [successfulSQL, setSuccessfulSQL] = useState<string>();
  const stale = staleProp ?? (!!result && successfulSQL !== undefined && sql !== successfulSQL);
  useEffect(() => {
    if (mode !== "analyze") setConfirm(false);
  }, [mode]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (mode === "analyze" && !confirm) return;
    const request = onExplain({ sql, mode, confirmed: mode === "analyze" && confirm });
    if (request)
      void request.then(
        () => setSuccessfulSQL(sql),
        () => {},
      );
  }

  return (
    <section aria-labelledby="plan-heading" className={`${styles.panel} mt-8 p-5`}>
      <h2 id="plan-heading" className="text-lg font-semibold text-slate-900">
        Query plan
      </h2>
      <p className={`${styles.muted} mt-2 max-w-3xl leading-6`}>
        Explain shows how the database would run one SELECT, INSERT, UPDATE or DELETE without
        running it. Analyze runs a SELECT on a connection marked as an analyze sandbox, inside a
        read-only transaction, and reports actual time and rows.
      </p>
      <form onSubmit={submit} className="mt-4 flex flex-col gap-4">
        <fieldset className="flex flex-wrap items-center gap-x-6 gap-y-1">
          <legend className={`${styles.label} mb-1`}>Plan mode</legend>
          <label className="inline-flex min-h-11 cursor-pointer items-center gap-2 text-sm text-slate-900">
            <input
              type="radio"
              name="mode"
              value="explain"
              checked={mode === "explain"}
              onChange={() => setMode("explain")}
              className="size-4 accent-cyan-700"
            />
            Explain
          </label>
          <label className="inline-flex min-h-11 cursor-pointer items-center gap-2 text-sm text-slate-900">
            <input
              type="radio"
              name="mode"
              value="analyze"
              checked={mode === "analyze"}
              onChange={() => setMode("analyze")}
              className="size-4 accent-cyan-700"
            />
            Analyze (sandbox only)
          </label>
        </fieldset>
        <label className="flex min-h-11 cursor-pointer items-start gap-2 text-sm leading-6 text-slate-800">
          <input
            type="checkbox"
            checked={confirm}
            onChange={(event) => setConfirm(event.target.checked)}
            className="mt-1 size-4 shrink-0 accent-cyan-700"
          />
          I understand Analyze executes the SELECT on the sandbox database.
        </label>
        <div>
          <button
            type="submit"
            disabled={pending || (mode === "analyze" && !confirm)}
            className={styles.button}
          >
            {pending ? "Working…" : "Show plan"}
          </button>
        </div>
      </form>
      <div className="group mt-4" aria-live="polite">
        {stale && (
          <p
            role="status"
            className="mb-3 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm leading-6 text-amber-950"
          >
            The SQL changed after this plan was made. Show the plan again to update it.
          </p>
        )}
        {error && (
          <p role="alert" className={styles.alert}>
            {error}
          </p>
        )}
        {result && <PlanResult plan={result} />}
      </div>
    </section>
  );
}

export function PlanResult({ plan }: { plan: QueryPlan }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-slate-50 p-4">
      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-slate-600">
        <Metric label="Mode" value={plan.mode} />
        <Metric label="Dialect" value={plan.dialect} />
        <Metric label="Planning" value={milliseconds(plan.planning_time)} />
        <Metric label="Execution" value={milliseconds(plan.execution_time)} />
        <div className="min-w-0">
          <dt className="inline">SQL hash: </dt>
          <dd className="inline break-all font-mono text-slate-800">{plan.sql_hash}</dd>
        </div>
      </dl>
      {plan.warnings.length > 0 && (
        <ul className="mt-3 flex flex-col gap-2">
          {plan.warnings.map((warning, i) => (
            <li key={i} className="rounded-lg border border-slate-200 bg-white p-3 text-sm">
              {warning}
            </li>
          ))}
        </ul>
      )}
      <ul className="mt-1 text-sm">
        <PlanTree node={plan.root} />
      </ul>
    </div>
  );
}

function number(value: number) {
  return String(value);
}
function optional(value: number | undefined) {
  return value === undefined ? "N/A" : number(value);
}
function milliseconds(value: number | undefined) {
  return value === undefined ? "N/A" : `${number(value)} ms`;
}
function detail(node: PlanNode) {
  const values: string[] = [];
  if (node.operation && node.relation) values.push(`${node.operation} on ${node.relation}`);
  else if (node.relation) values.push(`on ${node.relation}`);
  if (node.index) values.push(`using ${node.index}`);
  if (node.join) values.push(`${node.join} join`);
  return values.join(" · ");
}
function Metric({ label, value }: { label: string; value: string | number | undefined }) {
  return (
    <div>
      <dt className="text-[11px] text-slate-600">{label}</dt>
      <dd className="font-mono text-[13px] text-slate-900 tabular-nums">{value ?? "N/A"}</dd>
    </div>
  );
}
function PlanTree({ node }: { node: PlanNode }) {
  const description = detail(node);
  return (
    <li className="mt-2">
      <div className="rounded-lg border border-slate-200 bg-white p-3">
        <p className="break-words font-semibold text-slate-900">
          {node.type}
          {description && <span className="font-normal text-slate-600"> · {description}</span>}
        </p>
        <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-5">
          <Metric
            label="Est. cost"
            value={`${number(node.startup_cost)}–${number(node.total_cost)}`}
          />
          <Metric label="Est. rows" value={number(node.plan_rows)} />
          <Metric label="Actual time (ms)" value={optional(node.actual_time)} />
          <Metric label="Actual rows" value={optional(node.actual_rows)} />
          <Metric label="Loops" value={optional(node.loops)} />
        </dl>
      </div>
      {node.children?.length > 0 && (
        <ul className="ml-3 border-l-2 border-slate-200 pl-3 sm:ml-5 sm:pl-5">
          {node.children.map((child, i) => (
            <PlanTree key={`${child.type}-${i}`} node={child} />
          ))}
        </ul>
      )}
    </li>
  );
}
