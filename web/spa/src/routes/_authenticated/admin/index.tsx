import { createFileRoute } from "@tanstack/react-router";
import { DashboardPage } from "../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/")({ component: DashboardPage });
