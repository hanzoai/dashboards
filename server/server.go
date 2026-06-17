package server

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"
	luxlog "github.com/luxfi/log"
	zaplib "github.com/luxfi/zap"
	zcap "github.com/zap-proto/go/cap"
)

// Server implements the Dashboards ZAP capability-RPC interface on top of a Base
// app. It is the Go peer of console's five tRPC routers: one method per
// procedure, each gated on the caller's capability via the single chokepoint
// authorize() (Kind == CapKindIAMSession AND the method's DashPermissions bit),
// then reading/writing the project-scoped Base collection.
type Server struct {
	app    core.App
	logger luxlog.Logger

	// verifier validates capability buffers. Wired to ed25519 (the bootstrap
	// scheme); a PQ deployment swaps in an ML-DSA-65 SchemeVerify + the IAM
	// pubkey registry for IssuerKey. With no registry (bootstrap/tests) the
	// signature step is skipped but Kind + Permissions are STILL enforced.
	verifier zcap.Verifier

	// promises is the server-side pipelining table: a call may carry PromiseID,
	// and a later call may Target it. Promises are FUTURES — a dependent call
	// that arrives before its target resolves WAITS on the target (it is not
	// rejected), then dispatches against the resolved answer. Cap'n Proto promise
	// pipelining: the dependent call is admitted before the target's answer
	// exists, eliding the round trip. The resolved value is the authorized
	// project scope (e.g. a dashboard→widget chain shares one project). Entries
	// are short-lived (one connection turn).
	mu       sync.Mutex
	promises map[uint32]*promiseSlot
}

// promiseSlot is a future for a pipelined call's answer. done is closed when the
// slot resolves; project is then readable. resolvedAt drives reaping.
type promiseSlot struct {
	done       chan struct{}
	project    string
	resolvedAt time.Time
}

// promiseWaitTimeout bounds how long a dependent call waits for its target to
// resolve before failing. Generous relative to a same-connection turn.
const promiseWaitTimeout = 5 * time.Second

// getOrCreate returns the slot for id, creating an unresolved one if absent.
func (s *Server) getOrCreate(id uint32) *promiseSlot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	slot, ok := s.promises[id]
	if !ok {
		slot = &promiseSlot{done: make(chan struct{})}
		s.promises[id] = slot
	}
	return slot
}

// reapLocked drops promise slots that resolved more than promiseWaitTimeout ago.
// Caller must hold s.mu.
func (s *Server) reapLocked() {
	cutoff := time.Now().Add(-promiseWaitTimeout)
	for id, slot := range s.promises {
		if !slot.resolvedAt.IsZero() && slot.resolvedAt.Before(cutoff) {
			delete(s.promises, id)
		}
	}
}

// resolve fills a promise slot with its answer and wakes any waiters. Safe once
// per slot; a double-resolve is guarded by the done channel.
func (s *Server) resolve(id uint32, project string) {
	slot := s.getOrCreate(id)
	s.mu.Lock()
	select {
	case <-slot.done:
		// already resolved — leave as-is
	default:
		slot.project = project
		slot.resolvedAt = time.Now()
		close(slot.done)
	}
	s.mu.Unlock()
}

// await blocks until the target promise resolves or the timeout elapses,
// returning the resolved project scope.
func (s *Server) await(target uint32) (string, bool) {
	slot := s.getOrCreate(target)
	select {
	case <-slot.done:
		s.mu.Lock()
		project := slot.project
		s.mu.Unlock()
		return project, true
	case <-time.After(promiseWaitTimeout):
		return "", false
	}
}

// NewServer builds a Dashboards server. verifier supplies the capability trust
// anchor; pass a Verifier whose IssuerKey resolves your IAM issuer key.
func NewServer(app core.App, logger luxlog.Logger, verifier zcap.Verifier) *Server {
	return &Server{
		app:      app,
		logger:   logger,
		verifier: verifier,
		promises: make(map[uint32]*promiseSlot),
	}
}

// Register wires the server's handler onto a luxfi/zap node at this service's
// message-type slot. Called from main once the node is constructed.
func (s *Server) Register(node *zaplib.Node) {
	node.Handle(MsgTypeRouterBase, s.handle)
}

// handle is the ZAP dispatch entrypoint: decode → authorize (kind+perm) →
// resolve project scope → route. The capability gate is the SINGLE chokepoint;
// every handler runs only after the bit for its method is present.
func (s *Server) handle(ctx context.Context, from string, msg *zaplib.Message) (*zaplib.Message, error) {
	_ = ctx
	req := parseRequest(msg)

	if status, errMsg := s.authorize(req); status != StatusOK {
		s.logger.Debug("dashboards: auth rejected", "from", from, "method", req.Method, "status", status, "err", errMsg)
		return buildResponse(status, req.PromiseID, errorBody(errMsg))
	}

	// Effective project scope: inherited from a targeted promise if this call
	// pipelines, else read from the call's own request payload. The cap gate has
	// already passed; scope binds WHICH project's rows the handler may touch.
	project, status, errMsg := s.scope(req)
	if status != StatusOK {
		return buildResponse(status, req.PromiseID, errorBody(errMsg))
	}

	// Resolve this call's promise (its project scope) so any dependent call
	// WAITING on it can proceed.
	if req.PromiseID != NoTarget {
		s.resolve(req.PromiseID, project)
	}

	return s.dispatch(req, project)
}

// authorize enforces the capability: it must be a CapKindIAMSession cap carrying
// the DashPermissions bit the requested method requires. Returns StatusOK on
// success. Pipelining elides round trips, NEVER authorization — the bit is
// checked on every call.
func (s *Server) authorize(req Call) (status uint32, errMsg string) {
	need, ok := permissionFor(req.Method)
	if !ok {
		return StatusBadRequest, fmt.Sprintf("unknown method %d", req.Method)
	}

	c, err := zcap.Wrap(req.Cap)
	if err != nil {
		return StatusBadRequest, "malformed capability: " + err.Error()
	}
	if c.Kind() != uint32(zcap.KindIAMSession) {
		return StatusForbidden, "capability is not a CapKindIAMSession"
	}
	if c.Permissions()&need == 0 {
		return StatusForbidden, fmt.Sprintf("capability lacks required permission 0x%x", need)
	}

	// Cryptographic verification runs whenever an issuer registry is wired; with
	// no registry (bootstrap/tests) the signature step is skipped but Kind +
	// Permissions above are STILL enforced.
	//
	// TODO(IAM): walk the parent chain with verifier.VerifyChain and bind the cap
	// to the live session via the out-of-band holderSig over a server nonce once
	// the IAM pubkey registry is wired here.
	if s.verifier.IssuerKey != nil {
		if err := s.verifier.Verify(c, time.Now().Unix()); err != nil {
			return StatusUnauthorized, "capability verify failed: " + err.Error()
		}
	}
	return StatusOK, ""
}

// scope resolves the project this call operates within. If the call pipelines
// off an earlier promise it inherits that promise's resolved project; otherwise
// it reads projectId from the request payload (every request struct carries it
// as text @0). An empty projectId is rejected — the tenant key is mandatory.
func (s *Server) scope(req Call) (project string, status uint32, errMsg string) {
	if req.Target != NoTarget {
		p, ok := s.await(req.Target)
		if !ok {
			return "", StatusBadRequest, fmt.Sprintf("pipelined target %d did not resolve in time", req.Target)
		}
		return p, StatusOK, ""
	}
	if len(req.Payload) == 0 {
		return "", StatusBadRequest, "missing request payload"
	}
	p, err := zaplib.Parse(req.Payload)
	if err != nil {
		return "", StatusBadRequest, "malformed payload: " + err.Error()
	}
	project = p.Root().Text(0) // ProjectId is text @0 in every request struct
	if project == "" {
		return "", StatusBadRequest, "missing projectId"
	}
	return project, StatusOK, ""
}

// dispatch routes an authorized, project-scoped call to its handler. Adding a
// method means adding one case here + one row in permissionFor — nowhere else.
func (s *Server) dispatch(req Call, project string) (*zaplib.Message, error) {
	switch req.Method {
	// dashboardRouter — CRUD
	case MethodAllDashboards:
		return s.handleAllDashboards(req, project)
	case MethodGetDashboard:
		return s.handleGetDashboard(req, project)
	case MethodCreateDashboard:
		return s.handleCreateDashboard(req, project)
	case MethodUpdateDashboardMeta:
		return s.handleUpdateDashboardMeta(req, project)
	case MethodUpdateDashboardDef:
		return s.handleUpdateDashboardDef(req, project)
	case MethodUpdateDashboardFilters:
		return s.handleUpdateDashboardFilters(req, project)
	case MethodCloneDashboard:
		return s.handleCloneDashboard(req, project)
	case MethodDeleteDashboard:
		return s.handleDeleteDashboard(req, project)
	// dashboardRouter — analytics (ClickHouse)
	case MethodChart, MethodScoreHistogram, MethodExecuteQuery:
		return s.handleAnalytics(req)
	// dashboardWidgetRouter
	case MethodAllWidgets:
		return s.handleAllWidgets(req, project)
	case MethodGetWidget:
		return s.handleGetWidget(req, project)
	case MethodCreateWidget:
		return s.handleUpsertWidget(req, project, false)
	case MethodUpdateWidget:
		return s.handleUpsertWidget(req, project, true)
	case MethodCopyWidgetToProject:
		return s.handleCopyWidget(req, project)
	case MethodDeleteWidget:
		return s.handleDeleteWidget(req, project)
	// tableRouter (BullMQ queue)
	case MethodIsBatchActionInProgress:
		return s.handleIsBatchActionInProgress(req)
	// TableViewPresetsRouter
	case MethodGetPresetsByTableName:
		return s.handleGetPresetsByTableName(req, project)
	case MethodGetPresetById:
		return s.handleGetPresetById(req, project)
	case MethodCreatePreset:
		return s.handleUpsertPreset(req, project, false)
	case MethodUpdatePreset:
		return s.handleUpsertPreset(req, project, true)
	case MethodUpdatePresetName:
		return s.handleUpdatePresetName(req, project)
	case MethodDeletePreset:
		return s.handleDeletePreset(req, project)
	case MethodGeneratePermalink:
		return s.handleGeneratePermalink(req, project)
	// monitorsRouter
	case MethodAllMonitors:
		return s.handleAllMonitors(req, project)
	case MethodGetMonitor:
		return s.handleGetMonitor(req, project)
	case MethodCreateMonitor:
		return s.handleUpsertMonitor(req, project, false)
	case MethodUpdateMonitor:
		return s.handleUpsertMonitor(req, project, true)
	case MethodDeleteMonitor:
		return s.handleDeleteMonitor(req, project)
	default:
		return s.fail(req, StatusBadRequest, fmt.Sprintf("unknown method %d", req.Method))
	}
}

// ── shared response + record helpers ────────────────────────────────────────

// ok encodes a 200 response with the given body. fail encodes an error response.
func (s *Server) ok(req Call, body []byte) (*zaplib.Message, error) {
	return buildResponse(StatusOK, req.PromiseID, body)
}
func (s *Server) fail(req Call, status uint32, msg string) (*zaplib.Message, error) {
	return buildResponse(status, req.PromiseID, errorBody(msg))
}

// findScoped finds one row by id WITHIN the project — the tenant guard. A row in
// a different project is reported NotFound, never returned (no cross-tenant read).
func (s *Server) findScoped(collection, project, id string) (*core.Record, error) {
	return s.app.FindFirstRecordByFilter(collection,
		"id = {:id} && projectId = {:p}", dbx.Params{"id": id, "p": project})
}

// listScoped returns project-scoped rows with paging + sort, plus the unpaged
// total. page is 1-based (matches paginationZod); limit<=0 means "all".
func (s *Server) listScoped(collection, project, sort string, page, limit int) ([]*core.Record, int, error) {
	all, err := s.app.FindRecordsByFilter(collection,
		"projectId = {:p}", "", 0, 0, dbx.Params{"p": project})
	if err != nil {
		return nil, 0, err
	}
	total := len(all)
	rows, err := s.app.FindRecordsByFilter(collection,
		"projectId = {:p}", sort, limit, offsetOf(page, limit), dbx.Params{"p": project})
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// offsetOf converts a 1-based page + limit into a row offset.
func offsetOf(page, limit int) int {
	if page <= 1 || limit <= 0 {
		return 0
	}
	return (page - 1) * limit
}

// sortExpr maps a (column, "ASC"/"DESC") order pair into a Base sort string,
// defaulting to newest-first when no column is given (matches the tRPC default).
func sortExpr(column, order string) string {
	if column == "" {
		return "-created"
	}
	if order == "ASC" {
		return "+" + column
	}
	return "-" + column
}

// nullThreshold is the wire sentinel (NaN) for a null Monitor warningThreshold.
func nullThreshold() float64 { return math.NaN() }

// isNull reports whether a float carries the null sentinel.
func isNull(f float64) bool { return math.IsNaN(f) }
