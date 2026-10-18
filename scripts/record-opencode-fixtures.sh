#!/usr/bin/env bash
# Graba las salidas reales de `opencode run --format json` que usan los tests
# del adaptador (testdata/opencode/*.jsonl) y las sanitiza. Corre en un
# directorio fijo y neutro (ver record-claude-fixtures.sh). Consume cuota del
# modelo por defecto de opencode. Uso: scripts/record-opencode-fixtures.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
out=$repo/testdata/opencode
work=/tmp/wari-opencode-fixture

rm -rf "$work"
mkdir -p "$work" "$out"
cd "$work"
echo "contenido de prueba 42" >nota.txt

# record <nombre> <args de opencode run...>: el código de salida no corta el
# script, porque algunos casos terminan con error a propósito.
record() {
	local name=$1
	shift
	opencode run --standalone --format json "$@" </dev/null >"$out/$name.jsonl" 2>"$out/$name.stderr" || true
	echo "$name: $(wc -l <"$out/$name.jsonl") líneas"
}

record simple "Responde solo: hola"
record tool_read "Lee nota.txt con la herramienta de lectura y responde solo con su contenido."
record write_build "Crea el archivo hola.txt con el texto hola usando la herramienta de escritura. No preguntes."
rm -f hola.txt
record write_plan --agent plan "Crea el archivo hola.txt con el texto hola usando la herramienta de escritura. No preguntes."
session=$(grep -o '"sessionID":"[^"]*"' "$out/simple.jsonl" | head -1 | cut -d'"' -f4)
record resume --session "$session" "¿Qué palabra respondiste antes? Responde solo esa palabra."
record resume_missing --session ses_inexistente0000000000 "hola"
record error_model --model proveedor-inexistente/modelo-inexistente "hola"

python3 -I - "$out" <<'EOF'
import glob, json, os, re, sys

out = sys.argv[1]
user = os.environ.get("USER", "")
ids = {}

def stable(m):
    return ids.setdefault(m.group(0), "%s_%012d" % (m.group(1), len(ids) + 1))

for path in sorted(glob.glob(out + "/*.jsonl")):
    text = open(path, encoding="utf-8").read()
    # ids de opencode: ses_/msg_/prt_ + alfanumérico; call-<uuid>
    text = re.sub(r"\b(ses|msg|prt)_[0-9A-Za-z]{6,}", stable, text)
    text = re.sub(r"call-[0-9a-f-]{36}", lambda m: ids.setdefault(m.group(0), "call-%032d" % (len(ids) + 1)), text)
    text = re.sub(r"/(home|Users)/[^/\"\\\s]+", r"/\1/user", text)
    if len(user) >= 3 and user != "user":
        text = re.sub(r"(?<![A-Za-z0-9])%s(?![A-Za-z0-9])" % re.escape(user), "user", text)
    open(path, "w", encoding="utf-8").write(text)
EOF

# El stderr crudo no se versiona (puede traer rutas); solo importa su existencia.
rm -f "$out"/*.stderr
"$repo/scripts/sanitize-fixture.sh" "$out"/*.jsonl
cd /
rm -rf "$work"
