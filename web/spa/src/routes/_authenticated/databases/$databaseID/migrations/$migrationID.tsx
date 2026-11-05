import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute(
  "/_authenticated/databases/$databaseID/migrations/$migrationID",
)({
  head: () => ({ meta: [{ title: "Migration | sqlwise" }] }),
  component: () => <Outlet />,
});
