package main

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/config"
	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/model"
	cliui "github.com/NYBaywatch/agent-smith/internal/ui/cli"
)

// engineConfig builds the engine configuration from the defaults plus the
// user's config.json (extra anchors, synthetic checks, path/SLO settings).
// A broken config file is reported on stderr and ignored.
func engineConfig() engine.Config {
	cfg := engine.DefaultConfig()
	uc, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-smith: config:", err, "(using defaults)")
	}
	for _, a := range uc.Anchors {
		host := a.Host
		if net.ParseIP(host) == nil {
			addrs, err := net.LookupIP(host)
			if err != nil || len(addrs) == 0 {
				fmt.Fprintf(os.Stderr, "agent-smith: config: cannot resolve anchor %q, skipped\n", host)
				continue
			}
			host = addrs[0].String()
		}
		name := a.Name
		if name == "" {
			name = a.Host
		}
		cfg.Anchors = append(cfg.Anchors, engine.Target{Name: name, Host: host, Role: model.RoleInternet})
	}
	cfg.IPM.Synthetics = uc.Checks()
	cfg.IPM.SyntheticInterval = uc.SyntheticInterval()
	cfg.IPM.PathEnabled = uc.Path.Enabled
	cfg.IPM.PathInterval = uc.PathInterval()
	cfg.IPM.ProbesPerHop = uc.Path.ProbesPerHop
	cfg.IPM.SLO = baseline.SLO{Availability: uc.SLO.Availability, P95Ms: uc.SLO.P95Ms}
	cfg.IPM.Notify = uc.Notifications
	return cfg
}

// run starts the monitoring engine and launches a user interface. The headless
// CLI dashboard is the default everywhere; on Windows a native GUI is launched
// unless --cli is passed (see launchGUI in the platform-specific files).
func run(ctx context.Context, forceCLI bool) error {
	// Derive a cancellable context so that quitting the UI (e.g. the tray "Quit"
	// action, which does not raise SIGINT) also stops the engine and returns,
	// rather than hanging the process.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	eng, err := engine.New(engineConfig())
	if err != nil {
		return err
	}

	go func() { _ = eng.Run(ctx) }()

	// The UI call blocks until the user quits or ctx is cancelled. When it
	// returns, the deferred cancel() tears down the engine.
	if !forceCLI && guiAvailable() {
		return launchGUI(ctx, eng)
	}
	return cliui.Run(ctx, eng)
}
