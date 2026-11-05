import { createFileRoute } from "@tanstack/react-router";
import { ConnectionPage } from "../../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/databases/$id")({
  component: ConnectionRoute,
});

function ConnectionRoute() {
  const { id } = Route.useParams();
  return <ConnectionPage id={id} />;
}
