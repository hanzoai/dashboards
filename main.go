// Command dashboards is a Hanzo Base-native Go service binary: a typed ZAP
// capability-RPC backend for dashboards, widgets, tables, view-presets, and
// monitors, built on Hanzo Base (embedded SQLite + plugins). It replaces five
// in-process console tRPC routers (dashboardRouter, dashboardWidgetRouter,
// tableRouter, TableViewPresetsRouter, monitorsRouter) with one msgType-206
// capability router — no tRPC, no Mongo, no Cap'n Proto.
//
// Architecture (the reference pattern the sibling service binaries share):
//
//	base.New()                    → Base app: embedded SQLite, hooks, migrations
//	  ├── vault (optional)        → per-org encrypted SQLite shard (KEK)
//	  └── server.Register(node)   → THIS service's typed router (msgType 206)
//	app.Start()                   → serves HTTP :8090 + ZAP :9995
//
// The .zap schema (proto/) is the source of truth; gen/ is its Go projection.
package main

import (
	"crypto/rand"
	"log"
	"os"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/plugins/vault"
	"github.com/hanzoai/base/tools/hook"
	luxlog "github.com/luxfi/log"
	zaplib "github.com/luxfi/zap"
	zcap "github.com/zap-proto/go/cap"

	"github.com/hanzoai/dashboards/server"
)

// defaultZapAddr is the typed capability-RPC listen address. Port 9995 is this
// service's slot in the Hanzo Base service-binary port plan.
const defaultZapAddr = "127.0.0.1:9995"

func main() {
	app := base.New()

	var zapAddr string
	app.RootCmd.PersistentFlags().StringVar(&zapAddr, "zap", envOr("ZAP_ADDR", defaultZapAddr),
		"address for the typed ZAP capability-RPC listener")

	var vaultDir string
	app.RootCmd.PersistentFlags().StringVar(&vaultDir, "vaultDir", os.Getenv("VAULT_DIR"),
		"directory for per-org encrypted SQLite shards (enables the vault plugin)")

	app.RootCmd.ParseFlags(os.Args[1:])

	// Optional: per-org encrypted SQLite backing via the vault plugin. Enabled
	// only when --vaultDir is set so local dev stays single-file SQLite. The
	// master KEK comes from KMS in production; a process-ephemeral key is used
	// when VAULT_MASTER_KEY is unset (dev only — shards won't persist across
	// restarts, which is correct for throwaway dev data).
	if vaultDir != "" {
		vault.MustRegister(app, vault.Config{
			Enabled:   true,
			DataDir:   vaultDir,
			OrgID:     envOr("DASHBOARDS_ORG", "default"),
			MasterKey: masterKey(),
		})
	}

	// Ensure the five backing collections exist (idempotent, on bootstrap).
	server.RegisterCollections(app)

	// Stand up the typed ZAP router alongside Base's serve lifecycle. We run a
	// dedicated luxfi/zap node for the capability RPC (NoDiscovery: direct dial
	// only — service discovery is the gateway's job, not mDNS here).
	logger := luxlog.New("component", "dashboards")
	node := zaplib.NewNode(zaplib.NodeConfig{
		NodeID:      "dashboards",
		Port:        portOf(zapAddr),
		NoDiscovery: true,
	})

	// Verifier: bootstrap (ed25519, no issuer registry → Kind+Permissions
	// enforced, signature step skipped). Wire IssuerKey to the IAM pubkey
	// registry to enable full cryptographic verification.
	srv := server.NewServer(app, logger, zcap.Verifier{})
	srv.Register(node)

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "dashboardsZapNode",
		Func: func(e *core.ServeEvent) error {
			if err := e.Next(); err != nil {
				return err
			}
			if err := node.Start(); err != nil {
				return err
			}
			logger.Info("dashboards ZAP router listening", "addr", zapAddr, "msgType", server.MsgTypeRouterBase)
			return nil
		},
	})
	app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Id: "dashboardsZapNodeStop",
		Func: func(e *core.TerminateEvent) error {
			node.Stop()
			return e.Next()
		},
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// masterKey returns the 32-byte vault master KEK: from VAULT_MASTER_KEY (hex or
// raw 32 bytes) in production, else a process-ephemeral random key for dev.
func masterKey() []byte {
	if v := os.Getenv("VAULT_MASTER_KEY"); len(v) >= 32 {
		return []byte(v)[:32]
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// portOf extracts the port from a host:port address, defaulting to 9995.
func portOf(addr string) int {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p := 0
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return 9995
				}
				p = p*10 + int(c-'0')
			}
			if p == 0 {
				return 9995
			}
			return p
		}
	}
	return 9995
}
