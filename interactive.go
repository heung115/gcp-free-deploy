package main

import (
	"context"
	"io"
	"os/exec"
)

// InteractiveRunner is used only for browser-based user authentication. Tokens
// queried for credential checks still use the bounded, captured Runner path.
type InteractiveRunner interface {
	RunInteractive(context.Context, Command, io.Reader, io.Writer, io.Writer) CommandResult
}

func (ExecRunner) RunInteractive(ctx context.Context, c Command, in io.Reader, out, errOut io.Writer) CommandResult {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return CommandResult{ExitCode: 1}
	}
	return CommandResult{}
}
