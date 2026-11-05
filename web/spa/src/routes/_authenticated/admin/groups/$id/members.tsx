import { createFileRoute } from "@tanstack/react-router";
import { MembersPage } from "../../../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/groups/$id/members")({
  component: GroupMembersRoute,
});

function GroupMembersRoute() {
  const { id } = Route.useParams();
  return <MembersPage groupId={id} />;
}
