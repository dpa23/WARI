//go:build !linux

package lsp

import (
	"errors"
	"fmt"
)

// MemoryRSS solo está implementado en Linux; aquí devuelve 0 y
// errors.ErrUnsupported.
func (c *Client) MemoryRSS() (int64, error) {
	return 0, fmt.Errorf("lsp: memoria del proceso: %w", errors.ErrUnsupported)
}
