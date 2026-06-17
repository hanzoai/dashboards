package server

import (
	"encoding/hex"
	"strconv"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"
	zaplib "github.com/luxfi/zap"
	zcap "github.com/zap-proto/go/cap"

	gen "github.com/hanzoai/dashboards/gen"
)

// handlers.go holds the per-router method bodies. Each is a thin, total mapping
// between a generated request view and the project-scoped Base collection,
// returning a generated result view. The capability gate + project scoping have
// already run in server.handle — handlers trust `project` is the authorized
// tenant and never re-check the cap.

// ─────────────────────────────── dashboardRouter ───────────────────────────

func (s *Server) handleAllDashboards(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapListReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad ListReq: "+err.Error())
	}
	rows, total, err := s.listScoped(CollectionDashboard, project,
		sortExpr(in.OrderByColumn(), in.OrderByOrder()), int(in.Page()), int(in.Limit()))
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	items := make([][]byte, len(rows))
	for i, r := range rows {
		items[i] = encodeDashboard(r)
	}
	return s.ok(req, gen.NewDashboardList(gen.DashboardListInput{Dashboards: items, TotalCount: uint32(total)}))
}

func (s *Server) handleGetDashboard(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionDashboard, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "dashboard not found")
	}
	return s.ok(req, encodeDashboard(rec))
}

func (s *Server) handleCreateDashboard(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapCreateDashReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad CreateDashReq: "+err.Error())
	}
	if in.Name() == "" {
		return s.fail(req, StatusBadRequest, "dashboard name is required")
	}
	col, err := s.app.FindCollectionByNameOrId(CollectionDashboard)
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	rec := core.NewRecord(col)
	rec.Set(fProjectId, project)
	rec.Set(fName, in.Name())
	rec.Set(fDashDescription, in.Description())
	rec.Set(fDashOwner, "PROJECT")
	rec.Set(fDashDefinition, `{"widgets":[]}`)
	rec.Set(fDashFilters, "[]")
	rec.Set(fCreatedBy, holderOf(req))
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeDashboard(rec))
}

func (s *Server) handleUpdateDashboardMeta(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapUpdateDashReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad UpdateDashReq: "+err.Error())
	}
	if in.Name() == "" {
		return s.fail(req, StatusBadRequest, "dashboard name is required")
	}
	rec, err := s.findScoped(CollectionDashboard, project, in.DashboardId())
	if err != nil {
		return s.fail(req, StatusNotFound, "dashboard not found")
	}
	rec.Set(fName, in.Name())
	rec.Set(fDashDescription, in.Description())
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeDashboard(rec))
}

func (s *Server) handleUpdateDashboardDef(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapDashDefReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad DashDefReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionDashboard, project, in.DashboardId())
	if err != nil {
		return s.fail(req, StatusNotFound, "dashboard not found")
	}
	rec.Set(fDashDefinition, jsonOrEmpty(in.Definition(), `{"widgets":[]}`))
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeDashboard(rec))
}

func (s *Server) handleUpdateDashboardFilters(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapDashFiltersReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad DashFiltersReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionDashboard, project, in.DashboardId())
	if err != nil {
		return s.fail(req, StatusNotFound, "dashboard not found")
	}
	rec.Set(fDashFilters, jsonOrEmpty(in.Filters(), "[]"))
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeDashboard(rec))
}

func (s *Server) handleCloneDashboard(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	src, err := s.findScoped(CollectionDashboard, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "source dashboard not found")
	}
	col, err := s.app.FindCollectionByNameOrId(CollectionDashboard)
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	clone := core.NewRecord(col)
	clone.Set(fProjectId, project)
	clone.Set(fName, src.GetString(fName)+" (Clone)")
	clone.Set(fDashDescription, src.GetString(fDashDescription))
	clone.Set(fDashOwner, "PROJECT")
	clone.Set(fDashDefinition, src.GetString(fDashDefinition))
	clone.Set(fDashFilters, src.GetString(fDashFilters))
	clone.Set(fCreatedBy, holderOf(req))
	if err := s.app.Save(clone); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeDashboard(clone))
}

func (s *Server) handleDeleteDashboard(req Call, project string) (*zaplib.Message, error) {
	return s.deleteScoped(req, CollectionDashboard, project)
}

// ─────────────────────────── dashboardRouter analytics ─────────────────────

// handleAnalytics serves chart / scoreHistogram / executeQuery. These read from
// ClickHouse (OLAP), NOT from Base (OLTP). The query implementations live in
// console's @hanzo/shared analytics layer (getScoreAggregate,
// getNumericScoreHistogram, getObservation*ByTime, and the query-builder in
// features/query). Until the ClickHouse client is wired into this binary the
// method returns 501; it never fabricates rows.
//
// TODO(clickhouse): wire a ClickHouse reader and port the analytics functions
//
//	from ~/work/hanzo/console/web/../packages/shared/src/server (score-analytics
//	+ the features/query executeQuery builder) into a server/analytics.go shim
//	that returns AnalyticsResult.Rows as the JSON array the UI already expects.
func (s *Server) handleAnalytics(req Call) (*zaplib.Message, error) {
	return s.fail(req, StatusNotImpl,
		"analytics queries require the ClickHouse shim (server/analytics.go) — not yet wired")
}

// ──────────────────────────── dashboardWidgetRouter ────────────────────────

func (s *Server) handleAllWidgets(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapListReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad ListReq: "+err.Error())
	}
	rows, total, err := s.listScoped(CollectionWidget, project,
		sortExpr(in.OrderByColumn(), in.OrderByOrder()), int(in.Page()), int(in.Limit()))
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	items := make([][]byte, len(rows))
	for i, r := range rows {
		items[i] = encodeWidget(r)
	}
	return s.ok(req, gen.NewWidgetList(gen.WidgetListInput{Widgets: items, TotalCount: uint32(total)}))
}

func (s *Server) handleGetWidget(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionWidget, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "widget not found")
	}
	return s.ok(req, encodeWidget(rec))
}

// handleUpsertWidget creates (update=false) or updates (update=true) a widget.
func (s *Server) handleUpsertWidget(req Call, project string, update bool) (*zaplib.Message, error) {
	in, err := gen.WrapWidgetReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad WidgetReq: "+err.Error())
	}
	if in.Name() == "" {
		return s.fail(req, StatusBadRequest, "widget name is required")
	}
	var rec *core.Record
	if update {
		rec, err = s.findScoped(CollectionWidget, project, in.WidgetId())
		if err != nil {
			return s.fail(req, StatusNotFound, "widget not found")
		}
	} else {
		col, cerr := s.app.FindCollectionByNameOrId(CollectionWidget)
		if cerr != nil {
			return s.fail(req, StatusInternal, cerr.Error())
		}
		rec = core.NewRecord(col)
		rec.Set(fProjectId, project)
		rec.Set(fWidgetOwner, "PROJECT")
		rec.Set(fCreatedBy, holderOf(req))
	}
	rec.Set(fName, in.Name())
	rec.Set(fWidgetDescription, in.Description())
	rec.Set(fWidgetView, in.View())
	rec.Set(fWidgetDimensions, jsonOrEmpty(in.Dimensions(), "[]"))
	rec.Set(fWidgetMetrics, jsonOrEmpty(in.Metrics(), "[]"))
	rec.Set(fWidgetFilters, jsonOrEmpty(in.Filters(), "[]"))
	rec.Set(fWidgetChartType, in.ChartType())
	rec.Set(fWidgetChartConfig, jsonOrEmpty(in.ChartConfig(), "{}"))
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeWidget(rec))
}

func (s *Server) handleCopyWidget(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapCopyWidgetReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad CopyWidgetReq: "+err.Error())
	}
	src, err := s.findScoped(CollectionWidget, project, in.WidgetId())
	if err != nil {
		return s.fail(req, StatusNotFound, "source widget not found")
	}
	col, err := s.app.FindCollectionByNameOrId(CollectionWidget)
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	copyRec := core.NewRecord(col)
	copyRec.Set(fProjectId, project)
	copyRec.Set(fName, src.GetString(fName))
	copyRec.Set(fWidgetDescription, src.GetString(fWidgetDescription))
	copyRec.Set(fWidgetView, src.GetString(fWidgetView))
	copyRec.Set(fWidgetOwner, "PROJECT")
	copyRec.Set(fWidgetDimensions, src.GetString(fWidgetDimensions))
	copyRec.Set(fWidgetMetrics, src.GetString(fWidgetMetrics))
	copyRec.Set(fWidgetFilters, src.GetString(fWidgetFilters))
	copyRec.Set(fWidgetChartType, src.GetString(fWidgetChartType))
	copyRec.Set(fWidgetChartConfig, src.GetString(fWidgetChartConfig))
	copyRec.Set(fCreatedBy, holderOf(req))
	if err := s.app.Save(copyRec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, gen.NewMutation(gen.MutationInput{Success: true, Id: copyRec.Id}))
}

func (s *Server) handleDeleteWidget(req Call, project string) (*zaplib.Message, error) {
	return s.deleteScoped(req, CollectionWidget, project)
}

// ─────────────────────────────── tableRouter ───────────────────────────────

// handleIsBatchActionInProgress mirrors tableRouter.getIsBatchActionInProgress,
// which queries the BullMQ BatchActionQueue for the job state of
// (projectId, tableName, actionId). The queue (Valkey/BullMQ) is the source of
// truth for live job state — Base does NOT hold it. Until the queue client is
// wired into this binary the method returns 501; it never guesses a state.
//
// TODO(queue): wire a Valkey/BullMQ reader (hanzoai/kv) and port
//
//	generateBatchActionId + getJobState from
//	~/work/hanzo/console/web/src/features/table/server/{helpers,tableRouter}.ts
//	into a server/batchqueue.go shim returning BoolResult{Value: inProgress}.
func (s *Server) handleIsBatchActionInProgress(req Call) (*zaplib.Message, error) {
	return s.fail(req, StatusNotImpl,
		"batch-action progress requires the BullMQ queue shim (server/batchqueue.go) — not yet wired")
}

// ────────────────────────── TableViewPresetsRouter ─────────────────────────

func (s *Server) handleGetPresetsByTableName(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapPresetListReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad PresetListReq: "+err.Error())
	}
	rows, err := s.app.FindRecordsByFilter(CollectionPreset,
		"projectId = {:p} && tableName = {:t}", "-created", 0, 0,
		dbx.Params{"p": project, "t": in.TableName()})
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	items := make([][]byte, len(rows))
	for i, r := range rows {
		items[i] = encodePreset(r)
	}
	return s.ok(req, gen.NewPresetList(gen.PresetListInput{Presets: items}))
}

func (s *Server) handleGetPresetById(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionPreset, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "preset not found")
	}
	return s.ok(req, encodePreset(rec))
}

func (s *Server) handleUpsertPreset(req Call, project string, update bool) (*zaplib.Message, error) {
	in, err := gen.WrapPresetReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad PresetReq: "+err.Error())
	}
	if in.Name() == "" {
		return s.fail(req, StatusBadRequest, "view name is required")
	}
	var rec *core.Record
	if update {
		rec, err = s.findScoped(CollectionPreset, project, in.PresetId())
		if err != nil {
			return s.fail(req, StatusNotFound, "preset not found")
		}
	} else {
		col, cerr := s.app.FindCollectionByNameOrId(CollectionPreset)
		if cerr != nil {
			return s.fail(req, StatusInternal, cerr.Error())
		}
		rec = core.NewRecord(col)
		rec.Set(fProjectId, project)
		rec.Set(fCreatedBy, holderOf(req))
	}
	rec.Set(fName, in.Name())
	rec.Set(fPresetTableName, in.TableName())
	rec.Set(fPresetFilters, jsonOrEmpty(in.Filters(), "[]"))
	rec.Set(fPresetColumnOrder, jsonOrEmpty(in.ColumnOrder(), "[]"))
	rec.Set(fPresetColumnVisibility, jsonOrEmpty(in.ColumnVisibility(), "{}"))
	rec.Set(fPresetSearchQuery, in.SearchQuery())
	rec.Set(fPresetOrderByColumn, in.OrderByColumn())
	rec.Set(fPresetOrderByOrder, in.OrderByOrder())
	if err := s.app.Save(rec); err != nil {
		// The unique (projectId, tableName, name) index surfaces a duplicate as a
		// constraint error → 409, mirroring the console HanzoConflictError.
		return s.fail(req, StatusConflict, "preset with this name already exists: "+err.Error())
	}
	return s.ok(req, encodePreset(rec))
}

func (s *Server) handleUpdatePresetName(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapPresetNameReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad PresetNameReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionPreset, project, in.PresetId())
	if err != nil {
		return s.fail(req, StatusNotFound, "preset not found")
	}
	rec.Set(fName, in.Name())
	rec.Set(fPresetTableName, in.TableName())
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusConflict, "preset name conflict: "+err.Error())
	}
	return s.ok(req, encodePreset(rec))
}

func (s *Server) handleDeletePreset(req Call, project string) (*zaplib.Message, error) {
	return s.deleteScoped(req, CollectionPreset, project)
}

// handleGeneratePermalink builds the deep-link URL to a saved preset. Pure string
// composition (no I/O) — base/preset/table identity is all that's needed, mirror
// of TableViewService.generatePermalink.
func (s *Server) handleGeneratePermalink(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapPermalinkReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad PermalinkReq: "+err.Error())
	}
	if _, err := s.findScoped(CollectionPreset, project, in.PresetId()); err != nil {
		return s.fail(req, StatusNotFound, "preset not found")
	}
	link := in.BaseUrl() + "/project/" + project + "/" + in.TableName() + "?viewId=" + in.PresetId()
	return s.ok(req, gen.NewStringResult(gen.StringResultInput{Value: link}))
}

// ─────────────────────────────── monitorsRouter ────────────────────────────

func (s *Server) handleAllMonitors(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapListReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad ListReq: "+err.Error())
	}
	rows, total, err := s.listScoped(CollectionMonitor, project,
		sortExpr(in.OrderByColumn(), in.OrderByOrder()), int(in.Page()), int(in.Limit()))
	if err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	items := make([][]byte, len(rows))
	for i, r := range rows {
		items[i] = encodeMonitor(r)
	}
	return s.ok(req, gen.NewMonitorList(gen.MonitorListInput{Monitors: items, TotalCount: uint32(total)}))
}

func (s *Server) handleGetMonitor(req Call, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	rec, err := s.findScoped(CollectionMonitor, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "monitor not found")
	}
	return s.ok(req, encodeMonitor(rec))
}

func (s *Server) handleUpsertMonitor(req Call, project string, update bool) (*zaplib.Message, error) {
	in, err := gen.WrapMonitorReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad MonitorReq: "+err.Error())
	}
	if in.Name() == "" {
		return s.fail(req, StatusBadRequest, "monitor name is required")
	}
	var rec *core.Record
	if update {
		rec, err = s.findScoped(CollectionMonitor, project, in.MonitorId())
		if err != nil {
			return s.fail(req, StatusNotFound, "monitor not found")
		}
	} else {
		col, cerr := s.app.FindCollectionByNameOrId(CollectionMonitor)
		if cerr != nil {
			return s.fail(req, StatusInternal, cerr.Error())
		}
		rec = core.NewRecord(col)
		rec.Set(fProjectId, project)
		rec.Set(fCreatedBy, holderOf(req))
		rec.Set(fMonitorSeverity, "unknown")
		rec.Set(fMonitorNextRunAt, "") // owned by the scheduler; seed empty
	}
	rec.Set(fName, in.Name())
	rec.Set(fMonitorView, in.View())
	rec.Set(fMonitorFilters, jsonOrEmpty(in.Filters(), "[]"))
	rec.Set(fMonitorMetric, jsonOrEmpty(in.Metric(), "{}"))
	rec.Set(fMonitorWindow, jsonOrEmpty(in.Window(), "{}"))
	rec.Set(fMonitorThresholdOperator, in.ThresholdOperator())
	rec.Set(fMonitorAlertThreshold, in.AlertThreshold())
	rec.Set(fMonitorWarningThreshold, floatToNullableText(in.WarningThreshold()))
	rec.Set(fMonitorNoData, jsonOrEmpty(in.NoData(), `{"mode":"SILENT"}`))
	rec.Set(fMonitorRenotify, jsonOrEmpty(in.Renotify(), `{"mode":"OFF"}`))
	rec.Set(fMonitorTags, jsonOrEmpty(in.Tags(), "[]"))
	rec.Set(fMonitorStatus, orDefault(in.Status(), "active"))
	if err := s.app.Save(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, encodeMonitor(rec))
}

func (s *Server) handleDeleteMonitor(req Call, project string) (*zaplib.Message, error) {
	return s.deleteScoped(req, CollectionMonitor, project)
}

// ─────────────────────────── shared delete + helpers ───────────────────────

// deleteScoped deletes one row by id within the project (the tenant guard) and
// returns the { success } mutation shape the tRPC delete procedures returned.
func (s *Server) deleteScoped(req Call, collection, project string) (*zaplib.Message, error) {
	in, err := gen.WrapIdReq(req.Payload)
	if err != nil {
		return s.fail(req, StatusBadRequest, "bad IdReq: "+err.Error())
	}
	rec, err := s.findScoped(collection, project, in.Id())
	if err != nil {
		return s.fail(req, StatusNotFound, "record not found")
	}
	if err := s.app.Delete(rec); err != nil {
		return s.fail(req, StatusInternal, err.Error())
	}
	return s.ok(req, gen.NewMutation(gen.MutationInput{Success: true, Id: in.Id()}))
}

// jsonOrEmpty returns raw if it is non-empty, else the fallback default JSON.
func jsonOrEmpty(raw, def string) string {
	if raw == "" || raw == "null" {
		return def
	}
	return raw
}

// orDefault returns v if non-empty, else def.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// floatToNullableText encodes a wire f64 into the nullable-number text column:
// "" for the NaN null sentinel, else the decimal string. nullableTextToFloat is
// the inverse (in encode.go).
func floatToNullableText(f float64) string {
	if isNull(f) {
		return ""
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// holderOf returns the hex-encoded 32-byte holder hash of the call's capability
// as the createdBy attribution — the verified identity of the caller. The cap
// has already passed authorize(), so Wrap cannot fail here; the guard is belt-
// and-braces and yields "" rather than panicking.
func holderOf(req Call) string {
	c, err := zcap.Wrap(req.Cap)
	if err != nil {
		return ""
	}
	h := c.Holder()
	return hex.EncodeToString(h[:])
}
