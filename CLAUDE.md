# CLAUDE.md — contract for AI helpers

A Hanzo Base-native Go service in the console tRPC→ZAP migration. It replaces
five tRPC routers (dashboards + widgets + tables + view-presets + monitors) with
one typed ZAP capability router at msgType **206**.

## The one rule

**The `.zap` schema is the source of truth.** `proto/dashboards.zap` defines the
data structs; `gen/` is its Go projection via `make zap-gen`. Never hand-edit
`gen/`. Change the schema, regenerate, then update `server/`.

## Two schema dialects (do not conflate)

- `proto/dashboards.zap` is the **zap-spec dialect** (`package … / Field Type
  @off`) that `github.com/zap-proto/go/cmd/zapgen` compiles to Go. This is what
  THIS repo (a Go service) consumes.
- The console copies the same file and compiles it with `zapgen --target=ts`
  into TS View/Builder classes over the `@hanzo/zap` runtime. ONE schema, two
  code targets — no capnp on either side. The field set is the shared contract.

## Build / test

- Pure-Go always: `CGO_ENABLED=0`. CGO pulls blst/accel C deps that need a full
  toolchain and break reproducibility. `make` sets this + `GOWORK=off`.
- `GOWORK=off`: this repo is self-contained via go.mod `replace`s; a parent
  `go.work` must not capture it.
- `make zap-gen` and `make build` are **idempotent** (byte-identical regen,
  reproducible binary). Keep them so — no timestamps/paths in generated output.
- `make test` runs the in-process suite (per-method CRUD + per-category
  permission gate + pipelining + analytics/queue 501 contracts). Show it
  passing; don't claim "done" without it.

## Architecture invariants (DRY, orthogonal, decomplected)

- **Three wire layers, separated:** transport (`luxfi/zap` Node, msgType 206) /
  envelope (`server/wire.go`) / payload (`gen/` typed views). The capability is
  carried as OPAQUE bytes through all three — auth is a value, not a place.
- **One auth chokepoint:** `Server.authorize`. Kind + the method's
  `DashPermissions` bit (`permissionFor` in `wire.go`) always enforced; signature
  verify gated on a wired issuer registry (TODO). Do not scatter permission
  checks into the method handlers. Method→permission policy lives in ONE place
  (`permissionFor`), read once at dispatch.
- **One tenant boundary:** every data query is scoped to the request `projectId`
  (`findScoped`/`listScoped`). A row in another project is NotFound, never read.
- **One backend:** Hanzo Base. No Prisma, Postgres-as-source-of-truth, Mongo,
  Redis, tRPC, nginx. The five collections live in Base (encrypted SQLite via the
  vault plugin when `--vaultDir` is set).
- **One record→wire mapping:** `server/encode.go`. Handlers never pluck fields
  ad hoc.
- **Pipelining is real:** `createWidget` can target `createDashboard`'s promise;
  the server's promise table (`await`/`resolve`) joins them on the resolved
  project scope. Genuine in-flight pipelining needs the two calls on SEPARATE
  connections (the transport is FIFO per connection) — see
  `Client.PipelineCreateDashboardThenWidget` and the pipelining test.

## Not-yet-wired (return 501, never fake)

- Dashboard analytics (chart/scoreHistogram/executeQuery) read **ClickHouse**
  (OLAP), not Base. `server/handleAnalytics` returns 501 with the shim location
  (`server/analytics.go`).
- Table batch-action progress reads a **BullMQ/Valkey** queue, not Base.
  `server/handleIsBatchActionInProgress` returns 501 with the shim location
  (`server/batchqueue.go`).

## Do not

- Build Docker images locally (CI does, multi-arch → ghcr.io/hanzoai).
- Push to GitHub from here unless asked.
- Add plugins this one service doesn't need. Lean binary.
- Touch the console — wiring is documented in README, not done here.
