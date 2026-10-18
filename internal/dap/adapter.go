package dap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Errores de arranque de adaptadores.
var (
	// ErrAdapterNotFound indica que el adaptador no está en el PATH.
	ErrAdapterNotFound = errors.New("dap: adaptador no encontrado en el PATH")
	// ErrNeedsPortProvider indica un adaptador TCP sin forma de averiguar su
	// puerto (ver PortProvider).
	ErrNeedsPortProvider = errors.New("dap: el adaptador necesita un PortProvider")
)

// Transport es cómo se habla con el adaptador.
type Transport int

// Transportes soportados.
const (
	// Stdio: el adaptador es un proceso que habla por stdin/stdout.
	Stdio Transport = iota
	// TCP: el adaptador escucha en un puerto y el cliente se conecta.
	TCP
)

// Adapter describe un adaptador de depuración conocido.
type Adapter struct {
	// Name es el nombre visible.
	Name string
	// AdapterID se envía en initialize.
	AdapterID string
	// Command es el ejecutable que se busca en el PATH; vacío si el
	// adaptador no tiene ejecutable propio (java-debug vive dentro de jdtls).
	Command string
	// Args son los argumentos de arranque (Stdio).
	Args []string
	// Transport indica Stdio o TCP.
	Transport Transport
	// Exts son las extensiones (con punto) que depura.
	Exts []string
}

// Known son los adaptadores soportados.
//
// netcoredbg se lanza como proceso con --interpreter=vscode (DAP por stdio).
//
// java-debug no es un ejecutable: es un plugin de jdtls
// (com.microsoft.java.debug.plugin, que debe cargarse como bundle en
// initializationOptions de jdtls). Para depurar hay que pedirle a jdtls, con
// workspace/executeCommand "vscode.java.startDebugSession", que levante un
// servidor DAP: el resultado es un puerto TCP local. Eso requiere un cliente
// LSP con jdtls en marcha (fase 12), así que este paquete solo se conecta
// por TCP y deja el hook PortProvider (ConnectOptions.Port) para que la
// integración pida el puerto. Sin PortProvider, Connect devuelve
// ErrNeedsPortProvider.
var Known = []Adapter{
	{
		Name: "netcoredbg", AdapterID: "coreclr", Command: "netcoredbg",
		Args: []string{"--interpreter=vscode"}, Transport: Stdio, Exts: []string{".cs"},
	},
	{
		Name: "java-debug", AdapterID: "java", Transport: TCP,
		Exts: []string{".java", ".kt"},
	},
}

// Detect busca el ejecutable en el PATH. Un adaptador sin Command (TCP vía
// hook) devuelve "" sin error: su disponibilidad depende del hook. No lanza
// nada.
func (a Adapter) Detect() (string, error) {
	if a.Command == "" {
		return "", nil
	}
	p, err := exec.LookPath(a.Command)
	if err != nil {
		return "", errors.Join(ErrAdapterNotFound, err)
	}
	return p, nil
}

// AdapterForPath devuelve el adaptador conocido que depura la extensión de
// path, esté instalado o no.
func AdapterForPath(path string) (Adapter, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, a := range Known {
		for _, e := range a.Exts {
			if e == ext {
				return a, true
			}
		}
	}
	return Adapter{}, false
}

// PortProvider devuelve la dirección TCP ("host:puerto") del servidor DAP de
// un adaptador TCP, arrancándolo si hace falta. Para java-debug: ejecutar
// vscode.java.startDebugSession en jdtls y devolver "127.0.0.1:<puerto>".
type PortProvider func(ctx context.Context) (addr string, err error)

// ConnectOptions configura Connect.
type ConnectOptions struct {
	// Launcher lanza adaptadores Stdio; nil usa DefaultLauncher.
	Launcher Launcher
	// Dir es el directorio de trabajo del adaptador.
	Dir string
	// Port es el hook de los adaptadores TCP.
	Port PortProvider
	// DialTimeout acota la conexión TCP; 0 usa 10 segundos.
	DialTimeout time.Duration
	// Client son las opciones del cliente.
	Client Options
}

// Connect abre la conexión con un adaptador. Para Stdio lo lanza bajo
// demanda (ctx debe ser de larga vida: al cancelarse se termina el grupo);
// para TCP obtiene la dirección del hook y se conecta. Cerrar el cliente
// termina el proceso y sus descendientes sin dejar huérfanos.
func Connect(ctx context.Context, a Adapter, o ConnectOptions) (*Client, error) {
	switch a.Transport {
	case TCP:
		if o.Port == nil {
			return nil, fmt.Errorf("%w: %s", ErrNeedsPortProvider, a.Name)
		}
		addr, err := o.Port(ctx)
		if err != nil {
			return nil, fmt.Errorf("dap: puerto de %s: %w", a.Name, err)
		}
		return ConnectTCP(ctx, addr, o)
	default:
		path, err := a.Detect()
		if err != nil {
			return nil, err
		}
		if path == "" {
			return nil, fmt.Errorf("%w: %s no tiene ejecutable", ErrAdapterNotFound, a.Name)
		}
		launch := o.Launcher
		if launch == nil {
			launch = DefaultLauncher
		}
		p, err := launch(ctx, path, a.Args, o.Dir)
		if err != nil {
			return nil, fmt.Errorf("dap: lanzar %s: %w", a.Name, err)
		}
		return NewProcessClient(p, o.Client), nil
	}
}

// ConnectTCP se conecta a un servidor DAP que ya escucha en addr.
func ConnectTCP(ctx context.Context, addr string, o ConnectOptions) (*Client, error) {
	timeout := o.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dap: conectar a %s: %w", addr, err)
	}
	return NewClient(conn, conn, o.Client), nil
}
