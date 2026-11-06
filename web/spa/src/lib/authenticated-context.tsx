import { createContext, useContext } from "react";
import type { AppContext } from "./query-client";
import type { Session } from "./auth";

export const AuthenticatedContext = createContext<(AppContext & { session: Session }) | null>(null);

export function useAuthenticatedContext() {
  const value = useContext(AuthenticatedContext);
  if (!value) throw new Error("Authenticated pages require a session.");
  return value;
}
