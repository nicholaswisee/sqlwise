import { createFileRoute } from "@tanstack/react-router";
import { MigrationDetailPage } from "../../../../../features/migrations/views";

export const Route = createFileRoute(
  "/_authenticated/databases/$databaseID/migrations/$migrationID",
)({
  head: () => ({ meta: [{ title: "Migration | sqlwise" }] }),
  component: () => {
    const params = Route.useParams();
    return <MigrationDetailPage db={params.databaseID} id={params.migrationID} />;
  },
});
