//go:build !unix

package proc

import (
	"context"
	"errors"
)

func startPTY(context.Context, Spec) (*Process, error) {
	return nil, errors.ErrUnsupported
}

// Resize no está disponible sin PTY de Unix.
func (p *Process) Resize(uint16, uint16) error { return errors.ErrUnsupported }
