// Command squadron-plugin-localdev is a Squadron tool plugin that performs local
// software development. It clones GitHub repositories to the local filesystem and
// drives a pluggable LLM provider (AWS Bedrock) through three phases — Code
// Development, QA, and Review — using a tool-using agent that reads, writes, and
// runs commands directly in the local workspace.
//
// See README.md for configuration and usage.
package main

import (
	"fmt"
	"os"

	squadron "github.com/mlund01/squadron-sdk"

	// Register the AWS Bedrock provider. Additional providers register
	// themselves the same way via a blank import here.
	_ "github.com/ericlakich/squadron-plugin-localdev/provider/bedrock"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "squadron-plugin-localdev %s starting\n", version)
	squadron.Serve(&Plugin{})
}
