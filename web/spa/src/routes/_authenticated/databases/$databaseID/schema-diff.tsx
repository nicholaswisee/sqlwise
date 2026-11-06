import { createFileRoute } from "@tanstack/react-router";
import { SchemaDiffPage } from "../../../../features/migrations/views";

export const Route = createFileRoute("/_authenticated/databases/$databaseID/schema-diff")({
  head: () => ({ meta: [{ title: "Schema diff | sqlwise" }] }),
  component: () => <SchemaDiffPage db={Route.useParams().databaseID} />,
});
