import { renderToString } from "react-dom/server";
import { expect, test } from "vite-plus/test";
import { AuthenticatedContext, useAuthenticatedContext } from "../src/lib/authenticated-context";
import { createAppContext } from "../src/lib/query-client";

function PrivateIdentity() {
  return <span>{useAuthenticatedContext().session.user.username}</span>;
}

test("private components require a checked session provider", () => {
  expect(() => renderToString(<PrivateIdentity />)).toThrow(
    "Authenticated pages require a session.",
  );
  const context = createAppContext();
  const session = {
    user: { id: "actor", username: "editor", is_platform_admin: false },
    csrf_token: "csrf",
    expires_at: "2099-01-01",
    databases: [],
    needs_refresh: false,
  };
  expect(
    renderToString(
      <AuthenticatedContext value={{ ...context, session }}>
        <PrivateIdentity />
      </AuthenticatedContext>,
    ),
  ).toBe("<span>editor</span>");
});
