import { createFileRoute } from "@tanstack/react-router";
import { GroupsPage } from "../../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/groups/")({
  head: () => ({ meta: [{ title: "Groups | sqlwise" }] }),
  component: GroupsPage,
});
