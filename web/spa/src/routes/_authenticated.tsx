import { createFileRoute, Outlet } from "@tanstack/react-router";
import { AuthGate } from "../features/auth/auth-gate";

export const Route = createFileRoute("/_authenticated")({ component: AuthenticatedGate });

function AuthenticatedGate() {
  const context = Route.useRouteContext();
  return (
    <AuthGate context={context}>
      <Outlet />
    </AuthGate>
  );
}
