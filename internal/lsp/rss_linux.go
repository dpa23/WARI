//go:build linux

package lsp

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// MemoryRSS devuelve en bytes la memoria residente del servidor: la suma de
// VmRSS (/proc/<pid>/status) de todos los procesos de su grupo, porque
// jdtls es un script que lanza la JVM como descendiente. Sin proceso
// asociado devuelve 0 y ErrNoProcess.
func (c *Client) MemoryRSS() (int64, error) {
	if c.proc == nil || c.proc.PGID() <= 0 {
		return 0, ErrNoProcess
	}
	return groupRSS("/proc", c.proc.PGID())
}

func groupRSS(procDir string, pgid int) (int64, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0, fmt.Errorf("lsp: leer %s: %w", procDir, err)
	}
	var total int64
	found := false
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procDir, pid))
		if err != nil || parsePgrp(stat) != pgid {
			continue
		}
		found = true
		status, err := os.ReadFile(fmt.Sprintf("%s/%d/status", procDir, pid))
		if err != nil {
			continue // terminó entre las dos lecturas
		}
		total += parseVmRSS(status)
	}
	if !found {
		return 0, ErrNoProcess
	}
	return total, nil
}

// parsePgrp extrae el grupo de /proc/<pid>/stat. El nombre del comando va
// entre paréntesis y puede contenerlos, así que se parte desde el último ")".
func parsePgrp(stat []byte) int {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return -1
	}
	f := bytes.Fields(stat[i+1:]) // estado, ppid, pgrp, ...
	if len(f) < 3 {
		return -1
	}
	n, err := strconv.Atoi(string(f[2]))
	if err != nil {
		return -1
	}
	return n
}

// parseVmRSS devuelve los bytes de la línea "VmRSS:  1234 kB"; 0 si no hay
// (hilos del kernel).
func parseVmRSS(status []byte) int64 {
	for _, line := range bytes.Split(status, []byte{'\n'}) {
		rest, ok := bytes.CutPrefix(line, []byte("VmRSS:"))
		if !ok {
			continue
		}
		f := bytes.Fields(rest)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(string(f[0]), 10, 64)
		if err != nil {
			return 0
		}
		return kb << 10
	}
	return 0
}
