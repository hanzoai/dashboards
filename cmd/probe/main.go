// Command probe exercises a LIVE dashboards service over ZAP — the
// out-of-process smoke test. It mints a fully-authorized synthetic
// CapKindIAMSession capability, connects to the service at --addr, and runs a
// dashboard CRUD round-trip plus a pipelined dashboard→widget create, printing
// each result. Exit 0 on success, non-zero on any failure.
//
//	go run ./cmd/probe --addr 127.0.0.1:9995 --peer dashboards
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	zaplib "github.com/luxfi/zap"

	gen "github.com/hanzoai/dashboards/gen"
	"github.com/hanzoai/dashboards/server"
)

const probeProject = "probe-project"

func main() {
	addr := flag.String("addr", "127.0.0.1:9995", "service ZAP address")
	peer := flag.String("peer", "dashboards", "service ZAP node id")
	flag.Parse()

	if err := run(*addr, *peer); err != nil {
		fmt.Fprintln(os.Stderr, "PROBE FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("PROBE OK")
}

func run(addr, peer string) error {
	capBuf, err := server.SyntheticCap(server.AllDashPermissions)
	if err != nil {
		return fmt.Errorf("mint cap: %w", err)
	}

	// The pipelined create needs two connections (FIFO transport). Each gets a
	// UNIQUE node id — the server rejects duplicate peer ids, so id collisions
	// silently drop the second connection.
	clientN := 0
	mkClient := func(log *[]server.SendEvent) (*server.Client, func(), error) {
		clientN++
		node := zaplib.NewNode(zaplib.NodeConfig{
			NodeID:      fmt.Sprintf("dash-probe-%d-%d", os.Getpid(), clientN),
			Port:        0, // OS-assigned ephemeral port
			NoDiscovery: true,
		})
		if err := node.Start(); err != nil {
			return nil, nil, err
		}
		c, err := server.Dial(node, addr, peer, capBuf)
		if err != nil {
			node.Stop()
			return nil, nil, err
		}
		if log != nil {
			c.WithSendLog(log)
		}
		return c, node.Stop, nil
	}

	var log []server.SendEvent
	cli, stop1, err := mkClient(&log)
	if err != nil {
		return err
	}
	defer stop1()
	dep, stop2, err := mkClient(&log)
	if err != nil {
		return err
	}
	defer stop2()

	time.Sleep(200 * time.Millisecond) // handshake settle

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Create + get a dashboard.
	d, err := cli.CreateDashboard(ctx, gen.CreateDashReqInput{ProjectId: probeProject, Name: "Probe board"})
	if err != nil {
		return fmt.Errorf("CreateDashboard: %w", err)
	}
	fmt.Printf("CreateDashboard: id=%s name=%q owner=%s\n", d.Id(), d.Name(), d.Owner())

	got, err := cli.GetDashboard(ctx, gen.IdReqInput{ProjectId: probeProject, Id: d.Id()})
	if err != nil {
		return fmt.Errorf("GetDashboard: %w", err)
	}
	fmt.Printf("GetDashboard: name=%q\n", got.Name())

	// 2. List.
	list, err := cli.AllDashboards(ctx, gen.ListReqInput{ProjectId: probeProject, Page: 1, Limit: 50})
	if err != nil {
		return fmt.Errorf("AllDashboards: %w", err)
	}
	fmt.Printf("AllDashboards: total=%d\n", list.TotalCount())

	// 3. Pipelined create dashboard + widget.
	log = log[:0]
	pd, pw, err := cli.PipelineCreateDashboardThenWidget(ctx, dep,
		gen.CreateDashReqInput{ProjectId: probeProject, Name: "Pipelined board"},
		gen.WidgetReqInput{
			ProjectId: probeProject, Name: "Probe widget", View: "traces",
			Dimensions: "[]", Metrics: "[]", Filters: "[]", ChartType: "NUMBER", ChartConfig: `{"type":"NUMBER"}`,
		})
	if err != nil {
		return fmt.Errorf("Pipeline: %w", err)
	}
	fmt.Printf("Pipeline: dashboard=%s widget=%s\n", pd.Id(), pw.Id())
	fmt.Printf("Pipeline send log: %s\n", fmtLog(log))

	// Verify the pipelining invariant: ≥2 sends before the first recv.
	sends, firstRecv := 0, -1
	for i, e := range log {
		if e.Kind == "recv" {
			firstRecv = i
			break
		}
		sends++
	}
	if firstRecv == -1 || sends < 2 {
		return fmt.Errorf("pipelining not observed: %d sends before first recv", sends)
	}
	fmt.Printf("Pipelining verified: %d calls in flight before the first answer\n", sends)
	return nil
}

func fmtLog(log []server.SendEvent) string {
	out := ""
	for _, e := range log {
		m := "createDashboard"
		if e.Method == server.MethodCreateWidget {
			m = "createWidget"
		}
		out += fmt.Sprintf("%s(%s,p=%d,t=%d) ", e.Kind, m, e.PromiseID, e.Target)
	}
	return out
}
