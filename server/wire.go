// Package server implements the Dashboards ZAP capability-RPC service.
//
// Wire model — three concentric, decoupled layers (values, not places):
//
//  1. Transport: github.com/luxfi/zap Node. Frames are length-prefixed,
//     request/response-correlated, and routed by msgType = flags>>8. This
//     service owns MsgTypeRouterBase (206), disjoint from Base's generic ORM
//     plugin (100–103) and the sibling typed routers (200=ui-customization …).
//     One TCP listener, port 9995.
//
//  2. Envelope: a luxfi/zap object carrying the call shape. Request =
//     (Method u32, PromiseID u32, Target u32, Cap bytes, Payload bytes);
//     response = (Status u32, PromiseID u32, Body bytes). The Cap field is the
//     OPAQUE zap-proto/go capability buffer — re-Wrapped server-side, never
//     decoded by the transport.
//
//  3. Payload/Body: zap-proto/go typed views generated from the .zap schema
//     (gen/). These are the contract both Go and the console's TS client agree
//     on. The transport carries them as opaque bytes — the contract (data) is
//     fully separate from the transport (place).
//
// Pipelining: PromiseID + Target let a caller reference the not-yet-resolved
// answer of an earlier call. A second call shipped with Target = the first
// call's PromiseID is dispatched against that promise's resolved value —
// Cap'n Proto-style promise pipelining (see client.go Pipeline()).
package server

import (
	"fmt"

	zaplib "github.com/luxfi/zap"
)

// MsgTypeRouterBase is this service's ZAP message-type slot. Base's generic ORM
// transport plugin uses 100–103; typed capability routers start at 200, one per
// service binary — this one (Dashboards) is 206.
const MsgTypeRouterBase uint16 = 206

// Method identifiers — one per .zap interface method (the `@n` ordinals). The
// surface is the union of the five tRPC routers this binary replaces; grouped by
// router for readability, dispatched in server.go.
const (
	// dashboardRouter — CRUD.
	MethodAllDashboards          uint32 = 0
	MethodGetDashboard           uint32 = 1
	MethodCreateDashboard        uint32 = 2
	MethodUpdateDashboardMeta    uint32 = 3
	MethodUpdateDashboardDef     uint32 = 4
	MethodUpdateDashboardFilters uint32 = 5
	MethodCloneDashboard         uint32 = 6
	MethodDeleteDashboard        uint32 = 7
	// dashboardRouter — analytics (ClickHouse; see server.go).
	MethodChart          uint32 = 8
	MethodScoreHistogram uint32 = 9
	MethodExecuteQuery   uint32 = 10
	// dashboardWidgetRouter.
	MethodAllWidgets          uint32 = 11
	MethodGetWidget           uint32 = 12
	MethodCreateWidget        uint32 = 13
	MethodUpdateWidget        uint32 = 14
	MethodCopyWidgetToProject uint32 = 15
	MethodDeleteWidget        uint32 = 16
	// tableRouter (BullMQ queue; see server.go).
	MethodIsBatchActionInProgress uint32 = 17
	// TableViewPresetsRouter.
	MethodGetPresetsByTableName uint32 = 18
	MethodGetPresetById         uint32 = 19
	MethodCreatePreset          uint32 = 20
	MethodUpdatePreset          uint32 = 21
	MethodUpdatePresetName      uint32 = 22
	MethodDeletePreset          uint32 = 23
	MethodGeneratePermalink     uint32 = 24
	// monitorsRouter.
	MethodAllMonitors   uint32 = 25
	MethodGetMonitor    uint32 = 26
	MethodCreateMonitor uint32 = 27
	MethodUpdateMonitor uint32 = 28
	MethodDeleteMonitor uint32 = 29
)

// DashPermissions is the u64 capability bitmask this service enforces. It is the
// orthogonal product of the five resource categories × {Read, Write}, plus a
// dedicated analytics-read bit. Each bit maps 1:1 to a console RBAC scope so the
// gate here mirrors throwIfNoProjectAccess there exactly:
//
//	PermDashboardRead   ⇄ dashboards:read        PermDashboardWrite  ⇄ dashboards:CUD
//	PermAnalyticsRead   ⇄ dashboards:read (analytics subset)
//	PermPresetRead      ⇄ TableViewPresets:read  PermPresetWrite     ⇄ TableViewPresets:CUD
//	PermMonitorRead     ⇄ monitors:read          PermMonitorWrite    ⇄ monitors:CUD
//
// Widget + table-batch-action operations live under the Dashboard category
// (console scopes them as dashboards:read / dashboards:CUD), so they reuse the
// Dashboard bits — one category, no redundant bit.
const (
	PermDashboardRead  uint64 = 1 << 0
	PermDashboardWrite uint64 = 1 << 1
	PermAnalyticsRead  uint64 = 1 << 2
	PermPresetRead     uint64 = 1 << 3
	PermPresetWrite    uint64 = 1 << 4
	PermMonitorRead    uint64 = 1 << 5
	PermMonitorWrite   uint64 = 1 << 6
)

// permissionFor returns the single DashPermissions bit a method gates on — the
// one place method→permission policy is declared. The dispatcher reads it once
// at the top of handle(), so adding a method means adding one row here and one
// case in server.go, nowhere else.
func permissionFor(method uint32) (uint64, bool) {
	switch method {
	case MethodAllDashboards, MethodGetDashboard, MethodAllWidgets, MethodGetWidget,
		MethodIsBatchActionInProgress:
		return PermDashboardRead, true
	case MethodCreateDashboard, MethodUpdateDashboardMeta, MethodUpdateDashboardDef,
		MethodUpdateDashboardFilters, MethodCloneDashboard, MethodDeleteDashboard,
		MethodCreateWidget, MethodUpdateWidget, MethodCopyWidgetToProject, MethodDeleteWidget:
		return PermDashboardWrite, true
	case MethodChart, MethodScoreHistogram, MethodExecuteQuery:
		return PermAnalyticsRead, true
	case MethodGetPresetsByTableName, MethodGetPresetById, MethodGeneratePermalink:
		return PermPresetRead, true
	case MethodCreatePreset, MethodUpdatePreset, MethodUpdatePresetName, MethodDeletePreset:
		return PermPresetWrite, true
	case MethodAllMonitors, MethodGetMonitor:
		return PermMonitorRead, true
	case MethodCreateMonitor, MethodUpdateMonitor, MethodDeleteMonitor:
		return PermMonitorWrite, true
	default:
		return 0, false
	}
}

// NoTarget is the Target value for a call that does not pipeline off an earlier
// promise (i.e. it acts on the root capability directly).
const NoTarget uint32 = 0

// Request-frame field offsets within the envelope object's fixed section.
const (
	reqMethodOff    = 0  // u32: which interface method
	reqPromiseIDOff = 4  // u32: caller-assigned id this call's answer resolves to
	reqTargetOff    = 8  // u32: promise this call pipelines off (NoTarget = root)
	reqCapOff       = 12 // bytes: opaque zap-proto/go capability buffer
	reqPayloadOff   = 20 // bytes: zap-proto/go-encoded method params
	reqFixedSize    = 28
)

// Response-frame field offsets.
const (
	respStatusOff    = 0  // u32: 200 ok, else error
	respPromiseIDOff = 4  // u32: echoes the request's PromiseID
	respBodyOff      = 12 // bytes: zap-proto/go-encoded results (or error JSON)
	respFixedSize    = 20
)

// Status codes mirror the HTTP-ish convention Base's ZAP plugin already uses.
const (
	StatusOK           uint32 = 200
	StatusBadRequest   uint32 = 400
	StatusUnauthorized uint32 = 401
	StatusForbidden    uint32 = 403
	StatusNotFound     uint32 = 404
	StatusConflict     uint32 = 409
	StatusInternal     uint32 = 500
	StatusNotImpl      uint32 = 501
)

// Call is the decoded request envelope.
type Call struct {
	Method    uint32
	PromiseID uint32
	Target    uint32
	Cap       []byte // opaque capability buffer
	Payload   []byte // opaque zap-proto/go params
}

// buildRequest encodes a Call into a luxfi/zap message tagged for this router.
func buildRequest(c Call) (*zaplib.Message, error) {
	b := zaplib.NewBuilder(len(c.Cap) + len(c.Payload) + reqFixedSize + 64)
	ob := b.StartObject(reqFixedSize)
	ob.SetUint32(reqMethodOff, c.Method)
	ob.SetUint32(reqPromiseIDOff, c.PromiseID)
	ob.SetUint32(reqTargetOff, c.Target)
	ob.SetBytes(reqCapOff, c.Cap)
	ob.SetBytes(reqPayloadOff, c.Payload)
	ob.FinishAsRoot()
	data := b.FinishWithFlags(MsgTypeRouterBase << 8)
	return zaplib.Parse(data)
}

// parseRequest decodes a luxfi/zap message into a Call.
func parseRequest(msg *zaplib.Message) Call {
	root := msg.Root()
	return Call{
		Method:    root.Uint32(reqMethodOff),
		PromiseID: root.Uint32(reqPromiseIDOff),
		Target:    root.Uint32(reqTargetOff),
		Cap:       root.Bytes(reqCapOff),
		Payload:   root.Bytes(reqPayloadOff),
	}
}

// buildResponse encodes a status + body into a router-tagged luxfi/zap message.
func buildResponse(status, promiseID uint32, body []byte) (*zaplib.Message, error) {
	b := zaplib.NewBuilder(len(body) + respFixedSize + 64)
	ob := b.StartObject(respFixedSize)
	ob.SetUint32(respStatusOff, status)
	ob.SetUint32(respPromiseIDOff, promiseID)
	ob.SetBytes(respBodyOff, body)
	ob.FinishAsRoot()
	data := b.FinishWithFlags(MsgTypeRouterBase << 8)
	return zaplib.Parse(data)
}

// Response is the decoded response envelope.
type Response struct {
	Status    uint32
	PromiseID uint32
	Body      []byte
}

// parseResponse decodes a luxfi/zap response message.
func parseResponse(msg *zaplib.Message) Response {
	root := msg.Root()
	return Response{
		Status:    root.Uint32(respStatusOff),
		PromiseID: root.Uint32(respPromiseIDOff),
		Body:      root.Bytes(respBodyOff),
	}
}

// errorBody is a minimal JSON error body, matching the shape Base's plugin
// returns ({"error": "..."}) so a single client error path covers both.
func errorBody(msg string) []byte {
	return []byte(fmt.Sprintf(`{"error":%q}`, msg))
}
