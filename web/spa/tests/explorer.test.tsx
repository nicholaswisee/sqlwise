import { expect, test } from "vite-plus/test";
import { renderToStaticMarkup } from "react-dom/server";
import { createMemoryHistory, RouterContextProvider } from "@tanstack/react-router";
import { getRouter } from "../src/router";
import { TableDataView } from "../src/features/explorer/views";
import type { TableDataDTO } from "../src/features/explorer/types";

const table: TableDataDTO = {
  workspace: {
    id: "db",
    name: "Demo",
    can_edit: false,
    version_track: [],
    capabilities: {
      connection_test: false,
      schema_inspection: true,
      data_preview: true,
      migration_execution: false,
      atomic_migrations: false,
      schema_diff: false,
      explain: false,
      explain_analyze: false,
    },
    current_version: 0,
    track_earlier: 0,
  },
  schema: "public",
  table: "items",
  offset: 150,
  limit: 75,
  has_next: true,
  ordered: false,
  truncated: true,
  columns: [{ name: "payload", type: "bytea" }],
  rows: [
    [{ text: "\\xdeadbeef", null: false, binary: true, truncated: false }],
    [{ text: "", null: true, binary: false, truncated: false }],
    [{ text: "long…", null: false, binary: false, truncated: true }],
  ],
};

test("table cells preserve binary text, NULL, truncation, and absolute page offsets", async () => {
  const router = getRouter();
  router.update({
    context: router.options.context,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <RouterContextProvider router={router}>
      <TableDataView
        data={table}
        onPage={() => {}}
        onLimitChange={() => {}}
        schemaHref={() => "/databases/db"}
      />
    </RouterContextProvider>,
  );
  expect(html).toContain("\\xdeadbeef");
  expect(html).not.toContain("Binary data");
  expect(html).toContain("NULL");
  expect(html).toContain("… truncated");
  expect(html).toContain("Rows 151–153");
  expect(html).toContain('value="75" selected=""');
  expect(html).toContain("leading-6 text-slate-800");
  expect(html).toContain(
    'class="rounded-xl border border-slate-200 bg-white max-h-\[70vh\] overflow-auto"',
  );
  expect(html).toContain("hover:border-slate-400");
  expect(html).toContain('name="limit"');
  expect(html).toContain(">Previous</button>");
  expect(html).toContain(">Next</button>");
  expect(html).toContain('viewBox="0 0 16 16"');
  expect(html).toContain('<path d="M10 3.5 5.5 8l4.5 4.5"></path>');
});
