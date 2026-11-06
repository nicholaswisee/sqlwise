import { useEffect } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { TableDataView } from "../../../../../features/explorer/views";
import type { TableDataDTO } from "../../../../../features/explorer/types";
import { WorkspaceShell } from "../../../../../components/workspace";
import { useAuthenticatedContext } from "../../../../../lib/authenticated-context";

type Search = { offset: number; limit: number };
export const Route = createFileRoute("/_authenticated/databases/$databaseID/tables/$tableID")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    offset: Math.max(0, Number(search.offset) || 0),
    limit: Math.max(1, Math.min(200, Number(search.limit) || 50)),
  }),
  component: TablePage,
});

function TablePage() {
  const { api, session } = useAuthenticatedContext();
  const { databaseID, tableID } = Route.useParams();
  const { offset, limit } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const query = useQuery({
    queryKey: ["database-table", session.user.id, databaseID, tableID, offset, limit],
    queryFn: ({ signal }) =>
      api.request<TableDataDTO>(
        `/databases/${encodeURIComponent(databaseID)}/tables/${encodeURIComponent(tableID)}?offset=${offset}&limit=${limit}`,
        { signal },
      ),
  });
  useEffect(() => {
    document.title = query.data
      ? `${query.data.table} | ${query.data.workspace.name} | sqlwise`
      : "Table data | sqlwise";
  }, [query.data]);
  const workspace = query.data?.workspace;
  return workspace ? (
    <WorkspaceShell workspace={workspace} username={session.user.username} active="schema">
      <TableDataView
        data={query.data!}
        onPage={(nextOffset, nextLimit) =>
          void navigate({ search: { offset: nextOffset, limit: nextLimit }, resetScroll: false })
        }
        onLimitChange={(nextLimit) =>
          void navigate({ search: { offset: 0, limit: nextLimit }, resetScroll: false })
        }
        schemaHref={(schema, table) =>
          `/databases/${encodeURIComponent(databaseID)}?schema=${encodeURIComponent(schema)}&table=${encodeURIComponent(table)}`
        }
        loading={false}
        error={query.error?.message}
      />
    </WorkspaceShell>
  ) : (
    <div className="min-h-screen bg-slate-100 p-4">
      {query.isPending ? (
        <p role="status" className="text-sm text-slate-600">
          Loading table data…
        </p>
      ) : (
        <p
          role="alert"
          className="rounded-lg border border-rose-200 bg-rose-50 p-4 text-sm text-rose-900"
        >
          {query.error?.message ?? "Table data unavailable."}
        </p>
      )}
    </div>
  );
}
