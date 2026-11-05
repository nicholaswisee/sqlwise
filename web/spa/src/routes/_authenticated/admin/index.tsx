import { createFileRoute } from "@tanstack/react-router";
import { DashboardPage } from "../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/")({
  head: () => ({ meta: [{ title: "Administration | sqlwise" }] }),
  component: DashboardPage,
});
