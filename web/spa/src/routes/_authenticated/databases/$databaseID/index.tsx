import { useEffect } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { SchemaExplorer } from "../../../../features/explorer/views";
import type { SchemaExplorerDTO } from "../../../../features/explorer/types";
import { WorkspaceShell } from "../../../../components/workspace";
import { useAuthenticatedContext } from "../../../../lib/authenticated-context";

type Search = { schema?: string; table?: string };
export const Route = createFileRoute("/_authenticated/databases/$databaseID/")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    schema: typeof search.schema === "string" ? search.schema : undefined,
    table: typeof search.table === "string" ? search.table : undefined,
  }),
  component: DatabaseSchemaPage,
});

function DatabaseSchemaPage() {
  const { api, session } = useAuthenticatedContext();
  const { databaseID } = Route.useParams();
  const { schema, table } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const query = useQuery({
    queryKey: ["database-explorer", session.user.id, databaseID, schema ?? ""],
    queryFn: ({ signal }) =>
      api.request<SchemaExplorerDTO>(
        `/databases/${encodeURIComponent(databaseID)}${schema ? `?schema=${encodeURIComponent(schema)}` : ""}`,
        { signal },
      ),
  });
  useEffect(() => {
    document.title = query.data
      ? `Schema | ${query.data.workspace.name} | sqlwise`
      : "Schema | sqlwise";
  }, [query.data]);
  const workspace = query.data?.workspace;
  return workspace ? (
    <WorkspaceShell workspace={workspace} username={session.user.username} active="schema">
      <SchemaExplorer
        data={query.data!}
        selectedTableName={table}
        onSchemaChange={(next) => void navigate({ search: { schema: next }, resetScroll: false })}
        schemaHref={(next) =>
          `/databases/${encodeURIComponent(databaseID)}?schema=${encodeURIComponent(next)}`
        }
        tableHref={(currentSchema, selected) =>
          `/databases/${encodeURIComponent(databaseID)}?schema=${encodeURIComponent(currentSchema)}&table=${encodeURIComponent(selected)}`
        }
        dataHref={(tableID) =>
          `/databases/${encodeURIComponent(databaseID)}/tables/${encodeURIComponent(tableID)}?offset=0&limit=50`
        }
        loading={false}
        error={query.error?.message}
      />
    </WorkspaceShell>
  ) : (
    <div className="min-h-screen bg-slate-100 p-4">
      {query.isPending ? (
        <p role="status" className="text-sm text-slate-600">
          Loading schema…
        </p>
      ) : (
        <p
          role="alert"
          className="rounded-lg border border-rose-200 bg-rose-50 p-4 text-sm text-rose-900"
        >
          {query.error?.message ?? "Schema unavailable."}
        </p>
      )}
    </div>
  );
}
