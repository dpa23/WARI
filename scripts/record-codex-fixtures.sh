#!/usr/bin/env bash
# Graba las salidas reales de `codex exec --json` que deben sustituir a los
# fixtures sintéticos (testdata/codex/synthetic_*.jsonl, ver
# testdata/codex/SYNTHETIC.md) y las sanitiza. Hay que ejecutarlo tras
# `codex login`. Corre en un directorio fijo y neutro que no es un repo git
# (por eso --skip-git-repo-check; ver record-claude-fixtures.sh). Consume
# cuota del modelo por defecto de codex.
# Uso: scripts/record-codex-fixtures.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
out=$repo/testdata/codex
work=/tmp/wari-codex-fixture

rm -rf "$work"
mkdir -p "$work" "$out"
cd "$work"
echo "contenido de prueba 42" >nota.txt

# record <nombre> <sandbox> <args de codex exec...>: el código de salida no
# corta el script, porque algunos casos terminan con error a propósito. Los
# flags son los mismos que arma internal/agent/codex.Args (resume no admite
# -s: el modo va antes de `resume`, y se repite con -c). El stderr de
# resume_missing se conserva porque es la única señal de ese error; el resto
# se descarta.
record() {
	local name=$1 sandbox=$2
	shift 2
	codex exec --json --skip-git-repo-check -s "$sandbox" "$@" </dev/null >"$out/$name.jsonl" 2>"$out/$name.stderr" || true
	echo "$name: $(wc -l <"$out/$name.jsonl") líneas"
}

record simple read-only -- "Responde solo: hola"
record tool_read read-only -- "Lee nota.txt ejecutando un comando y responde solo con su contenido."
record write_workspace workspace-write -- "Crea el archivo hola.txt con el texto hola. No preguntes."
rm -f hola.txt
record write_readonly read-only -- "Crea el archivo hola.txt con el texto hola. No preguntes."
rm -f hola.txt
thread=$(grep -o '"thread_id":"[^"]*"' "$out/simple.jsonl" | head -1 | cut -d'"' -f4 || true)
if [[ -z $thread ]]; then
	echo "simple.jsonl no trae thread_id: ¿hiciste codex login?" >&2
	exit 1
fi
record resume read-only -c 'sandbox_mode="read-only"' resume -- "$thread" "¿Qué palabra respondiste antes? Responde solo esa palabra."
# Verificado con 0.163.0: stdout vacío, código 1 y "no rollout found for
# thread id" en stderr.
record resume_missing read-only -c 'sandbox_mode="read-only"' resume -- 11111111-1111-4111-8111-111111111111 "hola"
record error_model read-only --model=modelo-inexistente -- "hola"

# Estos IDs no los cubre sanitize-fixture.sh (los UUID y las rutas sí).
mv "$out/resume_missing.stderr" "$out/resume_missing.stderr.txt"
rm -f "$out"/*.stderr
perl -pi -e '
	s{cf-ray: [0-9a-f]+-[A-Za-z]+}{cf-ray: 0000000000000000-XXX}g;
	s{request id: req_[0-9A-Za-z]+}{request id: req_000000}g;
	s{\breq_[0-9a-f]{16,}\b}{req_000000}g;
' "$out"/simple.jsonl "$out"/tool_read.jsonl "$out"/write_workspace.jsonl \
	"$out"/write_readonly.jsonl "$out"/resume.jsonl "$out"/resume_missing.jsonl \
	"$out"/error_model.jsonl "$out"/resume_missing.stderr.txt
"$repo/scripts/sanitize-fixture.sh" "$out"/simple.jsonl "$out"/tool_read.jsonl \
	"$out"/write_workspace.jsonl "$out"/write_readonly.jsonl "$out"/resume.jsonl \
	"$out"/resume_missing.jsonl "$out"/error_model.jsonl "$out"/resume_missing.stderr.txt
cd /
rm -rf "$work"
