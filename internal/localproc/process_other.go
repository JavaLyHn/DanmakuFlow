//go:build !windows

package localproc

import (
	"context"
	"errors"
	"os"
)

var errUnsupported = errors.New("local process control is only supported on Windows")

func Start(StartOptions) (Identity, error)        { return Identity{}, errUnsupported }
func Inspect(int) (Identity, error)               { return Identity{}, errUnsupported }
func Gone(Identity) (bool, error)                 { return false, errUnsupported }
func Listeners(int) ([]Identity, error)           { return nil, errUnsupported }
func stopConsole(context.Context, Identity) error { return errUnsupported }
func ConsoleHelper(string) error                  { return errUnsupported }
func SecureDir(path string) error                 { return os.MkdirAll(path, 0700) }
func ProcessArgs(int) ([]string, error)           { return nil, errUnsupported }
func Track(Identity) error                        { return errUnsupported }
func ExitStatus(Identity) (bool, uint32, bool)    { return false, 0, false }
