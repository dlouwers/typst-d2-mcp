package main

import (
	"testing"
	"time"
)

func TestDurationEnv(t *testing.T) {
	const key = "TYPST_D2_MCP_TEST_DURATION"
	tests := []struct {
		name string
		set  string
		want time.Duration
	}{
		{"unset uses default", "", 17 * time.Second},
		{"valid duration", "5s", 5 * time.Second},
		{"valid sub-second", "250ms", 250 * time.Millisecond},
		{"garbage falls back", "not-a-duration", 17 * time.Second},
		{"negative falls back", "-1s", 17 * time.Second},
		{"zero accepted", "0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(key, tt.set)
			if got := durationEnv(key, 17*time.Second); got != tt.want {
				t.Errorf("durationEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInt64Env(t *testing.T) {
	const key = "TYPST_D2_MCP_TEST_INT"
	tests := []struct {
		name string
		set  string
		want int64
	}{
		{"unset uses default", "", 100},
		{"valid integer", "4096", 4096},
		{"zero accepted", "0", 0},
		{"garbage falls back", "abc", 100},
		{"negative falls back", "-1", 100},
		{"large value", "1099511627776", 1099511627776},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(key, tt.set)
			if got := int64Env(key, 100); got != tt.want {
				t.Errorf("int64Env() = %v, want %v", got, tt.want)
			}
		})
	}
}
