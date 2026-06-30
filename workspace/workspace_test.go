package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestFileRoundTripAndListing(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile("pkg/foo.go", "package pkg\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ws.ReadFile("pkg/foo.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != "package pkg\n" {
		t.Errorf("ReadFile = %q", got)
	}
	names, err := ws.ListDir("pkg")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(names) != 1 || names[0] != "foo.go" {
		t.Errorf("ListDir = %v, want [foo.go]", names)
	}
}

func TestSearch(t *testing.T) {
	ws, _ := Open(t.TempDir())
	_ = ws.WriteFile("a.txt", "hello world\nsecond line\n")
	_ = ws.WriteFile("b.txt", "nothing here\n")
	results, err := ws.Search("world", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "a.txt" || results[0].Line != 1 {
		t.Errorf("Search = %+v, want one match in a.txt line 1", results)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	ws, _ := Open(t.TempDir())
	if _, err := ws.ReadFile("../../etc/passwd"); err == nil {
		t.Error("expected path-escape read to be rejected")
	}
	if err := ws.WriteFile("../escape.txt", "x"); err == nil {
		t.Error("expected path-escape write to be rejected")
	}
}

func TestRunCommand(t *testing.T) {
	ws, _ := Open(t.TempDir())
	res, err := ws.RunCommand(context.Background(), "echo hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello") {
		t.Errorf("RunCommand = %+v, want exit 0 with 'hello'", res)
	}
	res, err = ws.RunCommand(context.Background(), "exit 3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
}
