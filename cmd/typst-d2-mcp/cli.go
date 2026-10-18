package main

import (
	"fmt"
	"io"
)

// handleCLI answers the two questions a command line can ask without
// starting the server: which version this is, and how to use it. It returns
// true when it handled the arguments and the process should exit.
//
// Everything else is configured through environment variables, so any other
// argument is ignored as before; an MCP client config that passes stray args
// keeps working.
//
// Before this, `typst-d2-mcp --help` started a stdio server that waited on
// stdin. The Homebrew formula's test ran exactly that and hung until brew's
// timeout, which the brew smoke test caught (#159).
func handleCLI(args []string, w io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(w, "typst-d2-mcp version %s\n", serverVersion)
		return true
	case "help", "--help", "-h":
		fmt.Fprintf(w, `typst-d2-mcp %s — MCP server for Typst documents with D2 diagrams

Usage:
  typst-d2-mcp            start the server (stdio by default)
  typst-d2-mcp version    print the version
  typst-d2-mcp help       show this help

Configuration is through environment variables, for example:
  %-24s stdio (default) or http
  %-24s listen address for http
  %-24s workspace directory
  %-24s none (default) or github

See the README for the full list.
`, serverVersion, envTransport, envAddr, envWorkspace, envAuth)
		return true
	}
	return false
}
