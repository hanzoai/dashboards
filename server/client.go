package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	zaplib "github.com/luxfi/zap"
	zcap "github.com/zap-proto/go/cap"

	gen "github.com/hanzoai/dashboards/gen"
)

// Client is a Dashboards ZAP capability-RPC client. It is what console's bridge
// substitutes for the in-process tRPC routers: a thin typed wrapper over a
// luxfi/zap connection that ships the verified capability with every call.
//
// Holds the caller's opaque capability buffer and the transport node. Construct
// with Dial, then call the typed methods (CreateDashboard, AllWidgets, …) or
// Pipeline for a dashboard→widget chain.
type Client struct {
	node   *zaplib.Node
	peerID string
	capBuf []byte

	promiseSeq uint32 // monotonic PromiseID allocator

	// sendLog records, in order, every call shipped — used by the smoke test to
	// prove a pipelined dependent call ships before the first answer resolves.
	logMu   sync.Mutex
	sendLog *[]SendEvent
}

// SendEvent is one entry in the instrumentation log: a call left the client
// (Sent) or its answer arrived (Recv), with a monotonic sequence number.
type SendEvent struct {
	Seq       uint64
	Kind      string // "send" or "recv"
	Method    uint32
	PromiseID uint32
	Target    uint32
	At        time.Time
}

var sendEventSeq uint64

// pipelineIDSeq hands out process-unique promise ids for pipelined call groups.
// Starts high (above any per-client counter) so a pipeline id never collides
// with a plain per-call PromiseID — promise ids are a cross-connection
// correlation namespace, so global uniqueness is required.
var pipelineIDSeq uint32 = 1 << 20

func nextPipelineID() uint32 { return atomic.AddUint32(&pipelineIDSeq, 1) }

// Dial constructs a Client over an already-started local node, connecting to the
// service at addr (e.g. "127.0.0.1:9995"). capBuf is the caller's opaque
// capability buffer (a zcap.Cap.Bytes()). peerID is the service's ZAP node id.
func Dial(node *zaplib.Node, addr, peerID string, capBuf []byte) (*Client, error) {
	if err := node.ConnectDirect(addr); err != nil {
		return nil, fmt.Errorf("dashboards client: connect %s: %w", addr, err)
	}
	return &Client{node: node, peerID: peerID, capBuf: capBuf}, nil
}

// WithSendLog attaches an instrumentation slice the client appends send/recv
// events to. Returns the client for chaining.
func (c *Client) WithSendLog(log *[]SendEvent) *Client {
	c.sendLog = log
	return c
}

func (c *Client) record(kind string, method, promiseID, target uint32) {
	if c.sendLog == nil {
		return
	}
	c.logMu.Lock()
	*c.sendLog = append(*c.sendLog, SendEvent{
		Seq:       atomic.AddUint64(&sendEventSeq, 1),
		Kind:      kind,
		Method:    method,
		PromiseID: promiseID,
		Target:    target,
		At:        time.Now(),
	})
	c.logMu.Unlock()
}

func (c *Client) nextPromise() uint32 { return atomic.AddUint32(&c.promiseSeq, 1) }

// call ships one request (method + payload) and blocks for its correlated
// response. promiseID/target drive pipelining; target=NoTarget for a root call.
func (c *Client) call(ctx context.Context, method, promiseID, target uint32, payload []byte) (Response, error) {
	msg, err := buildRequest(Call{
		Method:    method,
		PromiseID: promiseID,
		Target:    target,
		Cap:       c.capBuf,
		Payload:   payload,
	})
	if err != nil {
		return Response{}, err
	}
	c.record("send", method, promiseID, target)
	resp, err := c.node.Call(ctx, c.peerID, msg)
	if err != nil {
		return Response{}, err
	}
	c.record("recv", method, promiseID, target)
	return parseResponse(resp), nil
}

// invoke is the root-call convenience: ship method+payload with a fresh promise
// id and no pipelining target, returning the response body on 200 (else error).
func (c *Client) invoke(ctx context.Context, method uint32, payload []byte) ([]byte, error) {
	resp, err := c.call(ctx, method, c.nextPromise(), NoTarget, payload)
	if err != nil {
		return nil, err
	}
	if resp.Status != StatusOK {
		return nil, fmt.Errorf("method %d: status %d: %s", method, resp.Status, resp.Body)
	}
	return resp.Body, nil
}

// Probe ships method+payload as a root call and returns ONLY the response status
// code (discarding the body). It is the low-level escape hatch the
// not-implemented contract tests use to assert a method returns 501 without
// caring about a typed body. Transport errors surface as 0.
func (c *Client) Probe(ctx context.Context, method uint32, payload []byte) uint32 {
	resp, err := c.call(ctx, method, c.nextPromise(), NoTarget, payload)
	if err != nil {
		return 0
	}
	return resp.Status
}

// ── Dashboard CRUD ──────────────────────────────────────────────────────────

func (c *Client) AllDashboards(ctx context.Context, in gen.ListReqInput) (gen.DashboardList, error) {
	body, err := c.invoke(ctx, MethodAllDashboards, gen.NewListReq(in))
	if err != nil {
		return gen.DashboardList{}, err
	}
	return gen.WrapDashboardList(body)
}

func (c *Client) GetDashboard(ctx context.Context, in gen.IdReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodGetDashboard, gen.NewIdReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) CreateDashboard(ctx context.Context, in gen.CreateDashReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodCreateDashboard, gen.NewCreateDashReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) UpdateDashboardMetadata(ctx context.Context, in gen.UpdateDashReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodUpdateDashboardMeta, gen.NewUpdateDashReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) UpdateDashboardDefinition(ctx context.Context, in gen.DashDefReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodUpdateDashboardDef, gen.NewDashDefReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) UpdateDashboardFilters(ctx context.Context, in gen.DashFiltersReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodUpdateDashboardFilters, gen.NewDashFiltersReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) CloneDashboard(ctx context.Context, in gen.IdReqInput) (gen.Dashboard, error) {
	body, err := c.invoke(ctx, MethodCloneDashboard, gen.NewIdReq(in))
	if err != nil {
		return gen.Dashboard{}, err
	}
	return gen.WrapDashboard(body)
}

func (c *Client) DeleteDashboard(ctx context.Context, in gen.IdReqInput) (gen.Mutation, error) {
	body, err := c.invoke(ctx, MethodDeleteDashboard, gen.NewIdReq(in))
	if err != nil {
		return gen.Mutation{}, err
	}
	return gen.WrapMutation(body)
}

// ── Widget CRUD ─────────────────────────────────────────────────────────────

func (c *Client) AllWidgets(ctx context.Context, in gen.ListReqInput) (gen.WidgetList, error) {
	body, err := c.invoke(ctx, MethodAllWidgets, gen.NewListReq(in))
	if err != nil {
		return gen.WidgetList{}, err
	}
	return gen.WrapWidgetList(body)
}

func (c *Client) GetWidget(ctx context.Context, in gen.IdReqInput) (gen.Widget, error) {
	body, err := c.invoke(ctx, MethodGetWidget, gen.NewIdReq(in))
	if err != nil {
		return gen.Widget{}, err
	}
	return gen.WrapWidget(body)
}

func (c *Client) CreateWidget(ctx context.Context, in gen.WidgetReqInput) (gen.Widget, error) {
	body, err := c.invoke(ctx, MethodCreateWidget, gen.NewWidgetReq(in))
	if err != nil {
		return gen.Widget{}, err
	}
	return gen.WrapWidget(body)
}

func (c *Client) UpdateWidget(ctx context.Context, in gen.WidgetReqInput) (gen.Widget, error) {
	body, err := c.invoke(ctx, MethodUpdateWidget, gen.NewWidgetReq(in))
	if err != nil {
		return gen.Widget{}, err
	}
	return gen.WrapWidget(body)
}

func (c *Client) CopyWidgetToProject(ctx context.Context, in gen.CopyWidgetReqInput) (gen.Mutation, error) {
	body, err := c.invoke(ctx, MethodCopyWidgetToProject, gen.NewCopyWidgetReq(in))
	if err != nil {
		return gen.Mutation{}, err
	}
	return gen.WrapMutation(body)
}

func (c *Client) DeleteWidget(ctx context.Context, in gen.IdReqInput) (gen.Mutation, error) {
	body, err := c.invoke(ctx, MethodDeleteWidget, gen.NewIdReq(in))
	if err != nil {
		return gen.Mutation{}, err
	}
	return gen.WrapMutation(body)
}

// ── TableViewPreset CRUD ────────────────────────────────────────────────────

func (c *Client) GetPresetsByTableName(ctx context.Context, in gen.PresetListReqInput) (gen.PresetList, error) {
	body, err := c.invoke(ctx, MethodGetPresetsByTableName, gen.NewPresetListReq(in))
	if err != nil {
		return gen.PresetList{}, err
	}
	return gen.WrapPresetList(body)
}

func (c *Client) GetPresetById(ctx context.Context, in gen.IdReqInput) (gen.Preset, error) {
	body, err := c.invoke(ctx, MethodGetPresetById, gen.NewIdReq(in))
	if err != nil {
		return gen.Preset{}, err
	}
	return gen.WrapPreset(body)
}

func (c *Client) CreatePreset(ctx context.Context, in gen.PresetReqInput) (gen.Preset, error) {
	body, err := c.invoke(ctx, MethodCreatePreset, gen.NewPresetReq(in))
	if err != nil {
		return gen.Preset{}, err
	}
	return gen.WrapPreset(body)
}

func (c *Client) UpdatePreset(ctx context.Context, in gen.PresetReqInput) (gen.Preset, error) {
	body, err := c.invoke(ctx, MethodUpdatePreset, gen.NewPresetReq(in))
	if err != nil {
		return gen.Preset{}, err
	}
	return gen.WrapPreset(body)
}

func (c *Client) UpdatePresetName(ctx context.Context, in gen.PresetNameReqInput) (gen.Preset, error) {
	body, err := c.invoke(ctx, MethodUpdatePresetName, gen.NewPresetNameReq(in))
	if err != nil {
		return gen.Preset{}, err
	}
	return gen.WrapPreset(body)
}

func (c *Client) DeletePreset(ctx context.Context, in gen.IdReqInput) (gen.Mutation, error) {
	body, err := c.invoke(ctx, MethodDeletePreset, gen.NewIdReq(in))
	if err != nil {
		return gen.Mutation{}, err
	}
	return gen.WrapMutation(body)
}

func (c *Client) GeneratePermalink(ctx context.Context, in gen.PermalinkReqInput) (gen.StringResult, error) {
	body, err := c.invoke(ctx, MethodGeneratePermalink, gen.NewPermalinkReq(in))
	if err != nil {
		return gen.StringResult{}, err
	}
	return gen.WrapStringResult(body)
}

// ── Monitor CRUD ────────────────────────────────────────────────────────────

func (c *Client) AllMonitors(ctx context.Context, in gen.ListReqInput) (gen.MonitorList, error) {
	body, err := c.invoke(ctx, MethodAllMonitors, gen.NewListReq(in))
	if err != nil {
		return gen.MonitorList{}, err
	}
	return gen.WrapMonitorList(body)
}

func (c *Client) GetMonitor(ctx context.Context, in gen.IdReqInput) (gen.Monitor, error) {
	body, err := c.invoke(ctx, MethodGetMonitor, gen.NewIdReq(in))
	if err != nil {
		return gen.Monitor{}, err
	}
	return gen.WrapMonitor(body)
}

func (c *Client) CreateMonitor(ctx context.Context, in gen.MonitorReqInput) (gen.Monitor, error) {
	body, err := c.invoke(ctx, MethodCreateMonitor, gen.NewMonitorReq(in))
	if err != nil {
		return gen.Monitor{}, err
	}
	return gen.WrapMonitor(body)
}

func (c *Client) UpdateMonitor(ctx context.Context, in gen.MonitorReqInput) (gen.Monitor, error) {
	body, err := c.invoke(ctx, MethodUpdateMonitor, gen.NewMonitorReq(in))
	if err != nil {
		return gen.Monitor{}, err
	}
	return gen.WrapMonitor(body)
}

func (c *Client) DeleteMonitor(ctx context.Context, in gen.IdReqInput) (gen.Mutation, error) {
	body, err := c.invoke(ctx, MethodDeleteMonitor, gen.NewIdReq(in))
	if err != nil {
		return gen.Mutation{}, err
	}
	return gen.WrapMutation(body)
}

// ── Pipelining: dashboard → widget chain ────────────────────────────────────

// PipelineCreateDashboardThenWidget creates a dashboard and, pipelined off that
// call's promise, creates a widget in the SAME project — Cap'n Proto promise
// pipelining. The widget call Targets the dashboard call's PromiseID; the server
// resolves the dashboard call's project scope and only then dispatches the
// promised widget call against it, eliding a client round trip. This is the
// canonical dashboard-builder flow: open a dashboard, drop a widget on it.
//
// Transport note (load-bearing): luxfi/zap processes a single connection's
// frames strictly FIFO — one handler runs to completion before the next frame is
// read (node.go dispatchLoop). Genuine concurrent in-flight calls therefore need
// SEPARATE connections, where the server runs two dispatch loops concurrently and
// its promise table (Server.await/resolve) coordinates them. `dep` is a SECOND
// client connection over which the dependent CreateWidget call is shipped; pass
// a Client dialed on its own *zaplib.Node. When dep == c (one connection) this
// still works but degrades to sequential (no overlap) because of FIFO.
//
// Proof (on the shared send log both clients append to): the widget send precedes
// the dashboard recv — the dependent call was on the wire before the call it
// depends on had answered.
func (c *Client) PipelineCreateDashboardThenWidget(
	ctx context.Context, dep *Client, dash gen.CreateDashReqInput, widget gen.WidgetReqInput,
) (gen.Dashboard, gen.Widget, error) {
	dashPromise := nextPipelineID()
	widgetPromise := nextPipelineID()

	var (
		gotDash   gen.Dashboard
		gotWidget gen.Widget
		dashErr   error
		widgetErr error
		wg        sync.WaitGroup
	)
	// barrier releases the dependent send only after the dashboard send is
	// committed to its wire, so the server resolves the dashboard promise id
	// before (or concurrently with) the dependent call's await.
	barrier := make(chan struct{})
	wg.Add(2)

	// Call #1: createDashboard on connection c — the promise the widget targets.
	go func() {
		defer wg.Done()
		close(barrier)
		resp, err := c.call(ctx, MethodCreateDashboard, dashPromise, NoTarget, gen.NewCreateDashReq(dash))
		if err != nil {
			dashErr = err
			return
		}
		if resp.Status != StatusOK {
			dashErr = fmt.Errorf("createDashboard: status %d: %s", resp.Status, resp.Body)
			return
		}
		gotDash, dashErr = gen.WrapDashboard(resp.Body)
	}()

	// Call #2: createWidget on connection dep, pipelined off the dashboard promise.
	// Shipped without awaiting the dashboard answer; the server holds it until the
	// dashboard call resolves the project scope.
	go func() {
		defer wg.Done()
		<-barrier
		resp, err := dep.call(ctx, MethodCreateWidget, widgetPromise, dashPromise, gen.NewWidgetReq(widget))
		if err != nil {
			widgetErr = err
			return
		}
		if resp.Status != StatusOK {
			widgetErr = fmt.Errorf("createWidget: status %d: %s", resp.Status, resp.Body)
			return
		}
		gotWidget, widgetErr = gen.WrapWidget(resp.Body)
	}()

	wg.Wait()
	if dashErr != nil {
		return gen.Dashboard{}, gen.Widget{}, dashErr
	}
	if widgetErr != nil {
		return gen.Dashboard{}, gen.Widget{}, widgetErr
	}
	return gotDash, gotWidget, nil
}

// SyntheticCap mints an in-memory CapKindIAMSession capability for tests and
// bootstrap: ed25519-signed (the SPEC bootstrap scheme), holding the given
// DashPermissions bits. The signature is real (ed25519); production wires an
// IAM-issued cap instead. Returns the opaque buffer to pass to Dial.
func SyntheticCap(perms uint64) ([]byte, error) {
	signer, err := zcap.NewEd25519Signer()
	if err != nil {
		return nil, err
	}
	c, err := zcap.Issue(zcap.Issuance{
		Kind:        uint32(zcap.KindIAMSession),
		Holder:      signer.Public(),
		Permissions: perms,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}, signer)
	if err != nil {
		return nil, err
	}
	return c.Bytes(), nil
}

// AllDashPermissions is every DashPermissions bit OR-ed — a fully-authorized
// session capability (the convenience the probe + happy-path tests use).
const AllDashPermissions = PermDashboardRead | PermDashboardWrite | PermAnalyticsRead |
	PermPresetRead | PermPresetWrite | PermMonitorRead | PermMonitorWrite
