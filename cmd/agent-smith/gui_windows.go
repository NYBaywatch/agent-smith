//go:build windows

package main

import (
	"context"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/ui/gui"
)

func guiAvailable() bool { return true }

func launchGUI(ctx context.Context, e *engine.Engine) error { return gui.Run(ctx, e) }
