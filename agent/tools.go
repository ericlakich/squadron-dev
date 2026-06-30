package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ericlakich/squadron-plugin-localdev/provider"
	"github.com/ericlakich/squadron-plugin-localdev/workspace"
)

// Tool is a local capability the model may invoke during the agent loop. Each
// tool runs against the session workspace on the local filesystem.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, input json.RawMessage) (string, error)
}

// Spec converts a Tool into the provider-facing specification advertised to the
// model.
func (t Tool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: t.InputSchema,
	}
}

// ToolOptions controls which workspace tools are exposed and how they behave.
type ToolOptions struct {
	AllowWrite     bool          // expose write_file and edit_file
	AllowCommands  bool          // expose run_command
	CommandTimeout time.Duration // per-command timeout
	MaxOutputBytes int           // cap on command/file output returned to the model
}

// WorkspaceTools builds the set of local tools backed by ws, gated by opts.
// Read and discovery tools are always present; mutation and command execution
// are opt-in so that read-only phases (QA, review) cannot alter the repository.
func WorkspaceTools(ws *workspace.Workspace, opts ToolOptions) []Tool {
	maxOut := opts.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 60_000
	}

	tools := []Tool{
		{
			Name:        "read_file",
			Description: "Read the full contents of a file in the workspace. The path is relative to the repository root.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to the repository root"}},"required":["path"]}`),
			Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
				var p struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(in, &p); err != nil {
					return "", err
				}
				content, err := ws.ReadFile(p.Path)
				if err != nil {
					return "", err
				}
				return capString(content, maxOut), nil
			},
		},
		{
			Name:        "list_dir",
			Description: "List the files and subdirectories of a directory in the workspace. Use an empty path or '.' for the repository root.",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path relative to the repository root"}}}`),
			Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
				var p struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal(in, &p)
				names, err := ws.ListDir(p.Path)
				if err != nil {
					return "", err
				}
				if len(names) == 0 {
					return "(empty directory)", nil
				}
				return strings.Join(names, "\n"), nil
			},
		},
		{
			Name:        "search_files",
			Description: "Search the workspace for lines matching a regular expression. Returns up to 200 path:line matches. Use this to locate code before reading or editing it.",
			InputSchema: schema(`{"type":"object","properties":{"pattern":{"type":"string","description":"RE2 regular expression to search for"},"path":{"type":"string","description":"Optional subdirectory to limit the search to"}},"required":["pattern"]}`),
			Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
				var p struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				if err := json.Unmarshal(in, &p); err != nil {
					return "", err
				}
				results, err := ws.Search(p.Pattern, p.Path, 200)
				if err != nil {
					return "", err
				}
				if len(results) == 0 {
					return "(no matches)", nil
				}
				var b strings.Builder
				for _, r := range results {
					fmt.Fprintf(&b, "%s:%d: %s\n", r.Path, r.Line, r.Text)
				}
				return capString(b.String(), maxOut), nil
			},
		},
	}

	if opts.AllowWrite {
		tools = append(tools,
			Tool{
				Name:        "write_file",
				Description: "Create a new file or overwrite an existing one with the given content. Parent directories are created automatically. Provide the complete file content.",
				InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to the repository root"},"content":{"type":"string","description":"The complete new contents of the file"}},"required":["path","content"]}`),
				Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
					var p struct {
						Path    string `json:"path"`
						Content string `json:"content"`
					}
					if err := json.Unmarshal(in, &p); err != nil {
						return "", err
					}
					if err := ws.WriteFile(p.Path, p.Content); err != nil {
						return "", err
					}
					return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
				},
			},
			Tool{
				Name:        "edit_file",
				Description: "Replace an exact substring in a file with new text. old_string must appear exactly once in the file. Use this for targeted edits instead of rewriting the whole file.",
				InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to the repository root"},"old_string":{"type":"string","description":"The exact text to replace (must be unique in the file)"},"new_string":{"type":"string","description":"The replacement text"}},"required":["path","old_string","new_string"]}`),
				Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
					var p struct {
						Path      string `json:"path"`
						OldString string `json:"old_string"`
						NewString string `json:"new_string"`
					}
					if err := json.Unmarshal(in, &p); err != nil {
						return "", err
					}
					content, err := ws.ReadFile(p.Path)
					if err != nil {
						return "", err
					}
					n := strings.Count(content, p.OldString)
					if n == 0 {
						return "", fmt.Errorf("old_string not found in %s", p.Path)
					}
					if n > 1 {
						return "", fmt.Errorf("old_string appears %d times in %s; make it unique", n, p.Path)
					}
					updated := strings.Replace(content, p.OldString, p.NewString, 1)
					if err := ws.WriteFile(p.Path, updated); err != nil {
						return "", err
					}
					return fmt.Sprintf("edited %s", p.Path), nil
				},
			},
		)
	}

	if opts.AllowCommands {
		timeout := opts.CommandTimeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		tools = append(tools, Tool{
			Name: "run_command",
			Description: fmt.Sprintf("Run a shell command in the repository root and return its combined stdout/stderr and exit code. "+
				"Use this to install dependencies, build, run tests, run linters, or invoke git. Commands time out after %s.", timeout),
			InputSchema: schema(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run, e.g. 'go test ./...' or 'npm install'"}},"required":["command"]}`),
			Handler: func(ctx context.Context, in json.RawMessage) (string, error) {
				var p struct {
					Command string `json:"command"`
				}
				if err := json.Unmarshal(in, &p); err != nil {
					return "", err
				}
				cctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				res, err := ws.RunCommand(cctx, p.Command, maxOut)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "exit code: %d", res.ExitCode)
				if res.TimedOut {
					b.WriteString(" (timed out)")
				}
				b.WriteString("\n")
				if strings.TrimSpace(res.Stdout) != "" {
					b.WriteString(res.Stdout)
				} else {
					b.WriteString("(no output)")
				}
				return b.String(), nil
			},
		})
	}

	return tools
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func capString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated, %d of %d bytes shown]", max, len(s))
}
