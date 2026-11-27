# CodeQL findings

This directory records CodeQL alerts and the scans that produced or verified them. Findings are indexed by GitHub alert ID; the IDs remain stable across alert updates.

## Findings

| ID                           | Rule                              | Severity | Disposition                  | GitHub                                                                        |
| ---------------------------- | --------------------------------- | -------- | ---------------------------- | ----------------------------------------------------------------------------- |
| [CQ-001](findings/CQ-001.md) | `go/incorrect-integer-conversion` | High     | Fixed                        | [Alert #1](https://github.com/nicholaswisee/sqlwise/security/code-scanning/1) |
| [CQ-002](findings/CQ-002.md) | `go/sql-injection`                | High     | Hardened; absent from rescan | [Alert #2](https://github.com/nicholaswisee/sqlwise/security/code-scanning/2) |
| [CQ-003](findings/CQ-003.md) | `go/log-injection`                | Medium   | Hardened; absent from rescan | [Alert #3](https://github.com/nicholaswisee/sqlwise/security/code-scanning/3) |

## Scans

- [Main scan, 2026-11-23](scans/scan-2026-11-23.md)
- [Main scan, 2026-11-26](scans/scan-2026-11-26.md)
- [Remediation rescan, 2026-11-27](scans/scan-2026-11-27-remediation.md)

## Running CodeQL

The workflow is [`.github/workflows/codeql.yml`](../../.github/workflows/codeql.yml). It runs on pushes, pull requests targeting `main`, a weekly schedule, and manual dispatch. Both jobs add the `security-extended` query suite. The Go job analyzes packages built by `CGO_ENABLED=1 go build ./...`, including `cmd/` and `internal/`. The JavaScript/TypeScript job analyzes checked-in source without a build, including `web/spa/` and `web/src/`. The workflow does not configure a separate scan for SQL or C.

To run the same Go build locally before analysis, use:

```sh
CGO_ENABLED=1 go build ./...
```

To run manually, open **Actions → CodeQL → Run workflow** and choose the branch. Check that both jobs completed, then review the results in [Security → Code scanning](https://github.com/nicholaswisee/sqlwise/security/code-scanning). A clean result means CodeQL found nothing in that run's analyzed scope. It does not prove the application is secure.

## Recording a finding

Use one file per GitHub alert, named for its stable `CQ-NNN` ID. Record the alert link, rule, severity, source location and revision, conditional potential impact, disposition, technical reasoning, remediation commit, and relevant scan links and IDs. Distinguish observed facts from conditional impact; do not include exploit instructions, secrets, or credentials. For work not yet completed, write `Pending` rather than inferring a fix commit or verification result. If a fix is declined, document the review reason and evidence supporting that decision. Add scan records with date, workflow run, revision, language analyses, analysis IDs, and any errors or warnings. Use relative links within this directory.
