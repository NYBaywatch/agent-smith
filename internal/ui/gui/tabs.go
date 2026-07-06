//go:build windows

package gui

// tabVisibility reports which of n stacked content panels should be visible
// when the tab at index `active` is selected. Exactly one panel is visible
// (the active one); if active is out of range, none are.
func tabVisibility(active, n int) []bool {
	out := make([]bool, n)
	if active >= 0 && active < n {
		out[active] = true
	}
	return out
}
