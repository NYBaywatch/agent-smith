package main

import (
	"context"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	cliui "github.com/NYBaywatch/agent-smith/internal/ui/cli"
)

// run starts the monitoring engine and launches a user interface. The headless
// CLI dashboard is the default everywhere; on Windows a native GUI is launched
// unless --cli is passed (see launchGUI in the platform-specific files).
func run(ctx context.Context, forceCLI bool) error {
	eng, err := engine.New(engine.DefaultConfig())
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- eng.Run(ctx) }()

	if !forceCLI && guiAvailable() {
		if err := launchGUI(ctx, eng); err != nil {
			return err
		}
	} else {
		if err := cliui.Run(ctx, eng); err != nil {
			return err
		}
	}

	<-ctx.Done()
	return nil
}
