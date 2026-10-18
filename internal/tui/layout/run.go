package layout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/sandbox"
)

// Ejecutar el proyecto en el sandbox (fase 13, desde el TUI).
//
// run_project detecta el perfil del proyecto (Maven, Gradle o .NET en la
// raíz) y lo ejecuta en un contenedor Podman o Docker (internal/sandbox): el
// proyecto real nunca se escribe. Antes de lanzar:
//
//   - Si la imagen no está descargada, avisa con el comando para descargarla
//     y no hace nada más: WARI nunca descarga imágenes por su cuenta (un
//     "run" del contenedor lo haría sin avisar).
//   - Si el perfil necesita red (los tres resuelven dependencias al arrancar),
//     pide confirmación: y permite la red del contenedor (bridge), n lo ejecuta
//     sin red (el valor por omisión del sandbox) y esc cancela.
//
// Los logs (stdout, stderr y los mensajes del runtime) llegan en streaming al
// panel de salida, que muestra siempre la tecla run_stop. promote_artifact
// copia un artefacto de las salidas de la ejecución (/out dentro del
// contenedor) al proyecto real, solo a petición explícita y confirmando antes
// de sobrescribir.
//
// Nada de esto bloquea Update: el contenedor se lanza y se lee en tea.Cmd, y
// cada resultado vuelve como mensaje con la generación de la ejecución para
// descartar lo que llega tarde.

// Sandbox es lo que el layout necesita de internal/sandbox; *sandbox.Container
// lo cumple y los tests inyectan uno sobre un runtime de contenedores falso.
type Sandbox interface {
	// HasImage dice si la imagen ya está descargada; nunca la descarga.
	HasImage(ctx context.Context, image string) bool
	// Run lanza la ejecución y vuelve en cuanto el contenedor fue lanzado.
	Run(ctx context.Context, spec sandbox.Spec) (*sandbox.Run, error)
}

const (
	// runBatch es el máximo de líneas que se entregan juntas a Update.
	runBatch = 200
	// preflightTimeout acota la comprobación de la imagen y del runtime.
	preflightTimeout = 30 * time.Second
)

// runState es el estado de la ejecución en el sandbox.
type runState struct {
	gen int
	// checking: comprobando la imagen; active: hay una ejecución (lanzándose
	// o en curso).
	checking, active bool
	// confirm es el perfil que espera la respuesta sobre la red.
	confirm *sandbox.Profile
	// run es la última ejecución lanzada (puede haber terminado: sus salidas
	// siguen en disco y promote_artifact las usa).
	run *sandbox.Run
	// stopWanted: el usuario pidió parar antes de que el contenedor se lanzara.
	stopWanted bool
	profile    string
	// promoteArmed es la ruta cuya sobrescritura ya se avisó.
	promoteArmed string
}

// Mensajes de la ejecución.
type (
	runPreflightMsg struct {
		gen     int
		profile sandbox.Profile
		has     bool
		rt      sandbox.Runtime
		rtErr   error
	}
	runStartedMsg struct {
		gen int
		run *sandbox.Run
		err error
	}
	runLinesMsg struct {
		gen   int
		lines []sandbox.Line
	}
	runEndedMsg struct {
		gen int
		err error
	}
	runStopMsg     struct{ err error }
	runPromotedMsg struct {
		rel string
		err error
	}
)

// profileNeedsNetwork dice si el perfil necesita red para ejecutarse: Maven,
// Gradle y dotnet resuelven y descargan dependencias al arrancar, y con la red
// en none eso falla (sandbox.Profiles lo advierte).
func profileNeedsNetwork(p sandbox.Profile) bool {
	switch p.Name {
	case sandbox.ProfileMaven.Name, sandbox.ProfileGradle.Name, sandbox.ProfileDotnet.Name:
		return true
	}
	return false
}

// runInfo es lo que la barra de estado muestra siempre mientras corre algo: el
// estado y la tecla de parada. "" si no hay ejecución.
func (m Model) runInfo() string {
	switch {
	case m.run.active:
		s := "▶ " + m.run.profile
		if k := m.firstKey("run_stop"); k != "" {
			s += " · " + k + " detener"
		}
		return s
	case m.run.checking || m.run.confirm != nil:
		return "▶ preparando " + m.run.profile
	}
	return ""
}

// runProject es la acción run_project.
func (m Model) runProject() (Model, tea.Cmd) {
	switch {
	case m.sbx == nil:
		m.msg = "la ejecución en el sandbox no está disponible"
		return m, nil
	case m.run.active || m.run.checking || m.run.confirm != nil:
		m.msg = "ya hay una ejecución en curso (" + m.firstKey("run_stop") + " la detiene)"
		return m, nil
	}
	p, ok := sandbox.DetectProfile(m.root)
	if !ok {
		m.msg = "no se detectó un proyecto Maven (pom.xml), Gradle (build.gradle) ni .NET (*.csproj) en la raíz"
		return m, nil
	}
	m.run.gen++
	m.run.checking, m.run.profile = true, p.Name
	m.run.stopWanted = false
	m.msg = "comprobando la imagen " + p.Image + "…"

	sbx, gen := m.sbx, m.run.gen
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
		defer cancel()
		msg := runPreflightMsg{gen: gen, profile: p}
		msg.rt, msg.rtErr = sandbox.DetectRuntime(ctx, "")
		if msg.rtErr == nil {
			msg.has = sbx.HasImage(ctx, p.Image)
		}
		return msg
	}
}

// onRunPreflight sigue tras comprobar la imagen: avisa si falta, pide la
// confirmación de red o lanza.
func (m Model) onRunPreflight(msg runPreflightMsg) (Model, tea.Cmd) {
	if msg.gen != m.run.gen || !m.run.checking {
		return m, nil
	}
	m.run.checking = false
	m.msg = ""
	p := msg.profile
	m.openDock()
	switch {
	case msg.rtErr != nil:
		m.appendOut("runtime", "No se puede ejecutar: no hay podman ni docker en el PATH.")
		m.msg = "no hay podman ni docker en el PATH"
		return m, nil
	case !msg.has:
		m.appendOut("runtime", fmt.Sprintf("La imagen %s no está descargada.", p.Image))
		m.appendOut("runtime", "WARI nunca descarga imágenes por su cuenta. Descárgala tú y repite:")
		m.appendOut("runtime", fmt.Sprintf("    %s pull %s", msg.rt.Name, p.Image))
		m.msg = fmt.Sprintf("falta la imagen %s: %s pull %s", p.Image, msg.rt.Name, p.Image)
		return m, nil
	case profileNeedsNetwork(p):
		m.run.confirm = &p
		m.appendOut("runtime", fmt.Sprintf("El perfil %s descarga dependencias al arrancar y necesita red (el contenedor corre sin red por omisión).", p.Name))
		return m, nil
	}
	return m.launchRun(p, sandbox.NetworkNone)
}

// runConfirmKey atiende la pregunta de la red. Cualquier otra tecla la
// cancela, como las demás confirmaciones.
func (m Model) runConfirmKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	p := *m.run.confirm
	m.run.confirm = nil
	switch s := k.String(); {
	case s == "y" || s == "s":
		return m.launchRun(p, sandbox.NetworkBridge)
	case s == "n":
		return m.launchRun(p, sandbox.NetworkNone)
	}
	m.appendOut("runtime", "Ejecución cancelada.")
	m.msg = "ejecución cancelada"
	return m, nil
}

// launchRun lanza el contenedor con la red indicada.
func (m Model) launchRun(p sandbox.Profile, network string) (Model, tea.Cmd) {
	m.run.active, m.run.profile = true, p.Name
	m.run.run = nil // la anterior ya no cuenta: Stop y promote_artifact miran la nueva
	net := "sin red"
	if network != sandbox.NetworkNone {
		net = "con red (" + network + ")"
	}
	m.appendOut("runtime", fmt.Sprintf("Ejecutando %s en %s, %s: %s", p.Name, p.Image, net, strings.Join(p.Command, " ")))
	m.openDock()

	sbx, h, gen, root := m.sbx, m.hub, m.run.gen, m.root
	return m, func() tea.Msg {
		run, err := sbx.Run(h.ctx, sandbox.Spec{Root: root, Profile: p.Name, Network: network})
		if err == nil && !h.setRun(run) {
			err = errors.New("WARI se está cerrando")
		}
		return runStartedMsg{gen: gen, run: run, err: err}
	}
}

// onRunStarted incorpora el resultado del lanzamiento y empieza a leer logs.
func (m Model) onRunStarted(msg runStartedMsg) (Model, tea.Cmd) {
	if msg.gen != m.run.gen {
		return m, nil
	}
	if msg.err != nil {
		m.run.active = false
		m.appendOut("runtime", "No se pudo lanzar: "+msg.err.Error())
		m.msg = "no se pudo ejecutar: " + msg.err.Error()
		return m, nil
	}
	m.run.run = msg.run
	m.run.promoteArmed = ""
	m.msg = "ejecutando " + m.run.profile
	if m.run.stopWanted {
		m.run.stopWanted = false
		return m, tea.Batch(pumpRun(msg.gen, msg.run), stopRunCmd(msg.run))
	}
	return m, pumpRun(msg.gen, msg.run)
}

// pumpRun espera la siguiente tanda de líneas de log: bloquea en la primera y
// recoge sin esperar las que ya estén listas, para no inundar a Update.
func pumpRun(gen int, run *sandbox.Run) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-run.Logs()
		if !ok {
			return runEndedMsg{gen: gen, err: run.Wait()}
		}
		lines := []sandbox.Line{l}
		for len(lines) < runBatch {
			select {
			case l, ok := <-run.Logs():
				if !ok {
					return runLinesMsg{gen: gen, lines: lines}
				}
				lines = append(lines, l)
			default:
				return runLinesMsg{gen: gen, lines: lines}
			}
		}
		return runLinesMsg{gen: gen, lines: lines}
	}
}

// onRunLines añade las líneas al panel de salida y sigue leyendo.
func (m Model) onRunLines(msg runLinesMsg) (Model, tea.Cmd) {
	if msg.gen != m.run.gen || m.run.run == nil {
		return m, nil
	}
	for _, l := range msg.lines {
		m.appendOut(string(l.Stream), l.Text)
	}
	return m, pumpRun(msg.gen, m.run.run)
}

// onRunEnded cierra la ejecución con su resultado.
func (m Model) onRunEnded(msg runEndedMsg) (Model, tea.Cmd) {
	if msg.gen != m.run.gen {
		return m, nil
	}
	m.run.active = false
	var ee *exec.ExitError
	switch {
	case msg.err == nil:
		m.appendOut("runtime", "La ejecución terminó (código 0).")
		m.msg = "ejecución terminada"
	case errors.As(msg.err, &ee) && ee.ExitCode() >= 0:
		m.appendOut("runtime", fmt.Sprintf("La ejecución terminó con código %d.", ee.ExitCode()))
		m.msg = fmt.Sprintf("ejecución terminada con código %d", ee.ExitCode())
	default:
		m.appendOut("runtime", "La ejecución se detuvo.")
		m.msg = "ejecución detenida"
	}
	return m, nil
}

// runStop es la acción run_stop: cancela la pregunta o la comprobación en
// curso, o detiene el contenedor.
func (m Model) runStop() (Model, tea.Cmd) {
	switch {
	case m.run.confirm != nil:
		m.run.confirm = nil
		m.appendOut("runtime", "Ejecución cancelada.")
		m.msg = "ejecución cancelada"
		return m, nil
	case m.run.checking:
		m.run.checking = false
		m.run.gen++ // el resultado de la comprobación ya no sirve
		m.msg = "ejecución cancelada"
		return m, nil
	case !m.run.active:
		m.msg = "no hay ninguna ejecución en curso"
		return m, nil
	case m.run.run == nil:
		m.run.stopWanted = true
		m.msg = "deteniendo la ejecución…"
		return m, nil
	}
	m.msg = "deteniendo la ejecución…"
	return m, stopRunCmd(m.run.run)
}

func stopRunCmd(run *sandbox.Run) tea.Cmd {
	return func() tea.Msg { return runStopMsg{err: run.Stop()} }
}

// --- Promover un artefacto ---------------------------------------------------------

// promoteStart abre la entrada de la ruta del artefacto, relativa a las
// salidas de la última ejecución.
func (m Model) promoteStart() (Model, tea.Cmd) {
	run := m.run.run
	if run == nil {
		m.msg = "no hay ninguna ejecución de la que promover artefactos"
		return m, nil
	}
	m.run.promoteArmed = ""
	m.pal = palette{
		open:   true,
		mode:   palPromote,
		prompt: "Promover artefacto (ruta en /out): ",
		note:   promoteNote(run),
	}
	return m, nil
}

// promoteNote lista las salidas disponibles para elegir.
func promoteNote(run *sandbox.Run) string {
	entries, err := os.ReadDir(run.OutDir)
	var names []string
	if err == nil {
		for _, e := range entries {
			if n := e.Name(); !strings.HasPrefix(n, ".") {
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
		}
	}
	note := "enter acepta · esc cancela"
	switch {
	case len(names) == 0:
		return "sin artefactos en /out (el proyecto escribe ahí con $WARI_OUT) · " + note
	case len(names) > 6:
		names = append(names[:6], "…")
	}
	return "en /out: " + strings.Join(names, " ") + " · " + note
}

// submitPromote acepta la ruta: si ya existe en el proyecto, pide repetir
// enter para sobrescribir; después copia en segundo plano.
func (m Model) submitPromote() (Model, tea.Cmd) {
	run := m.run.run
	rel := strings.TrimSpace(string(m.pal.query))
	if run == nil || rel == "" {
		m.pal.note = "escribe la ruta del artefacto, relativa a /out"
		return m, nil
	}
	if !filepath.IsLocal(rel) {
		m.pal.note = "la ruta debe ser relativa y quedarse dentro de /out"
		return m, nil
	}
	if _, err := os.Lstat(filepath.Join(run.Root, filepath.FromSlash(rel))); err == nil && m.run.promoteArmed != rel {
		m.run.promoteArmed = rel
		m.pal.note = rel + " ya existe en el proyecto: enter otra vez para sobrescribirlo · esc cancela"
		return m, nil
	}
	m.run.promoteArmed = ""
	m.pal = palette{}
	// El watcher también ve la copia: se avisa antes para que no se la
	// atribuya a un agente en marcha.
	m.chat.NoteUserWrite(filepath.Join(run.Root, filepath.FromSlash(rel)))
	m.msg = "promoviendo " + rel + "…"
	return m, func() tea.Msg { return runPromotedMsg{rel: rel, err: sandbox.Promote(run, rel)} }
}

// onRunPromoted informa del resultado de la promoción.
func (m Model) onRunPromoted(msg runPromotedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.msg = "no se pudo promover " + msg.rel + ": " + msg.err.Error()
		m.appendOut("runtime", "No se pudo promover "+msg.rel+": "+msg.err.Error())
		return m, nil
	}
	m.tree.Refresh()
	m.msg = "promovido " + msg.rel + " al proyecto"
	m.appendOut("runtime", "Promovido al proyecto: "+msg.rel)
	return m, m.refreshIndex()
}

// runUpdate atiende los mensajes de la ejecución. handled es false para los
// demás.
func (m Model) runUpdate(msg tea.Msg) (_ Model, _ tea.Cmd, handled bool) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case runPreflightMsg:
		m, cmd = m.onRunPreflight(msg)
	case runStartedMsg:
		m, cmd = m.onRunStarted(msg)
	case runLinesMsg:
		m, cmd = m.onRunLines(msg)
	case runEndedMsg:
		m, cmd = m.onRunEnded(msg)
	case runStopMsg:
		if msg.err != nil {
			m.msg = "detener la ejecución: " + msg.err.Error()
		}
	case runPromotedMsg:
		m, cmd = m.onRunPromoted(msg)
	default:
		return m, nil, false
	}
	return m, cmd, true
}
