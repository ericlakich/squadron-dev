// Command squadron-dev is a Squadron tool plugin that performs local
// software development. It clones GitHub repositories to the local filesystem and
// drives a pluggable Amazon Bedrock provider (bedrock-mantle or bedrock-runtime)
// through three phases — Code Development, QA, and Review — using a tool-using
// agent that reads, writes, and runs commands directly in the local workspace.
//
// See README.md for configuration and usage.
package main

import (
	"fmt"
	"os"

	squadron "github.com/mlund01/squadron-sdk"

	// Register the providers. Each registers itself via its init function, so a
	// blank import here is all that's needed to make it selectable by name.
	//   - bedrock-mantle: OpenAI-compatible Responses API on the mantle endpoint (default)
	//   - bedrock-runtime: AWS Bedrock Converse API via the AWS SDK (alias: "bedrock")
	_ "github.com/ericlakich/squadron-dev/provider/bedrock"
	_ "github.com/ericlakich/squadron-dev/provider/mantle"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "squadron-dev %s starting\n", version)
	squadron.Serve(&Plugin{})
}
