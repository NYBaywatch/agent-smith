//go:build windows

package main

// The Windows resource (rsrc_windows.syso) embeds the application manifest from
// build/agent-smith.manifest, which declares Common Controls v6 (required by
// lxn/walk) and per-monitor DPI awareness. It is committed to the repo so the
// GUI builds reproducibly without extra tooling. To regenerate after editing the
// manifest, install rsrc (go install github.com/akavel/rsrc@latest) and run:
//
//go:generate rsrc -manifest ../../build/agent-smith.manifest -arch amd64 -o rsrc_windows.syso
