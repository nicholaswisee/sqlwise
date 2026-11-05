import { createFileRoute } from "@tanstack/react-router";
import { AttemptDetailPage } from "../../../../../../../features/migrations/views";

export const Route = createFileRoute(
  "/_authenticated/databases/$databaseID/migrations/$migrationID/attempts/$attemptID",
)({
  head: () => ({ meta: [{ title: "Migration attempt | sqlwise" }] }),
  component: () => {
    const params = Route.useParams();
    return (
      <AttemptDetailPage
        db={params.databaseID}
        id={params.migrationID}
        attemptID={params.attemptID}
      />
    );
  },
});
