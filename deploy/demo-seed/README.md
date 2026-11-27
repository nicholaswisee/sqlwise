# Seed a disposable demo

The optional demo project sets up five logistics databases (four PostgreSQL and one MySQL), named users and groups, and ready-to-try migration requests. The real SQLwise worker applies the two starter migrations on Billing Ledger; the other requests are left for the demo. This seed is separate from the default app and the k6 benchmark. For Docker setup without demo data, see [Development](../../docs/development.md#run-with-docker).

## Prerequisites

Run commands from the repository root. Use Docker Compose 2.24.4 or later and OpenSSL, and ensure local port `18083` is free. The seed requires `SQLWISE_ADMIN_USERNAME`, `SQLWISE_ADMIN_PASSWORD`, and `SQLWISE_DEMO_PASSWORD`; the usernames must be valid and both passwords must be at least 12 characters (maximum 128 UTF-8 bytes). Keep the original passwords for this project so you can sign in and rerun it.

## Seed

Generate credentials once and run the seeder:

```sh
export SQLWISE_ADMIN_USERNAME=admin
export SQLWISE_ADMIN_PASSWORD="$(openssl rand -hex 24)"
export SQLWISE_DEMO_PASSWORD="$(openssl rand -hex 24)"
docker compose -p sqlwise-demo-seed -f compose.yaml -f deploy/demo-seed/compose.yaml run --build --rm seed
```

After the seeder succeeds, open <http://localhost:18083/login>. Sign in as `nadia.pratama` with `SQLWISE_DEMO_PASSWORD` to start on Delivery Orders, or as `admin` with `SQLWISE_ADMIN_PASSWORD` to manage the demo. All non-admin demo users share the supplied demo password.

Stable scenario names in the seed include:

- **Delivery Orders:** “Add delivery instructions” is pending review.
- **Billing Ledger:** “Record invoice issuer” and “Record payment reference” are the two starter migrations applied by the worker.
- **Dispatch Schedule:** “Add departure window” and “Add carrier notes” are ready-to-try requests.
- **Shipment Archive:** “Backfill shipment service level” is a checkpointed-batch request.

The seed also creates **Stock Catalog** and the groups and users defined in [`seed.py`](seed.py).

### Optional: collect telemetry

To use the [local observability stack](../observability/README.md), free its host ports first and include its Compose file:

```sh
docker compose -p sqlwise-demo-seed -f compose.yaml -f deploy/demo-seed/compose.yaml \
  -f deploy/observability/compose.yaml run --build --rm seed
```

Use the same set of Compose files to tear down the stack.

## Smoke test

Run the smoke test against the fresh demo before trying the scenarios:

```sh
docker compose -p sqlwise-demo-seed -f compose.yaml -f deploy/demo-seed/compose.yaml \
  run --rm --no-deps -e SQLWISE_VERIFY_DEMO=1 seed python -B -m unittest -v
```

## Rerun

To rerun, repeat the Compose seed command without generating new passwords. A successful seed does not duplicate records or revert progress. If the seed was interrupted after it began making changes, the seed state is incomplete; use the scoped reset below before seeding again.

## Reset

Remove only the disposable demo project to replay it from its original state. **`down -v` permanently deletes this project's demo data and migration history.** If you enabled the observability overlay, include it in the teardown command too:

```sh
docker compose -p sqlwise-demo-seed -f compose.yaml -f deploy/demo-seed/compose.yaml down -v
```

With the observability overlay enabled, use:

```sh
docker compose -p sqlwise-demo-seed -f compose.yaml -f deploy/demo-seed/compose.yaml \
  -f deploy/observability/compose.yaml down -v
```
