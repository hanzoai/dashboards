package server

import (
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
)

// The five Base collections backing this service — one per model the migrated
// tRPC routers persisted. Every row is scoped by `projectId`; the service reads
// and writes ONLY within the project carried in the verified capability, the
// multi-tenant boundary (mirrors console throwIfNoProjectAccess(projectId)).
//
// Backed by Base's encrypted SQLite (vault plugin KEK per org) when vault is
// registered; plain SQLite otherwise. No Mongo, no Prisma — Base is the store.
const (
	CollectionDashboard = "dashboards"        // dashboardRouter
	CollectionWidget    = "dashboard_widgets" // dashboardWidgetRouter
	CollectionTable     = "table_batch_jobs"  // tableRouter (batch-action job tracking)
	CollectionPreset    = "table_view_presets"
	CollectionMonitor   = "monitors"
)

// Shared column names. projectId is on every collection (the tenant key).
const (
	fProjectId = "projectId"
	fName      = "name"
	fCreatedBy = "createdBy"
)

// Dashboard columns (mirror DashboardDomain). JSON blobs (definition, filters)
// are stored verbatim and round-tripped to the UI untouched.
const (
	fDashDescription = "description"
	fDashOwner       = "owner"
	fDashDefinition  = "definition"
	fDashFilters     = "filters"
)

// Widget columns (mirror WidgetDomain).
const (
	fWidgetDescription = "description"
	fWidgetView        = "view"
	fWidgetOwner       = "owner"
	fWidgetDimensions  = "dimensions"
	fWidgetMetrics     = "metrics"
	fWidgetFilters     = "filters"
	fWidgetChartType   = "chartType"
	fWidgetChartConfig = "chartConfig"
)

// Table batch-job columns. The BullMQ queue is the source of truth for live job
// state (see server.handleIsBatchActionInProgress); this collection records the
// submitted (projectId, tableName, actionId) tuple for audit/lookup symmetry.
const (
	fTableTableName = "tableName"
	fTableActionId  = "actionId"
	fTableState     = "state"
)

// Preset columns (mirror a TableViewPreset row).
const (
	fPresetTableName        = "tableName"
	fPresetFilters          = "filters"
	fPresetColumnOrder      = "columnOrder"
	fPresetColumnVisibility = "columnVisibility"
	fPresetSearchQuery      = "searchQuery"
	fPresetOrderByColumn    = "orderByColumn"
	fPresetOrderByOrder     = "orderByOrder"
)

// Monitor columns (mirror a Monitor row). The query/threshold/state config that
// the UI treats as structured travels as JSON blobs (filters, metric, window,
// noData, renotify, tags); scalars are broken out for typed access + ordering.
const (
	fMonitorView              = "view"
	fMonitorFilters           = "filters"
	fMonitorMetric            = "metric"
	fMonitorWindow            = "window"
	fMonitorThresholdOperator = "thresholdOperator"
	fMonitorAlertThreshold    = "alertThreshold"
	fMonitorWarningThreshold  = "warningThreshold"
	fMonitorNoData            = "noData"
	fMonitorRenotify          = "renotify"
	fMonitorTags              = "tags"
	fMonitorStatus            = "status"
	fMonitorSeverity          = "severity"
	fMonitorSeverityChangedAt = "severityChangedAt"
	fMonitorAlertedAt         = "alertedAt"
	fMonitorNextRunAt         = "nextRunAt"
)

// RegisterCollections ensures all five collections exist on first boot.
// Idempotent: re-running finds existing collections and no-ops. Wired via
// OnBootstrap so it runs once at startup, before the ZAP listener accepts calls.
func RegisterCollections(app core.App) {
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Id: "dashboardsCollections",
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil { // init DB first
				return err
			}
			return EnsureCollections(app)
		},
	})
}

// EnsureCollections creates any missing collection. Exported so callers that
// bootstrap the app themselves (tests, one-shot migrations) can provision them
// directly rather than via the OnBootstrap hook. Idempotent per collection.
func EnsureCollections(app core.App) error {
	for _, build := range []func(core.App) error{
		ensureDashboard, ensureWidget, ensureTable, ensurePreset, ensureMonitor,
	} {
		if err := build(app); err != nil {
			return err
		}
	}
	return nil
}

// exists reports whether a collection is already provisioned.
func exists(app core.App, name string) bool {
	_, err := app.FindCollectionByNameOrId(name)
	return err == nil
}

// text adds TextFields with a generous cap (JSON blobs use jsonField instead).
func text(col *core.Collection, names ...string) {
	for _, n := range names {
		col.Fields.Add(&core.TextField{Name: n, Max: 4096})
	}
}

// jsonField adds JSONFields sized for structured blobs (definitions, filters).
func jsonField(col *core.Collection, names ...string) {
	for _, n := range names {
		col.Fields.Add(&core.JSONField{Name: n, MaxSize: 262144})
	}
}

// timestamps adds the created/updated autodate pair every collection carries.
func timestamps(col *core.Collection) {
	col.Fields.Add(&core.AutodateField{Name: "created", OnCreate: true})
	col.Fields.Add(&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true})
}

func ensureDashboard(app core.App) error {
	if exists(app, CollectionDashboard) {
		return nil
	}
	col := core.NewBaseCollection(CollectionDashboard)
	col.Fields.Add(&core.TextField{Name: fProjectId, Required: true, Max: 255})
	text(col, fName, fDashDescription, fDashOwner, fCreatedBy)
	jsonField(col, fDashDefinition, fDashFilters)
	timestamps(col)
	col.AddIndex("idx_dashboards_project", false, fProjectId, "")
	return app.Save(col)
}

func ensureWidget(app core.App) error {
	if exists(app, CollectionWidget) {
		return nil
	}
	col := core.NewBaseCollection(CollectionWidget)
	col.Fields.Add(&core.TextField{Name: fProjectId, Required: true, Max: 255})
	text(col, fName, fWidgetDescription, fWidgetView, fWidgetOwner, fWidgetChartType, fCreatedBy)
	jsonField(col, fWidgetDimensions, fWidgetMetrics, fWidgetFilters, fWidgetChartConfig)
	timestamps(col)
	col.AddIndex("idx_widgets_project", false, fProjectId, "")
	return app.Save(col)
}

func ensureTable(app core.App) error {
	if exists(app, CollectionTable) {
		return nil
	}
	col := core.NewBaseCollection(CollectionTable)
	col.Fields.Add(&core.TextField{Name: fProjectId, Required: true, Max: 255})
	text(col, fTableTableName, fTableActionId, fTableState, fCreatedBy)
	timestamps(col)
	col.AddIndex("idx_table_jobs_project", false, fProjectId, "")
	return app.Save(col)
}

func ensurePreset(app core.App) error {
	if exists(app, CollectionPreset) {
		return nil
	}
	col := core.NewBaseCollection(CollectionPreset)
	col.Fields.Add(&core.TextField{Name: fProjectId, Required: true, Max: 255})
	text(col, fName, fPresetTableName, fPresetSearchQuery,
		fPresetOrderByColumn, fPresetOrderByOrder, fCreatedBy)
	jsonField(col, fPresetFilters, fPresetColumnOrder, fPresetColumnVisibility)
	timestamps(col)
	// Unique (projectId, tableName, name): mirrors the console P2002 conflict the
	// TableViewPresetsRouter maps to HanzoConflictError.
	col.AddIndex("idx_presets_unique", true, fProjectId+", "+fPresetTableName+", "+fName, "")
	return app.Save(col)
}

func ensureMonitor(app core.App) error {
	if exists(app, CollectionMonitor) {
		return nil
	}
	col := core.NewBaseCollection(CollectionMonitor)
	col.Fields.Add(&core.TextField{Name: fProjectId, Required: true, Max: 255})
	text(col, fName, fMonitorView, fMonitorThresholdOperator, fMonitorStatus,
		fMonitorSeverity, fMonitorSeverityChangedAt, fMonitorAlertedAt, fMonitorNextRunAt, fCreatedBy)
	col.Fields.Add(&core.NumberField{Name: fMonitorAlertThreshold})
	// warningThreshold is z.number().nullable() upstream. Base's NumberField is
	// NUMERIC DEFAULT 0 NOT NULL — it cannot hold SQL NULL — so the nullable
	// number is stored as text: a decimal string when set, "" when null. This
	// preserves the source's three-state (set / null) faithfully; the wire still
	// carries an f64 with NaN as the null sentinel (see encode/handlers).
	text(col, fMonitorWarningThreshold)
	jsonField(col, fMonitorFilters, fMonitorMetric, fMonitorWindow,
		fMonitorNoData, fMonitorRenotify, fMonitorTags)
	timestamps(col)
	col.AddIndex("idx_monitors_project", false, fProjectId, "")
	return app.Save(col)
}
