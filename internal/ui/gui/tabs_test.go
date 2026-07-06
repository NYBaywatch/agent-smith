//go:build windows

package gui

import "testing"

func TestTabVisibility(t *testing.T) {
	got := tabVisibility(2, 5)
	want := []bool{false, false, true, false, false}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestTabVisibilityOutOfRange(t *testing.T) {
	// active out of range => nothing visible, but slice still length n.
	got := tabVisibility(9, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, v := range got {
		if v {
			t.Fatalf("index %d visible, want none visible: %v", i, got)
		}
	}
}
