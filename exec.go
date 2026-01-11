package libexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/outofforest/logger"
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
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setsid = true
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
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

		logger.Get(ctx).Debug("Executing command", zap.Stringer("command", cmd))

		if err := cmd.Start(); err != nil {
			return errors.WithStack(err)
		}

		err := parallel.Run(ctx, func(ctx context.Context, spawn parallel.SpawnFn) error {
			spawn("cmd", parallel.Exit, func(ctx context.Context) error {
				err := cmd.Wait()
				if ctx.Err() != nil {
					return errors.WithStack(ctx.Err())
				}
				if err != nil {
					return errors.WithStack(cmdError{Err: err, Debug: cmd.String()})
				}
				return nil
			})
			spawn("ctx", parallel.Fail, func(ctx context.Context) error {
				<-ctx.Done()
				_ = cmd.Process.Signal(syscall.SIGTERM)
				_ = cmd.Process.Signal(syscall.SIGINT)
				return errors.WithStack(ctx.Err())
			})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Kill tries to terminate processes gracefully, after timeout it kills them
func Kill(ctx context.Context, pids []int) error {
	return parallel.Run(ctx, func(ctx context.Context, spawn parallel.SpawnFn) error {
		for _, pid := range pids {
			pid := pid
			spawn(fmt.Sprintf("%d", pid), parallel.Continue, func(ctx context.Context) error {
				return parallel.Run(ctx, func(ctx context.Context, spawn parallel.SpawnFn) error {
					proc, err := os.FindProcess(pid)
					if err != nil {
						return errors.WithStack(err)
					}
					spawn("waiter", parallel.Exit, func(ctx context.Context) error {
						_, _ = proc.Wait()
						return nil
					})
					spawn("killer", parallel.Continue, func(ctx context.Context) error {
						if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
							return errors.WithStack(err)
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(20 * time.Second):
						}
						if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
							return errors.WithStack(err)
						}
						return nil
					})
					return nil
				})
			})
		}
		return nil
	})
}
