import { ApiError } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import type { AdminTitle } from "./views";
import {
  useAdminActions,
  useAdminSnapshot,
  useConnection,
  useConnections,
  field,
  connectionBody,
} from "./data";
import {
  ConnectionDetailView,
  ConnectionsView,
  DashboardView,
  GrantsView,
  GroupsView,
  MembersView,
  UsersView,
} from "./views";
import { AdminShell } from "./views";
import { wsAlert, wsHeading, wsMuted } from "./ui";

function Loading({ title }: { title: string }) {
  return (
    <main aria-busy="true">
      <h1 className={wsHeading}>{title}</h1>
      <p className={`${wsMuted} mt-2`} role="status">
        Loading…
      </p>
    </main>
  );
}

function QueryFailure({
  title,
  error,
  retry,
  connectionsAvailable = true,
}: {
  title: AdminTitle;
  error: Error;
  retry: () => void | Promise<unknown>;
  connectionsAvailable?: boolean;
}) {
  const denied = error instanceof ApiError && error.status === 403;
  const missing = error instanceof ApiError && error.status === 404;
  const stale =
    error instanceof ApiError && error.status === 409 && error.code === "authorization_stale";
  return (
    <AdminShell title={title} connectionsAvailable={connectionsAvailable}>
      <main>
        <h1 className={wsHeading}>{title}</h1>
        <div role="alert" className={`${wsAlert} mt-6`}>
          {denied
            ? "You do not have permission to view this page."
            : missing
              ? "The requested resource was not found."
              : stale
                ? "Your permissions changed. Refresh and try again."
                : error.message || "The request could not be completed."}
          {stale && (
            <button type="button" className="ml-3 underline" onClick={retry}>
              Refresh session and data
            </button>
          )}
        </div>
        {!denied && !missing && !stale && (
          <button type="button" className="mt-4 underline" onClick={retry}>
            Try again
          </button>
        )}
      </main>
    </AdminShell>
  );
}

function useSnapshotPage(path: string, title: AdminTitle) {
  const query = useAdminSnapshot(path);
  const actions = useAdminActions();
  const { auth, queryClient } = useAuthenticatedContext();
  const refresh = async () => {
    const current = await auth.session();
    if (current)
      await queryClient.invalidateQueries({ queryKey: ["admin", current.user.id, path] });
  };
  if (query.isPending && !query.data)
    return {
      component: (
        <AdminShell title={title} connectionsAvailable>
          <Loading title={title} />
        </AdminShell>
      ),
    };
  if (
    query.isError &&
    (!query.data || (query.error instanceof ApiError && [403, 404].includes(query.error.status)))
  )
    return {
      component: (
        <QueryFailure
          title={title}
          error={query.error}
          retry={() =>
            query.error instanceof ApiError && query.error.status === 409
              ? refresh()
              : query.refetch()
          }
        />
      ),
    };
  return { query, actions };
}

export function DashboardPage() {
  const result = useSnapshotPage("/admin/groups", "Administration");
  if ("component" in result) return result.component;
  const stale =
    result.query.isError &&
    result.query.error instanceof ApiError &&
    result.query.error.code === "authorization_stale";
  return (
    <DashboardView
      data={result.query.data!}
      status={
        result.query.isError
          ? {
              error: result.query.error.message,
              refreshNeeded: stale,
              onRefresh: result.actions.onRefresh,
            }
          : result.actions.status
      }
      connectionsAvailable
    />
  );
}

export function UsersPage() {
  const result = useSnapshotPage("/admin/users", "Users");
  if ("component" in result) return result.component;
  const { run, ...status } = result.actions;
  const data = result.query.data!;
  const stale =
    result.query.isError &&
    result.query.error instanceof ApiError &&
    result.query.error.code === "authorization_stale";
  return (
    <UsersView
      data={data}
      connectionsAvailable
      message={status.message}
      error={result.query.isError ? result.query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onCreate={(form) =>
        run("/admin/users", {
          username: field(form, "username"),
          password: field(form, "password"),
        })
      }
      onRename={(id, form) =>
        run(`/admin/users/${encodeURIComponent(id)}`, { username: field(form, "username") })
      }
      onPassword={(id, form) =>
        run(`/admin/users/${encodeURIComponent(id)}`, { password: field(form, "password") })
      }
      onAdmin={(id, admin) => run(`/admin/users/${encodeURIComponent(id)}/admin`, { admin }, true)}
      onActive={(id, active) =>
        run(`/admin/users/${encodeURIComponent(id)}/active`, { active }, true)
      }
    />
  );
}

export function GroupsPage() {
  const result = useSnapshotPage("/admin/groups", "Groups");
  if ("component" in result) return result.component;
  const { run, ...status } = result.actions;
  const data = result.query.data!;
  const stale =
    result.query.isError &&
    result.query.error instanceof ApiError &&
    result.query.error.code === "authorization_stale";
  return (
    <GroupsView
      data={data}
      connectionsAvailable
      message={status.message}
      error={result.query.isError ? result.query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onCreate={(form) => run("/admin/groups", { name: field(form, "name") })}
      onRename={(id, form) =>
        run(`/admin/groups/${encodeURIComponent(id)}`, { name: field(form, "name") })
      }
      onActive={(id, active) =>
        run(`/admin/groups/${encodeURIComponent(id)}/active`, { active }, true)
      }
    />
  );
}

export function MembersPage({ groupId }: { groupId: string }) {
  const result = useSnapshotPage(
    `/admin/groups/${encodeURIComponent(groupId)}/members`,
    "Group members",
  );
  if ("component" in result) return result.component;
  const { run, ...status } = result.actions;
  const data = result.query.data!;
  const stale =
    result.query.isError &&
    result.query.error instanceof ApiError &&
    result.query.error.code === "authorization_stale";
  return (
    <MembersView
      data={data}
      connectionsAvailable
      message={status.message}
      error={result.query.isError ? result.query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onAdd={(form) =>
        run(
          `/admin/groups/${encodeURIComponent(groupId)}/members`,
          { user_id: field(form, "user_id") },
          true,
        )
      }
      onRemove={(userId) =>
        run(
          `/admin/groups/${encodeURIComponent(groupId)}/members/remove`,
          { user_id: userId },
          true,
        )
      }
    />
  );
}

export function GrantsPage({ groupId }: { groupId: string }) {
  const result = useSnapshotPage(
    `/admin/groups/${encodeURIComponent(groupId)}/databases`,
    "Group database access",
  );
  if ("component" in result) return result.component;
  const { run, ...status } = result.actions;
  const data = result.query.data!;
  const stale =
    result.query.isError &&
    result.query.error instanceof ApiError &&
    result.query.error.code === "authorization_stale";
  return (
    <GrantsView
      data={data}
      connectionsAvailable
      message={status.message}
      error={result.query.isError ? result.query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onSave={(form) =>
        run(
          `/admin/groups/${encodeURIComponent(groupId)}/databases`,
          { database_id: field(form, "database_id"), permission: field(form, "permission") },
          true,
        )
      }
      onRemove={(databaseId) =>
        run(
          `/admin/groups/${encodeURIComponent(groupId)}/databases/remove`,
          { database_id: databaseId },
          true,
        )
      }
    />
  );
}

export function ConnectionsPage() {
  const query = useConnections();
  const actions = useAdminActions();
  if (query.isPending && !query.data)
    return (
      <AdminShell title="Connections" connectionsAvailable>
        <Loading title="Connections" />
      </AdminShell>
    );
  if (
    query.isError &&
    (!query.data || (query.error instanceof ApiError && [403, 404].includes(query.error.status)))
  )
    return (
      <QueryFailure
        title="Connections"
        error={query.error}
        retry={
          query.error instanceof ApiError && query.error.status === 409
            ? actions.onRefresh
            : () => void query.refetch()
        }
      />
    );
  const { run, ...status } = actions;
  const stale =
    query.isError && query.error instanceof ApiError && query.error.code === "authorization_stale";
  return (
    <ConnectionsView
      connections={query.data!.connections ?? []}
      message={status.message}
      error={query.isError ? query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onCreate={(form) => run("/admin/databases", connectionBody(form))}
    />
  );
}

export function ConnectionPage({ id }: { id: string }) {
  const query = useConnection(id);
  const actions = useAdminActions();
  if (query.isPending && !query.data)
    return (
      <AdminShell title="Edit connection" connectionsAvailable>
        <Loading title="Edit connection" />
      </AdminShell>
    );
  if (
    query.isError &&
    (!query.data || (query.error instanceof ApiError && [403, 404].includes(query.error.status)))
  )
    return (
      <QueryFailure
        title="Edit connection"
        error={query.error}
        retry={
          query.error instanceof ApiError && query.error.status === 409
            ? actions.onRefresh
            : () => void query.refetch()
        }
      />
    );
  const { run, ...status } = actions;
  const detail = query.data!;
  const stale =
    query.isError && query.error instanceof ApiError && query.error.code === "authorization_stale";
  return (
    <ConnectionDetailView
      detail={detail}
      message={status.message}
      error={query.isError ? query.error.message : status.error}
      pending={status.pending}
      refreshNeeded={stale || status.refreshNeeded}
      onRefresh={status.onRefresh}
      onSave={(form) => run(`/admin/databases/${encodeURIComponent(id)}`, connectionBody(form))}
      onTest={() => run(`/admin/databases/${encodeURIComponent(id)}/test`)}
      onActive={(active) =>
        run(`/admin/databases/${encodeURIComponent(id)}/active`, { active }, true)
      }
    />
  );
}
