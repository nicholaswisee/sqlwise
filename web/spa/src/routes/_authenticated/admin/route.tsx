import { Outlet, createFileRoute } from "@tanstack/react-router";
import { useAuthenticatedContext } from "../../../lib/authenticated-context";
import { wsAlert } from "../../../features/admin/ui";
import { AdminShell } from "../../../features/admin/views";

export const Route = createFileRoute("/_authenticated/admin")({ component: AdminLayout });

function AdminLayout() {
  const { session } = useAuthenticatedContext();
  if (!session.user.is_platform_admin)
    return (
      <AdminShell title="Administration" connectionsAvailable>
        <main>
          <h1 className="text-2xl font-semibold tracking-tight text-slate-900">
            Platform administration
          </h1>
          <p role="alert" className={`${wsAlert} mt-6`}>
            You do not have permission to view this page.
          </p>
        </main>
      </AdminShell>
    );
  return <Outlet />;
}
