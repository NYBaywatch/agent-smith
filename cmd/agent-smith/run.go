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
	// Derive a cancellable context so that quitting the UI (e.g. the tray "Quit"
	// action, which does not raise SIGINT) also stops the engine and returns,
	// rather than hanging the process.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	eng, err := engine.New(engine.DefaultConfig())
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
