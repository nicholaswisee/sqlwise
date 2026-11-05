import { createFileRoute } from "@tanstack/react-router";
import { UsersPage } from "../../../features/admin/pages";

export const Route = createFileRoute("/_authenticated/admin/users")({ component: UsersPage });
