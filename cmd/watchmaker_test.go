//go:build linux && (amd64 || arm64)

package main

import "testing"

func TestParseParentPID(t *testing.T) {
	for _, stat := range []string{
		"42 (worker) S 10 11 12",
		"42 (clock worker) S 10 11 12",
		"42 (clock (worker)) S 10 11 12",
		"42 (clock ) worker) S 10 11 12",
		"42 (clock\nworker) S 10 11 12",
	} {
		t.Run(stat, func(t *testing.T) {
			parent, err := parseParentPID(stat)
			if err != nil || parent != 10 {
				t.Fatalf("parent=%d, error=%v", parent, err)
			}
		})
	}
	for _, stat := range []string{"", "42 worker S 10", "42 (worker)", "42 (worker) S", "42 (worker) S invalid"} {
		if _, err := parseParentPID(stat); err == nil {
			t.Fatalf("accepted invalid stat: %q", stat)
		}
	}
}
