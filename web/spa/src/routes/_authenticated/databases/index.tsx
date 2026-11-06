import { useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { DatabaseList } from "../../../features/explorer/views";
import { WorkspaceTopbar } from "../../../components/workspace";
import { useAuthenticatedContext } from "../../../lib/authenticated-context";

type DatabaseListResponse = {
  username: string;
  databases: { id: string; name: string; permission: string; engine?: string }[];
};
export const Route = createFileRoute("/_authenticated/databases/")({ component: DatabasesPage });

function DatabasesPage() {
  const { api, session } = useAuthenticatedContext();
  const query = useQuery({
    queryKey: ["databases", session.user.id],
    queryFn: ({ signal }) => api.request<DatabaseListResponse>("/databases", { signal }),
  });
  useEffect(() => {
    document.title = "Databases | sqlwise";
  }, []);
  return (
    <div className="flex min-h-screen flex-col bg-slate-100 text-slate-900">
      <WorkspaceTopbar username={session.user.username}>
        <nav
          aria-label="Breadcrumb"
          className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600"
        >
          <span aria-current="page" className="font-medium text-slate-900">
            Databases
          </span>
        </nav>
      </WorkspaceTopbar>
      <DatabaseList
        databases={query.data?.databases ?? []}
        loading={query.isPending}
        error={query.error?.message}
        href={(id) => `/databases/${encodeURIComponent(id)}`}
      />
    </div>
  );
}
