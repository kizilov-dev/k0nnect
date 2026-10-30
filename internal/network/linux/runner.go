package linux

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Runner never invokes a shell. Input may contain a private key and must never be logged.
type Runner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, program string, args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// wg may echo invalid configuration, so never return its stderr.
		if program == "wg" {
			return nil, fmt.Errorf("wg %s failed: %w (diagnostic output suppressed to protect keys)", args[0], err)
		}
		return nil, fmt.Errorf("%s %v: %w: %s", program, args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
