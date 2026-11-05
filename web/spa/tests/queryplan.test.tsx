import { expect, test } from "vite-plus/test";
import { renderToStaticMarkup } from "react-dom/server";
import { PlanPanel, PlanResult } from "../src/features/queryplan/views";
import type { ExplainRequest, QueryPlan } from "../src/features/queryplan/types";

const fixture: QueryPlan = {
  dialect: "postgres",
  mode: "analyze",
  sql_hash: "abc",
  warnings: [],
  planning_time: undefined,
  execution_time: 2.25,
  root: {
    type: "Seq Scan",
    operation: "Seq Scan",
    relation: "users",
    index: "users_idx",
    join: "inner",
    startup_cost: 0,
    total_cost: 2.5,
    plan_rows: 3,
    actual_time: undefined,
    actual_rows: 1,
    loops: 1,
    children: [
      { type: "Index Scan", startup_cost: 0.1, total_cost: 1, plan_rows: 1, children: [] },
    ],
  },
};

test("plan rendering matches legacy metric formatting and recursively shows detail", () => {
  const html = renderToStaticMarkup(<PlanResult plan={fixture} />);
  expect(html).toContain("N/A");
  expect(html).toContain("2.25 ms");
  expect(html).toContain("Seq Scan on users · using users_idx · inner join");
  expect(html).toContain("0–2.5");
  expect(html).toContain("Index Scan");
});

test("plan controls retain Analyze confirmation and submit the API confirmed field", () => {
  const request: ExplainRequest = { sql: "SELECT 1", mode: "analyze", confirmed: true };
  expect(request.confirmed).toBe(true);
  const html = renderToStaticMarkup(
    <PlanPanel sql="SELECT 1" onExplain={() => Promise.resolve()} />,
  );
  expect(html).toContain("Analyze (sandbox only)");
  expect(html).toContain("I understand Analyze executes the SELECT on the sandbox database.");
  expect(html).not.toContain("<textarea");
});
