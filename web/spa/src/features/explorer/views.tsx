import { Link } from "@tanstack/react-router";
import { styles } from "../../components/styles";
import type { SchemaExplorerDTO, SchemaTable, TableDataDTO } from "./types";

const th =
  "whitespace-nowrap border-b border-slate-200 bg-slate-50 px-4 py-2.5 text-xs font-semibold text-slate-600";
const td = "border-b border-slate-100 px-4 py-2.5 align-top";
export function DatabaseList({
  databases,
  loading = false,
  error,
  href,
}: {
  databases: { id: string; name: string; engine?: string; permission: string }[];
  loading?: boolean;
  error?: string;
  href: (id: string) => string;
}) {
  return (
    <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-8 sm:px-6">
      <h1 className={styles.heading}>Databases</h1>
      <p className={`${styles.muted} mt-2 max-w-2xl leading-6`}>
        Databases your groups can open. Your access is the highest permission among your groups.
      </p>
      {loading ? (
        <p role="status" className="mt-6 text-sm text-slate-600">
          Loading databases…
        </p>
      ) : error ? (
        <p role="alert" className={`${styles.alert} mt-6`}>
          {error}
        </p>
      ) : databases.length === 0 ? (
        <p className="mt-6 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
          No database access has been assigned to your account.
        </p>
      ) : (
        <div className={`${styles.panel} mt-6 overflow-x-auto`}>
          <table className="w-full min-w-[32rem] text-left text-sm tabular-nums">
            <thead>
              <tr>
                {["Database", "Engine", "Your access"].map((h) => (
                  <th key={h} scope="col" className={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {databases.map((db) => (
                <tr key={db.id} className="hover:bg-slate-50">
                  <td className={`${td} py-1.5`}>
                    <Link
                      to={href(db.id) as never}
                      className={`${styles.link} inline-flex min-h-11 items-center break-all font-medium`}
                    >
                      {db.name}
                    </Link>
                  </td>
                  <td className={td}>
                    {db.engine ? <span className={styles.chip}>{db.engine}</span> : "Unknown"}
                  </td>
                  <td className={td}>
                    <span className={db.permission === "edit" ? styles.chipTeal : styles.chip}>
                      {db.permission === "edit" ? "View + Edit" : "View only"}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className={`${styles.muted} mt-4`}>
        Missing a database? Ask a platform administrator to add your group to it.
      </p>
    </main>
  );
}

export function SchemaExplorer({
  data,
  onSchemaChange,
  schemaHref,
  tableHref,
  dataHref,
  selectedTableName,
  loading = false,
  error,
}: {
  data: SchemaExplorerDTO;
  onSchemaChange: (schema: string) => void;
  schemaHref: (schema: string) => string;
  tableHref: (schema: string, table: string) => string;
  dataHref: (tableId: string) => string;
  selectedTableName?: string;
  loading?: boolean;
  error?: string;
}) {
  const selected = data.tables.find((t) => t.name === selectedTableName) ?? data.tables[0];
  return (
    <div>
      {loading ? (
        <p role="status" className={styles.muted}>
          Loading schema…
        </p>
      ) : error ? (
        <p role="alert" className={styles.alert}>
          {error}
        </p>
      ) : (
        <div className="flex flex-wrap items-start gap-6">
          <aside
            aria-label="Tables"
            className="flex min-w-0 flex-[1_1_16rem] flex-col gap-4 rounded-xl border border-slate-200 bg-slate-50 p-3"
          >
            <div className="flex flex-wrap items-end gap-2 px-1 pt-1">
              <div className="min-w-0 flex-1 space-y-1.5">
                <label htmlFor="schema" className={styles.label}>
                  Schema
                </label>
                <select
                  id="schema"
                  value={data.schema}
                  onChange={(e) => onSchemaChange(e.target.value)}
                  className={`${styles.input} w-full text-sm`}
                  aria-label="Schema"
                >
                  {data.schemas.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
              </div>
            </div>
            <nav aria-label="Tables in schema" className="flex flex-col gap-0.5">
              <h2 className="break-all px-3 pb-1 text-xs font-semibold text-slate-600">
                Tables in {data.schema}
              </h2>
              {data.tables.length === 0 ? (
                <p className="px-3 py-2 text-sm text-slate-600">This schema has no tables.</p>
              ) : (
                data.tables.map((t) => (
                  <Link
                    key={t.id}
                    to={tableHref(data.schema, t.name) as never}
                    aria-current={t.id === selected?.id ? "page" : undefined}
                    className={`flex min-h-11 items-center justify-between gap-3 rounded-lg px-3 py-2 text-sm hover:bg-white ${styles.focus} ${t.id === selected?.id ? "bg-white text-cyan-900" : "text-slate-700"}`}
                  >
                    <span className="min-w-0 break-all font-mono">{t.name}</span>
                    <span className="shrink-0 text-xs text-slate-600 tabular-nums">
                      {t.columns.length} {t.columns.length === 1 ? "column" : "columns"}
                    </span>
                  </Link>
                ))
              )}
            </nav>
          </aside>
          {selected && (
            <SchemaTableView table={selected} schema={data.schema} dataHref={dataHref} />
          )}
        </div>
      )}
    </div>
  );
}
function Definitions({
  title,
  values,
  empty,
}: {
  title: string;
  values: SchemaTable["indexes"];
  empty: string;
}) {
  return (
    <section
      aria-label={title}
      className={`${styles.panel} flex min-w-0 flex-[1_1_20rem] flex-col gap-3 p-4`}
    >
      <h2 className="text-base font-semibold text-slate-900">{title}</h2>
      {values.length === 0 ? (
        <p className={styles.muted}>{empty}</p>
      ) : (
        <dl className="flex flex-col gap-3 text-sm">
          {values.map((d) => (
            <div key={d.name}>
              <dt className="flex flex-wrap items-center gap-2">
                <span className="break-all font-mono font-medium text-slate-900">{d.name}</span>
                <span className={styles.chip}>{d.kind}</span>
              </dt>
              <dd className="mt-1 break-all font-mono text-[13px] leading-5 text-slate-700">
                {d.definition}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  );
}
function SchemaTableView({
  table,
  schema,
  dataHref,
}: {
  table: SchemaTable;
  schema: string;
  dataHref: (id: string) => string;
}) {
  const keys = table.columns.filter((c) => c.primary_key).map((c) => c.name);
  return (
    <section
      aria-labelledby="table-heading"
      className="flex min-w-0 flex-[999_1_560px] flex-col gap-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <h1 id="table-heading" className="break-all font-mono text-xl font-medium text-slate-900">
            {schema}.{table.name}
          </h1>
          <p className={styles.muted}>
            {table.columns.length} {table.columns.length === 1 ? "column" : "columns"} ·{" "}
            {keys.length ? `Primary key on ${keys.join(", ")}` : "No primary key"}
          </p>
        </div>
        <Link to={dataHref(table.id) as never} className={styles.primary}>
          View data
        </Link>
      </div>
      <section aria-labelledby="columns-heading" className={`${styles.panel} overflow-hidden`}>
        <h2
          id="columns-heading"
          className="border-b border-slate-200 px-4 py-3 text-base font-semibold text-slate-900"
        >
          Columns
        </h2>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[36rem] text-left text-sm tabular-nums">
            <thead>
              <tr>
                {["Name", "Type", "Nullable", "Default"].map((x) => (
                  <th key={x} scope="col" className={th}>
                    {x}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {table.columns.map((c) => (
                <tr key={c.name}>
                  <td className={`${td} break-all`}>
                    <span className="font-mono text-slate-900">{c.name}</span>
                    {c.primary_key && <span className={`${styles.chipTeal} ml-2`}>PK</span>}
                  </td>
                  <td className={`${td} break-all font-mono`}>{c.type}</td>
                  <td className={td}>{c.nullable ? "yes" : "no"}</td>
                  <td className={`${td} break-all font-mono`}>{c.default}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <div className="flex flex-wrap gap-6">
        <Definitions
          title="Constraints"
          values={table.constraints}
          empty="No constraints besides the column types."
        />
        <Definitions title="Indexes" values={table.indexes} empty="No indexes on this table." />
      </div>
    </section>
  );
}

export function TableDataView({
  data,
  onPage,
  onLimitChange,
  schemaHref,
  loading = false,
  error,
}: {
  data: TableDataDTO;
  onPage: (offset: number, limit: number) => void;
  onLimitChange: (limit: number) => void;
  schemaHref: (schema: string, table: string) => string;
  loading?: boolean;
  error?: string;
}) {
  const from = data.rows.length ? data.offset + 1 : 0,
    to = data.offset + data.rows.length;
  const pageSizes = [25, 50, 100, 200];
  if (!pageSizes.includes(data.limit) && data.limit > 0) {
    const index = pageSizes.findIndex((size) => data.limit < size);
    pageSizes.splice(index < 0 ? pageSizes.length : index, 0, data.limit);
  }
  return (
    <div>
      {loading ? (
        <p role="status" className={styles.muted}>
          Loading rows…
        </p>
      ) : error ? (
        <p role="alert" className={styles.alert}>
          {error}
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
            <div className="min-w-0 space-y-1">
              <Link
                to={schemaHref(data.schema, data.table) as never}
                className={`${styles.link} inline-flex min-h-11 items-center gap-1.5 text-sm`}
              >
                <svg
                  width="16"
                  height="16"
                  viewBox="0 0 16 16"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.75"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  aria-hidden="true"
                >
                  <path d="M10 3.5 5.5 8l4.5 4.5"></path>
                </svg>
                Schema {data.schema}
              </Link>
              <h1 className="break-all font-mono text-2xl font-medium tracking-tight text-slate-900">
                {data.schema}.{data.table}
              </h1>
            </div>
            <form
              className="flex flex-wrap items-center gap-3"
              onSubmit={(event) => event.preventDefault()}
            >
              <label htmlFor="limit" className="text-sm font-medium text-slate-800">
                Rows per page
              </label>
              <select
                id="limit"
                name="limit"
                value={data.limit}
                onChange={(e) =>
                  onLimitChange(Math.max(1, Math.min(200, Number(e.target.value) || 50)))
                }
                className="min-h-11 rounded-lg border border-slate-300 bg-white px-3 text-sm text-slate-900 hover:border-slate-400 focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-cyan-700 tabular-nums"
              >
                {pageSizes.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </form>
          </div>
          <div className="mt-6">
            {!data.ordered && (
              <p
                role="status"
                className="mb-4 rounded-lg border border-slate-200 bg-white p-4 text-sm leading-6 text-slate-800"
              >
                This table has no primary key, so rows may appear in a different order on each page.
              </p>
            )}
            {data.truncated && (
              <p
                role="status"
                className="mb-4 rounded-lg border border-slate-200 bg-white p-4 text-sm leading-6 text-slate-800"
              >
                This page was shortened to stay within the response size limit.
              </p>
            )}
            <div className={`${styles.panel} max-h-[70vh] overflow-auto`}>
              <table className="w-full border-separate border-spacing-0 text-left text-sm tabular-nums">
                <thead>
                  <tr>
                    {data.columns.map((c) => (
                      <th
                        key={c.name}
                        scope="col"
                        className="sticky top-0 z-10 whitespace-nowrap border-b border-slate-200 bg-slate-50 px-3 py-2 align-bottom"
                      >
                        <span className="block font-semibold text-slate-800">{c.name}</span>
                        <span className="block font-mono text-xs font-normal text-slate-600">
                          {c.type}
                        </span>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {data.rows.map((row, i) => (
                    <tr
                      key={i}
                      className="even:bg-slate-50 hover:bg-cyan-50 [&>td]:border-b [&>td]:border-slate-100"
                    >
                      {row.map((cell, j) => {
                        const long =
                          !cell.null &&
                          (Array.from(cell.text).length > 48 || cell.text.includes("\n"));
                        return (
                          <td
                            key={j}
                            className={`px-3 py-2 align-top font-mono text-[13px] leading-5 text-slate-900 ${long ? "min-w-64 max-w-md" : "whitespace-nowrap"}`}
                          >
                            {cell.null ? (
                              <span className="italic text-slate-600">NULL</span>
                            ) : (
                              <span
                                className={
                                  long
                                    ? cell.binary
                                      ? "break-all"
                                      : "whitespace-pre-wrap break-words"
                                    : "whitespace-pre"
                                }
                              >
                                {cell.text}
                              </span>
                            )}
                            {cell.truncated && (
                              <span className="ml-1 font-sans text-xs text-cyan-800">
                                … truncated
                              </span>
                            )}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.rows.length && (
                <p className="p-6 text-sm text-slate-600">No rows on this page.</p>
              )}
            </div>
            <nav
              aria-label="Pages"
              className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm"
            >
              <span className="text-slate-700 tabular-nums">
                {data.rows.length ? `Rows ${from}–${to}` : ""}
              </span>
              <span className="flex gap-2">
                {data.offset > 0 && (
                  <button
                    type="button"
                    onClick={() => onPage(Math.max(0, data.offset - data.limit), data.limit)}
                    className={styles.button}
                  >
                    Previous
                  </button>
                )}
                {data.has_next && (
                  <button
                    type="button"
                    onClick={() => onPage(data.offset + data.limit, data.limit)}
                    className={styles.button}
                  >
                    Next
                  </button>
                )}
              </span>
            </nav>
          </div>
        </>
      )}
    </div>
  );
}
