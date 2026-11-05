import { createFileRoute } from "@tanstack/react-router";
import { MigrationHistoryPage } from "../../../../../features/migrations/views";

export const Route = createFileRoute("/_authenticated/databases/$databaseID/migrations/")({
  head: () => ({ meta: [{ title: "Migrations | sqlwise" }] }),
  component: () => <MigrationHistoryPage db={Route.useParams().databaseID} />,
});
