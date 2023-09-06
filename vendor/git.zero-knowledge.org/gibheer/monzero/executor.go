package monzero

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// CheckExec runs a command line string.
// The output is recorded completely and returned as one message.
func CheckExec(check Check, ctx context.Context) CheckResult {
	result := CheckResult{}

	cmd := exec.CommandContext(ctx, check.Command[0], check.Command[1:]...)
	output := bytes.NewBuffer([]byte{})
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if err != nil {
		if cmd.ProcessState == nil {
			result.Message = fmt.Sprintf("unknown error when running command: %w", err)
			result.ExitCode = 3
			return result
		}

		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok {
			result.Message = fmt.Sprintf("error running check: %w", err)
			result.ExitCode = 2
		} else {
			result.ExitCode = status.ExitStatus()
		}
	}
	result.Message = output.String()
	return result
}
