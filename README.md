# dashboards

A Hanzo Base-native Go service binary in the console tRPC→ZAP migration. It
replaces **five** in-process console tRPC routers — `dashboardRouter`,
`dashboardWidgetRouter`, `tableRouter`, `TableViewPresetsRouter`,
`monitorsRouter` — with one typed [ZAP](../zap) capability-RPC interface at
msgType **206**.

**Pattern:** Go binary built on [Hanzo Base](../base) (embedded encrypted SQLite
+ plugins) exposing a typed ZAP capability-RPC interface. No Prisma, no
Postgres-as-source-of-truth, no Mongo, no Cap'n Proto, no tRPC in the backend.

```
.zap schema (source of truth)  ──zapgen──▶  gen/ (Go views)
        │                                        │
        └──zapgen --target=ts──▶ console TS      ▼
                                   server/  ─ ZAP RPC handler (cap-gated)
                                   main.go  ─ base.New() + ZAP router :9995
                                              Base HTTP (health/metrics) :8090
                                              vault → per-org encrypted SQLite
```

## Run

```bash
make build
./dashboards serve --http=127.0.0.1:8090 --zap=127.0.0.1:9995
```

Optional per-org encrypted SQLite (vault plugin): add `--vaultDir=/data/vaults`.
The five backing collections (`dashboards`, `dashboard_widgets`,
`table_batch_jobs`, `table_view_presets`, `monitors`) are provisioned on first
boot (idempotent, see `server/collections.go`).

## Smoke test

In-process suite (per-method CRUD + per-category permission gate + pipelining
proof + analytics/queue 501 contracts):

```bash
make test
```

Out-of-process probe against a live binary (dashboard CRUD + pipelined
dashboard→widget create):

```bash
./dashboards serve --zap=127.0.0.1:9995 &
go run ./cmd/probe --addr 127.0.0.1:9995 --peer dashboards
```

## The interface

30 methods on a `CapKindIAMSession` capability, each gated on exactly one
`DashPermissions` bit (`server/wire.go`). The bits are the orthogonal product of
the five resource categories × {Read, Write} plus an analytics-read bit, mapping
1:1 to the console RBAC scopes:

| Category | Methods | DashPermissions bit ⇄ RBAC scope |
|----------|---------|----------------------------------|
| Dashboard CRUD | allDashboards, getDashboard, createDashboard, updateDashboardMetadata, updateDashboardDefinition, updateDashboardFilters, cloneDashboard, deleteDashboard | `dashboards:read` / `dashboards:CUD` |
| Widget CRUD | allWidgets, getWidget, createWidget, updateWidget, copyWidgetToProject, deleteWidget | (shares Dashboard bits) |
| Dashboard analytics | chart, scoreHistogram, executeQuery | `PermAnalyticsRead` — **501, ClickHouse shim pending** |
| Table batch-action | isBatchActionInProgress | `PermDashboardRead` — **501, BullMQ shim pending** |
| TableViewPreset CRUD | getPresetsByTableName, getPresetById, createPreset, updatePreset, updatePresetName, deletePreset, generatePermalink | `TableViewPresets:read` / `TableViewPresets:CUD` |
| Monitor CRUD | allMonitors, getMonitor, createMonitor, updateMonitor, deleteMonitor | `monitors:read` / `monitors:CUD` |

Capability auth is the single chokepoint `Server.authorize`: it Wraps the opaque
capability buffer, enforces `Kind == CapKindIAMSession` and the method's
`DashPermissions` bit (via `permissionFor`), and (when an issuer registry is
wired) verifies the signature. The signature step is stubbed in bootstrap (TODO
in `server.go`) — Kind + Permissions are always enforced. Every data query is
scoped to the `projectId` in the request — the multi-tenant boundary.

### Pipelining

`createDashboard`/`createWidget` support Cap'n Proto-style promise pipelining:
the widget call Targets the dashboard call's PromiseID, so the server resolves
the dashboard's project scope and dispatches the promised widget create against
it — eliding a client round trip. See `Client.PipelineCreateDashboardThenWidget`
and `TestPipelineDashboardThenWidget`.

### Not-yet-wired (TODO, return 501)

The analytics reads (chart/scoreHistogram/executeQuery) hit **ClickHouse**
(OLAP), and the table batch-action read hits a **BullMQ/Valkey** queue — neither
is Base. Both return `501` until their shims land; they never fabricate data:

- `server/analytics.go` — ClickHouse reader porting `getScoreAggregate`,
  `getNumericScoreHistogram`, `getObservation*ByTime`, and the `features/query`
  executeQuery builder from `@hanzo/shared`.
- `server/batchqueue.go` — Valkey/BullMQ reader porting `generateBatchActionId`
  + `getJobState` from `console/web/src/features/table/server`.

## Wiring the console to this service (handoff)

The console keeps its existing TS client (same `.zap` contract, compiled to TS by
`zapgen --target=ts` instead of Go). Its ZAP bridge substitutes the in-process
routers for a ZAP client to this service. The wire envelope is `(method:u32,
promiseID:u32, target:u32, cap:bytes, payload:bytes)` at ZAP msgType **206**;
responses are `(status:u32, promiseID:u32, body:bytes)`. Method ordinals match
the schema (`allDashboards`=0 … `deleteMonitor`=29).

This repo does **not** edit console.

## Layout

| Path | What |
|------|------|
| `proto/dashboards.zap` | Canonical schema (zap-spec dialect → Go + TS). |
| `gen/` | zapgen output (`make zap-gen`). Generated; do not edit. |
| `server/wire.go` | Transport envelope codec + msgType/method/status/permission consts. |
| `server/server.go` | ZAP RPC handler: cap auth, project scoping, dispatch, promise pipelining. |
| `server/handlers.go` | The 30 per-router method bodies (Base CRUD + analytics/queue TODOs). |
| `server/encode.go` | Base record → generated wire-struct projection (one encoder per model). |
| `server/collections.go` | The five Base collection schemas. |
| `server/client.go` | Typed client (one method per RPC) + pipelining + `SyntheticCap`. |
| `main.go` | Binary: `base.New()` + vault + ZAP router :9995. |
| `cmd/probe/` | Out-of-process smoke probe. |

Container registry: `ghcr.io/hanzoai/dashboards` (CI-built, multi-arch).
