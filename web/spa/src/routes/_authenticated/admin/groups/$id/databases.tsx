import { createFileRoute } from "@tanstack/react-router";
import { GrantsPage } from "../../../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/groups/$id/databases")({
  head: () => ({ meta: [{ title: "Group database access | sqlwise" }] }),
  component: GroupDatabasesRoute,
});

function GroupDatabasesRoute() {
  const { id } = Route.useParams();
  return <GrantsPage groupId={id} />;
}
