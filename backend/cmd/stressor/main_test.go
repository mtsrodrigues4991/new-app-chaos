package main

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"512k": 512 << 10, "256m": 256 << 20, "1g": 1 << 30, "100": 100} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	if n, err := parseSize("10%"); err != nil || n <= 0 {
		t.Errorf("10%%: %d %v", n, err)
	}
	if _, err := parseSize("abc"); err == nil {
		t.Error("esperava erro")
	}
}
