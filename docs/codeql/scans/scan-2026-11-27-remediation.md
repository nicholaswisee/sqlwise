# CodeQL remediation rescan: 2026-11-27

- **Workflow run:** [45925118126](https://github.com/nicholaswisee/sqlwise/actions/runs/45925118126) (push to `fix/codeql-findings` for [PR #16](https://github.com/nicholaswisee/sqlwise/pull/16))
- **Scanned revision:** [`baf1e1a`](https://github.com/nicholaswisee/sqlwise/commit/baf1e1ab79b8c177e5d5d005fc6957c6c1404b99)
- **Scanner:** CodeQL 2.27.6, with the workflow's `security-extended` queries
- **GitHub analysis API errors or warnings:** None reported

| Language and scope                                                                              | Analysis ID  | Results |
| ----------------------------------------------------------------------------------------------- | ------------ | ------: |
| Go application packages built by `go build ./...` with cgo, including `cmd/` and `internal/`    | `2260156418` |       0 |
| Checked-in JavaScript/TypeScript, including `web/spa/` and `web/src/`, analyzed without a build | `2260154434` |       0 |

Both jobs succeeded. Neither [CQ-002](../findings/CQ-002.md) nor [CQ-003](../findings/CQ-003.md) recurred on the branch; [CQ-001](../findings/CQ-001.md) also did not recur. This record describes the first revision containing both hardening changes. See the [PR checks](https://github.com/nicholaswisee/sqlwise/pull/16/checks) for the follow-up that rejects ambiguous MySQL metadata. GitHub updates default-branch alert states after scanning `main`.

Zero results mean no alerts were reported for the code and queries covered by this run. They do not prove the application is secure.
