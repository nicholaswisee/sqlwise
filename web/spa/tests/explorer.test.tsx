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
  offset: 50,
  limit: 25,
  has_next: true,
  ordered: true,
  truncated: false,
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
  expect(html).toContain("Rows 51–53");
});
