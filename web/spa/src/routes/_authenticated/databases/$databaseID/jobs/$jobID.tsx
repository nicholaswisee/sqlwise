import { createFileRoute } from "@tanstack/react-router";
import { JobDetailPage } from "../../../../../features/jobs/views";

export const Route = createFileRoute("/_authenticated/databases/$databaseID/jobs/$jobID")({
  head: () => ({ meta: [{ title: "Job | sqlwise" }] }),
  component: () => {
    const params = Route.useParams();
    return <JobDetailPage db={params.databaseID} jobID={params.jobID} />;
  },
});
