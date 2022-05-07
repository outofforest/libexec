package libexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/outofforest/parallel"
)

type cmdError struct {
	Err   error
	Debug string
}

// Error returns the string representation of an Error.
func (e cmdError) Error() string {
	return fmt.Sprintf("%s: %q", e.Err, e.Debug)
}

// Exec executes commands sequentially and terminates the running one gracefully if context is cancelled
func Exec(ctx context.Context, cmds ...*exec.Cmd) error {
	for _, cmd := range cmds {
		cmd := cmd
		if cmd.Stdout == nil {
			cmd.Stdout = os.Stdout
		}
		if cmd.Stderr == nil {
			cmd.Stderr = os.Stderr
		}
		if cmd.Stdin == nil {
			// If Stdin is nil, then exec library tries to assign it to /dev/null
			// Null device does not exist in chrooted environment unless created, so we set a fake nil buffer
			// just to remove this dependency
			cmd.Stdin = bytes.NewReader(nil)
		}

		if err := cmd.Start(); err != nil {
			return err
		}

		err := parallel.Run(ctx, func(ctx context.Context, spawn parallel.SpawnFn) error {
			spawn("cmd", parallel.Exit, func(ctx context.Context) error {
				err := cmd.Wait()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					return &cmdError{Err: err, Debug: cmd.String()}
				}
				return nil
			})
			spawn("ctx", parallel.Exit, func(ctx context.Context) error {
				<-ctx.Done()
				_ = cmd.Process.Signal(syscall.SIGTERM)
				return ctx.Err()
			})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
