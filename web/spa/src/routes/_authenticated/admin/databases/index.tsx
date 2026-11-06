import { createFileRoute } from "@tanstack/react-router";
import { ConnectionsPage } from "../../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/databases/")({
  head: () => ({ meta: [{ title: "Connections | sqlwise" }] }),
  component: ConnectionsPage,
});
