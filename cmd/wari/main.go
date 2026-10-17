// Command wari es un editor de código TUI con agentes múltiples.
package main

import "fmt"

// version se fija en el build con -ldflags "-X main.version=...".
var version = "dev"

func main() {
	fmt.Printf("wari %s — en desarrollo (fase 0)\n", version)
}
