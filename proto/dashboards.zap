# dashboards.zap — canonical wire schema for the Dashboards service.
#
# Dialect: zap-spec (the `package … / Field Type @off` grammar that
# github.com/zap-proto/go/cmd/zapgen consumes) — the SAME dialect the capability
# schema (zap-spec/capabilities.zap) is written in. ONE schema, two code targets:
# the console copies THIS file verbatim and compiles it with `zapgen --target=ts`
# into TS View/Builder classes over the native @hanzo/zap runtime; this repo
# compiles the same file with `zapgen` (Go target) into gen/dashboards_zap.go.
# No capnp on either side — the field set below is the single byte-for-byte
# contract both speak.
#
# RPC surface — interface Dashboards @ MsgTypeRouterBase (206). Hand-dispatched
# in server/ (zapgen emits data views, never method stubs; the dispatch + cap
# gate live in server.go, exactly as cap/ hand-writes Verify on zapgen'd views):
#
#   # --- Dashboard CRUD (dashboardRouter) -----------------------------------
#   allDashboards            @0  (ListReq)        -> (DashboardList)   dashboards:read
#   getDashboard             @1  (IdReq)          -> (Dashboard)       dashboards:read
#   createDashboard          @2  (CreateDashReq)  -> (Dashboard)       dashboards:CUD
#   updateDashboardMetadata  @3  (UpdateDashReq)  -> (Dashboard)       dashboards:CUD
#   updateDashboardDef       @4  (DashDefReq)     -> (Dashboard)       dashboards:CUD
#   updateDashboardFilters   @5  (DashFiltersReq) -> (Dashboard)       dashboards:CUD
#   cloneDashboard           @6  (IdReq)          -> (Dashboard)       dashboards:CUD
#   deleteDashboard          @7  (IdReq)          -> (Mutation)        dashboards:CUD
#   # --- Dashboard analytics (dashboardRouter) — ClickHouse, see server.go ---
#   chart                    @8  (AnalyticsReq)   -> (AnalyticsResult) dashboards:read
#   scoreHistogram           @9  (AnalyticsReq)   -> (AnalyticsResult) dashboards:read
#   executeQuery             @10 (AnalyticsReq)   -> (AnalyticsResult) dashboards:read
#   # --- Widget CRUD (dashboardWidgetRouter) --------------------------------
#   allWidgets               @11 (ListReq)        -> (WidgetList)      dashboards:read
#   getWidget                @12 (IdReq)          -> (Widget)          dashboards:read
#   createWidget             @13 (WidgetReq)      -> (Widget)          dashboards:CUD
#   updateWidget             @14 (WidgetReq)      -> (Widget)          dashboards:CUD
#   copyWidgetToProject      @15 (CopyWidgetReq)  -> (Mutation)        dashboards:CUD
#   deleteWidget             @16 (IdReq)          -> (Mutation)        dashboards:CUD
#   # --- Table batch-action (tableRouter) — BullMQ queue, see server.go ------
#   isBatchActionInProgress  @17 (BatchActionReq) -> (BoolResult)      dashboards:read
#   # --- TableViewPreset CRUD (TableViewPresetsRouter) ----------------------
#   getPresetsByTableName    @18 (PresetListReq)  -> (PresetList)      TableViewPresets:read
#   getPresetById            @19 (IdReq)          -> (Preset)          TableViewPresets:read
#   createPreset             @20 (PresetReq)      -> (Preset)          TableViewPresets:CUD
#   updatePreset             @21 (PresetReq)      -> (Preset)          TableViewPresets:CUD
#   updatePresetName         @22 (PresetNameReq)  -> (Preset)          TableViewPresets:CUD
#   deletePreset             @23 (IdReq)          -> (Mutation)        TableViewPresets:CUD
#   generatePermalink        @24 (PermalinkReq)   -> (StringResult)    TableViewPresets:read
#   # --- Monitor CRUD (monitorsRouter) --------------------------------------
#   allMonitors              @25 (ListReq)        -> (MonitorList)     monitors:read
#   getMonitor               @26 (IdReq)          -> (Monitor)         monitors:read
#   createMonitor            @27 (MonitorReq)     -> (Monitor)         monitors:CUD
#   updateMonitor            @28 (MonitorReq)     -> (Monitor)         monitors:CUD
#   deleteMonitor            @29 (IdReq)          -> (Mutation)        monitors:CUD
#
# Permission model: the caller's verified Capability (CapKindIAMSession = 0x01)
# carries a u64 Permissions bitmask. Each method gates on exactly one DashPermissions
# bit (server.go requirePermission(cap, bit)) — the single chokepoint. The bits are
# the orthogonal product of {Dashboard, Widget, Table, Preset, Monitor} × {Read, Write}
# plus the analytics-read bit. See server/wire.go for the canonical bit values.

package dashboards

# ── Common request envelopes ────────────────────────────────────────────────

# IdReq addresses one row by id within a project (getDashboard, getWidget,
# cloneDashboard, delete*, getPresetById, getMonitor). id carries the
# dashboardId / widgetId / presetId / monitorId.
struct IdReq {
    ProjectId text @0
    Id        text @8
}

# ListReq is the paginated list envelope shared by allDashboards / allWidgets /
# allMonitors. orderByColumn+orderByOrder mirror the tRPC `orderBy` object.
struct ListReq {
    ProjectId     text @0
    Page          u32  @8
    Limit         u32  @12
    OrderByColumn text @16
    OrderByOrder  text @24
}

# ── Dashboard ───────────────────────────────────────────────────────────────

# Dashboard mirrors the DashboardDomain the tRPC router returned. `definition`,
# `filters` carry the JSON blobs verbatim (the wire model of the structured
# DashboardDefinition / filter array — opaque to the transport, parsed by the UI).
struct Dashboard {
    Id          text @0
    ProjectId   text @8
    Name        text @16
    Description text @24
    Owner       text @32   # "PROJECT" | "HANZO"
    Definition  text @40   # JSON: { widgets: [...] }
    Filters     text @48   # JSON: singleFilter[]
    CreatedBy   text @56
    CreatedAt   text @64   # RFC3339
    UpdatedAt   text @72   # RFC3339
}

struct DashboardList {
    Dashboards list<Dashboard> @0
    TotalCount u32             @8
}

struct CreateDashReq {
    ProjectId   text @0
    Name        text @8
    Description text @16
}

struct UpdateDashReq {
    ProjectId   text @0
    DashboardId text @8
    Name        text @16
    Description text @24
}

struct DashDefReq {
    ProjectId   text @0
    DashboardId text @8
    Definition  text @16   # JSON: validated client-side against DashboardDefinitionSchema
}

struct DashFiltersReq {
    ProjectId   text @0
    DashboardId text @8
    Filters     text @16   # JSON: singleFilter[]
}

# ── Widget ──────────────────────────────────────────────────────────────────

# Widget mirrors WidgetDomain. dimensions/metrics/filters/chartConfig are JSON
# blobs carried verbatim; view/chartType are the string enums.
struct Widget {
    Id          text @0
    ProjectId   text @8
    Name        text @16
    Description text @24
    View        text @32   # "traces" | "observations" | "scores-numeric" | "scores-categorical"
    Owner       text @40   # "PROJECT" | "HANZO"
    Dimensions  text @48   # JSON: DimensionSchema[]
    Metrics     text @56   # JSON: MetricSchema[]
    Filters     text @64   # JSON: singleFilter[]
    ChartType   text @72
    ChartConfig text @80   # JSON: ChartConfigSchema
    CreatedAt   text @88   # RFC3339
    UpdatedAt   text @96   # RFC3339
}

struct WidgetList {
    Widgets    list<Widget> @0
    TotalCount u32          @8
}

struct WidgetReq {
    ProjectId   text @0
    WidgetId    text @8    # "" on create
    Name        text @16
    Description text @24
    View        text @32
    Dimensions  text @40   # JSON
    Metrics     text @48   # JSON
    Filters     text @56   # JSON
    ChartType   text @64
    ChartConfig text @72   # JSON
}

struct CopyWidgetReq {
    ProjectId   text @0
    WidgetId    text @8
    DashboardId text @16
    PlacementId text @24
}

# ── Table batch-action ──────────────────────────────────────────────────────

struct BatchActionReq {
    ProjectId text @0
    TableName text @8
    ActionId  text @16
}

# ── TableViewPreset ─────────────────────────────────────────────────────────

# Preset mirrors a TableViewPreset row. filters/columnOrder/columnVisibility are
# JSON blobs carried verbatim.
struct Preset {
    Id               text @0
    ProjectId        text @8
    Name             text @16
    TableName        text @24
    Filters          text @32   # JSON: singleFilter[]
    ColumnOrder      text @40   # JSON: string[]
    ColumnVisibility text @48   # JSON: Record<string,bool>
    SearchQuery      text @56
    OrderByColumn    text @64
    OrderByOrder     text @72
    CreatedBy        text @80
    CreatedAt        text @88   # RFC3339
    UpdatedAt        text @96   # RFC3339
}

struct PresetList {
    Presets list<Preset> @0
}

struct PresetListReq {
    ProjectId text @0
    TableName text @8
}

struct PresetReq {
    ProjectId        text @0
    PresetId         text @8    # "" on create
    Name             text @16
    TableName        text @24
    Filters          text @32   # JSON
    ColumnOrder      text @40   # JSON
    ColumnVisibility text @48   # JSON
    SearchQuery      text @56
    OrderByColumn    text @64
    OrderByOrder     text @72
}

struct PresetNameReq {
    ProjectId text @0
    PresetId  text @8
    Name      text @16
    TableName text @24
}

struct PermalinkReq {
    ProjectId text @0
    PresetId  text @8
    TableName text @16
    BaseUrl   text @24
}

# ── Monitor ─────────────────────────────────────────────────────────────────

# Monitor mirrors a Monitor row. The query/threshold/state config travels as JSON
# blobs verbatim (filters/metric/window/noData/renotify/tags), with the scalar
# config and state columns broken out for typed access + ordering.
struct Monitor {
    Id                text @0
    ProjectId         text @8
    Name              text @16
    View              text @24
    Filters           text @32   # JSON: MonitorFilters
    Metric            text @40   # JSON: MetricSchema
    Window            text @48   # JSON: MonitorWindow
    ThresholdOperator text @56
    AlertThreshold    f64  @64
    WarningThreshold  f64  @72   # NaN when null
    NoData            text @80   # JSON: MonitorNoData
    Renotify          text @88   # JSON: MonitorRenotify
    Tags              text @96   # JSON: string[]
    Status            text @104
    Severity          text @112
    SeverityChangedAt text @120  # RFC3339, "" when null
    AlertedAt         text @128  # RFC3339, "" when null
    NextRunAt         text @136  # RFC3339
    CreatedBy         text @144
    CreatedAt         text @152  # RFC3339
    UpdatedAt         text @160  # RFC3339
}

struct MonitorList {
    Monitors   list<Monitor> @0
    TotalCount u32           @8
}

struct MonitorReq {
    ProjectId         text @0
    MonitorId         text @8    # "" on create
    Name              text @16
    View              text @24
    Filters           text @32   # JSON
    Metric            text @40   # JSON
    Window            text @48   # JSON
    ThresholdOperator text @56
    AlertThreshold    f64  @64
    WarningThreshold  f64  @72   # NaN when unset
    NoData            text @80   # JSON
    Renotify          text @88   # JSON
    Tags              text @96   # JSON
    Status            text @104
}

# ── Generic result envelopes ────────────────────────────────────────────────

# Mutation is the { success } shape the delete/copy mutations returned.
struct Mutation {
    Success bool @0
    Id      text @8   # affected/created id when applicable, else ""
}

struct BoolResult {
    Value bool @0
}

struct StringResult {
    Value text @0
}

# AnalyticsResult carries a ClickHouse query result back to the UI as a JSON
# array (DatabaseRow[] / histogram bins / executeQuery rows). Opaque to the
# transport — see server.go for the ClickHouse-shim wiring location.
struct AnalyticsResult {
    Rows text @0   # JSON array
}

struct AnalyticsReq {
    ProjectId text @0
    QueryName text @8    # nullable enum on chart; "" otherwise
    Filter    text @16   # JSON: filterInterface
    Query     text @24   # JSON: QueryType (executeQuery) | sqlInterface
    Limit     u32  @32
}
