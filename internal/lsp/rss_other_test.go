//go:build !linux

package lsp

import (
	"errors"
	"testing"
)

func TestMemoryRSSUnsupported(t *testing.T) {
	c := &Client{}
	n, err := c.MemoryRSS()
	if n != 0 || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("n=%d err=%v", n, err)
	}
}
