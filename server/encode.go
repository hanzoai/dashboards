package server

import (
	"strconv"

	"github.com/hanzoai/base/core"

	gen "github.com/hanzoai/dashboards/gen"
)

// encode.go is the single place a Base record is projected onto its generated
// wire struct. One encoder per model; handlers call these so the record→view
// mapping lives in exactly one location (no per-handler field plucking).
//
// JSON columns (definition, filters, dimensions, …) are emitted verbatim as the
// text they were stored as — the UI parses them, the transport never does. The
// created/updated columns are emitted as their stored autodate strings.

func encodeDashboard(r *core.Record) []byte {
	return gen.NewDashboard(gen.DashboardInput{
		Id:          r.Id,
		ProjectId:   r.GetString(fProjectId),
		Name:        r.GetString(fName),
		Description: r.GetString(fDashDescription),
		Owner:       r.GetString(fDashOwner),
		Definition:  r.GetString(fDashDefinition),
		Filters:     r.GetString(fDashFilters),
		CreatedBy:   r.GetString(fCreatedBy),
		CreatedAt:   r.GetString("created"),
		UpdatedAt:   r.GetString("updated"),
	})
}

func encodeWidget(r *core.Record) []byte {
	return gen.NewWidget(gen.WidgetInput{
		Id:          r.Id,
		ProjectId:   r.GetString(fProjectId),
		Name:        r.GetString(fName),
		Description: r.GetString(fWidgetDescription),
		View:        r.GetString(fWidgetView),
		Owner:       r.GetString(fWidgetOwner),
		Dimensions:  r.GetString(fWidgetDimensions),
		Metrics:     r.GetString(fWidgetMetrics),
		Filters:     r.GetString(fWidgetFilters),
		ChartType:   r.GetString(fWidgetChartType),
		ChartConfig: r.GetString(fWidgetChartConfig),
		CreatedAt:   r.GetString("created"),
		UpdatedAt:   r.GetString("updated"),
	})
}

func encodePreset(r *core.Record) []byte {
	return gen.NewPreset(gen.PresetInput{
		Id:               r.Id,
		ProjectId:        r.GetString(fProjectId),
		Name:             r.GetString(fName),
		TableName:        r.GetString(fPresetTableName),
		Filters:          r.GetString(fPresetFilters),
		ColumnOrder:      r.GetString(fPresetColumnOrder),
		ColumnVisibility: r.GetString(fPresetColumnVisibility),
		SearchQuery:      r.GetString(fPresetSearchQuery),
		OrderByColumn:    r.GetString(fPresetOrderByColumn),
		OrderByOrder:     r.GetString(fPresetOrderByOrder),
		CreatedBy:        r.GetString(fCreatedBy),
		CreatedAt:        r.GetString("created"),
		UpdatedAt:        r.GetString("updated"),
	})
}

func encodeMonitor(r *core.Record) []byte {
	return gen.NewMonitor(gen.MonitorInput{
		Id:                r.Id,
		ProjectId:         r.GetString(fProjectId),
		Name:              r.GetString(fName),
		View:              r.GetString(fMonitorView),
		Filters:           r.GetString(fMonitorFilters),
		Metric:            r.GetString(fMonitorMetric),
		Window:            r.GetString(fMonitorWindow),
		ThresholdOperator: r.GetString(fMonitorThresholdOperator),
		AlertThreshold:    r.GetFloat(fMonitorAlertThreshold),
		WarningThreshold:  nullableTextToFloat(r.GetString(fMonitorWarningThreshold)),
		NoData:            r.GetString(fMonitorNoData),
		Renotify:          r.GetString(fMonitorRenotify),
		Tags:              r.GetString(fMonitorTags),
		Status:            r.GetString(fMonitorStatus),
		Severity:          r.GetString(fMonitorSeverity),
		SeverityChangedAt: r.GetString(fMonitorSeverityChangedAt),
		AlertedAt:         r.GetString(fMonitorAlertedAt),
		NextRunAt:         r.GetString(fMonitorNextRunAt),
		CreatedBy:         r.GetString(fCreatedBy),
		CreatedAt:         r.GetString("created"),
		UpdatedAt:         r.GetString("updated"),
	})
}

// nullableTextToFloat decodes the warningThreshold storage column: "" → the NaN
// null sentinel, else the parsed decimal. Inverse of floatToNullableText.
func nullableTextToFloat(s string) float64 {
	if s == "" {
		return nullThreshold()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nullThreshold()
	}
	return f
}
