//go:build !unix

package dap

import "testing"

func assertNoOrphans(t *testing.T, pgid int) { t.Helper() }
