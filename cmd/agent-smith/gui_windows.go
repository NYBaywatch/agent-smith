//go:build windows

package main

import (
	"context"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/ui/web"
)

func guiAvailable() bool { return true }

func launchGUI(ctx context.Context, e *engine.Engine) error { return web.Run(ctx, e, version) }
