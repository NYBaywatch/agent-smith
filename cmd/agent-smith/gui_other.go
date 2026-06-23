//go:build !windows

package main

import (
	"context"

	"github.com/NYBaywatch/agent-smith/internal/engine"
)

// On non-Windows platforms there is no native GUI; the CLI dashboard is used.
func guiAvailable() bool { return false }

func launchGUI(ctx context.Context, e *engine.Engine) error { return nil }
