import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { createMemoryHistory, Link, RouterContextProvider } from "@tanstack/react-router";
import { AuthGate } from "../src/features/auth/auth-gate";
import { getRouter } from "../src/router";
import { useAuthenticatedContext } from "../src/lib/authenticated-context";

function PrivateForm() {
  const { session } = useAuthenticatedContext();
  const [value, setValue] = useState("");
  return (
    <section>
      <h2>Private form for {session.user.username}</h2>
      <label>
        Private draft
        <input value={value} onChange={(event) => setValue(event.target.value)} />
      </label>
    </section>
  );
}

// Browser-only test fixture; uses the actual gate and context under StrictMode.
export async function mount(publicLink = false) {
  const router = getRouter();
  router.update({
    context: router.options.context,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  const host = document.createElement("div");
  host.id = "auth-gate-test";
  document.body.append(host);
  const root = createRoot(host);
  root.render(
    <StrictMode>
      <RouterContextProvider router={router}>
        {publicLink ? (
          <Link to="/account">Private account</Link>
        ) : (
          <AuthGate context={router.options.context}>
            <PrivateForm />
          </AuthGate>
        )}
      </RouterContextProvider>
    </StrictMode>,
  );
  return {
    clear: router.options.context.auth.clear,
    unmount: () => {
      root.unmount();
      host.remove();
    },
  };
}
