import { expect, test } from "vite-plus/test";
import { renderToStaticMarkup } from "react-dom/server";
import { createMemoryHistory, RouterContextProvider } from "@tanstack/react-router";
import { getRouter } from "../src/router";
import { WorkspaceShell } from "../src/components/workspace";

test("workspace presents safe metadata, read-only access, and accessible version history", async () => {
  const router = getRouter();
  router.update({
    context: router.options.context,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <RouterContextProvider router={router}>
      <WorkspaceShell
        username="ada.lovelace"
        active="schema"
        workspace={{
          id: "db-1",
          name: "<Database>",
          can_edit: false,
          engine: "PostgreSQL",
          track_earlier: 2,
          version_track: [
            {
              label: "v1",
              state: "current",
              href: "/databases/db-1/migrations",
              aria: "Current version 1",
            },
            {
              label: "v2",
              state: "partial",
              href: "/databases/db-1/migrations",
              aria: "Partial version 2",
              note: "Stopped",
              note_tone: "warn",
            },
          ],
        }}
      >
        <h1>Schema</h1>
      </WorkspaceShell>
    </RouterContextProvider>,
  );
  expect(html).toContain("&lt;Database&gt;");
  expect(html).not.toContain("<Database>");
  expect(html).toContain("View only");
  expect(html).not.toContain("View + Edit");
  expect(html).toContain('aria-label="Version history"');
  expect(html).toContain('aria-label="Current version 1"');
  expect(html).toContain("+2 earlier");
  expect(html).toContain("Stopped");
  expect(html).toContain("AL</span>");
});
