import { createFileRoute } from "@tanstack/react-router";
import { JobsPage } from "../../../../../features/jobs/views";

export const Route = createFileRoute("/_authenticated/databases/$databaseID/jobs/")({
  head: () => ({ meta: [{ title: "Jobs | sqlwise" }] }),
  component: () => <JobsPage db={Route.useParams().databaseID} />,
});
