//go:build unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/lsp"
)

// TestHelperLSPServer es un servidor LSP mínimo: el propio binario de test
// relanzado por el "jdtls" falso (ver TestLSPLauncherRealProcess). Sin la
// variable de entorno no hace nada. Al iniciar lanza un descendiente
// (sleep) en su grupo, como la JVM que lanza el script de jdtls.
func TestHelperLSPServer(t *testing.T) {
	if os.Getenv("WARI_FAKE_LSP") != "1" {
		t.Skip("solo como servidor LSP falso")
	}
	br := bufio.NewReader(os.Stdin)
	var out sync.Mutex
	send := func(v any) {
		body, _ := json.Marshal(v)
		out.Lock()
		defer out.Unlock()
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	for {
		n := -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				n, _ = strconv.Atoi(v)
			}
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			os.Exit(0)
		}
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		_ = json.Unmarshal(body, &m)
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
		switch m.Method {
		case "initialize":
			_ = exec.Command("sleep", "300").Start() // descendiente en el mismo grupo
			reply(map[string]any{"capabilities": map[string]any{
				"completionProvider": map[string]any{}, "definitionProvider": true}})
		case "textDocument/completion":
			reply([]map[string]any{{"label": "getName()", "kind": 2}, {"label": "setName(String)", "kind": 2}})
		case "shutdown":
			reply(nil)
		case "exit":
			os.Exit(0)
		}
	}
}

// TestLSPLauncherRealProcess prueba el camino real: el gestor lanza el
// "jdtls" (un script que ejecuta este binario de test) con internal/proc, el
// cliente habla con él por stdin/stdout, la memoria se lee del grupo y, al
// cerrar el gestor, no queda ningún proceso del grupo (tampoco el
// descendiente).
func TestLSPLauncherRealProcess(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nexec '" + os.Args[0] + "' -test.run='^TestHelperLSPServer$'\n"
	if err := os.WriteFile(filepath.Join(bin, "jdtls"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WARI_FAKE_LSP", "1")

	var mu sync.Mutex
	var pgids []int
	launch := func(ctx context.Context, path string, args []string, dir string) (lsp.Process, error) {
		p, err := lspLauncher(ctx, path, args, dir)
		if err == nil {
			mu.Lock()
			pgids = append(pgids, p.PGID())
			mu.Unlock()
		}
		return p, err
	}
	mgr := lsp.NewManager(context.Background(), launch, lsp.Options{ShutdownTimeout: 3 * time.Second})
	defer mgr.Shutdown()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := mgr.Get(ctx, lsp.Known[0], root)
	if err != nil {
		t.Fatal(err)
	}
	uri := lsp.FileURI(filepath.Join(root, "A.java"))
	if err := c.DidOpen(uri, "java", "class A { void f(Obj obj) { obj. } }"); err != nil {
		t.Fatal(err)
	}
	items, err := c.Complete(ctx, uri, lsp.Position{Line: 0, Character: 33})
	if err != nil || len(items) != 2 || items[0].Label != "getName()" {
		t.Fatalf("completion=%v err=%v", items, err)
	}
	if runtime.GOOS == "linux" {
		st := mgr.Status()
		if len(st) != 1 || st[0].Name != "jdtls" || st[0].RSS <= 0 {
			t.Errorf("status=%+v", st)
		}
	}

	mu.Lock()
	pgid := pgids[0]
	mu.Unlock()
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("el grupo %d debía estar vivo: %v", pgid, err)
	}
	if err := mgr.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos en el grupo %d tras Shutdown", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(pgids) != 1 {
		t.Errorf("lanzamientos=%d", len(pgids))
	}
	mu.Unlock()
}
