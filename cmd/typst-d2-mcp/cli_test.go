package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHandleCLIVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		var out bytes.Buffer
		if !handleCLI([]string{arg}, &out) {
			t.Fatalf("%s: not handled", arg)
		}
		if want := "typst-d2-mcp version " + serverVersion + "\n"; out.String() != want {
			t.Errorf("%s: got %q, want %q", arg, out.String(), want)
		}
	}
}

func TestHandleCLIHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out bytes.Buffer
		if !handleCLI([]string{arg}, &out) {
			t.Fatalf("%s: not handled", arg)
		}
		if !strings.Contains(out.String(), envTransport) {
			t.Errorf("%s: help does not mention %s:\n%s", arg, envTransport, out.String())
		}
	}
}

// Anything else must still start the server, as it did before handleCLI.
func TestHandleCLIIgnoresOtherArgs(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"--stdio"}, {"serve"}} {
		var out bytes.Buffer
		if handleCLI(args, &out) {
			t.Errorf("%v: handled, want ignored", args)
		}
		if out.Len() != 0 {
			t.Errorf("%v: wrote %q", args, out.String())
		}
	}
}
