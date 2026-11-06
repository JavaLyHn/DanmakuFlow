package localproc

import (
 "context"
 "os"
 "strings"
)

type Identity struct { PID int; Created uint64; Executable string }
type StartOptions struct { Executable string; Args []string; Dir string; Env []string; Console bool; Stdout, Stderr *os.File }

func Matches(id Identity) bool {
 if id.PID <= 0 || id.Created == 0 || id.Executable == "" { return false }
 actual, err := Inspect(id.PID)
 return err == nil && actual.Created == id.Created && samePath(actual.Executable, id.Executable)
}
func samePath(a,b string) bool { return strings.EqualFold(a,b) }

// StopConsole never terminates a process when the context expires.
func StopConsole(ctx context.Context, id Identity) error { return stopConsole(ctx,id) }
