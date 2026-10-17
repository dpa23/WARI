#!/usr/bin/env bash
# Graba las salidas reales de `claude` que usan los tests del adaptador
# (testdata/claude/*.jsonl) y las sanitiza. Corre en un directorio fijo y
# neutro: el texto del modelo llega partido en deltas, y una ruta con el
# nombre del usuario partida entre dos deltas no se puede sanitizar después.
# Consume cuota (modelo haiku). Uso: scripts/record-claude-fixtures.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
out=$repo/testdata/claude
work=/tmp/wari-fixture

rm -rf "$work"
mkdir -p "$work" "$out"
cd "$work"
echo "contenido de prueba 42" >nota.txt

stream=(--output-format stream-json --verbose --include-partial-messages)
ask=(--permission-mode manual --permission-prompts none)

# record <nombre> <flags y prompt para claude -p>. La salida con error es
# parte de algunos casos, así que el código de salida no corta el script.
record() {
	local name=$1
	shift
	claude -p "$@" </dev/null >"$out/$name.jsonl" 2>/dev/null || true
	echo "$name: $(wc -l <"$out/$name.jsonl") líneas"
}

record simple "${stream[@]}" --model haiku "${ask[@]}" -- "Responde solo: hola"
record tool_read "${stream[@]}" --model haiku "${ask[@]}" -- \
	"Lee el archivo nota.txt con la herramienta Read y responde solo con su contenido."
record write_denied "${stream[@]}" --model haiku "${ask[@]}" -- \
	"Crea el archivo hola.txt con el texto hola usando la herramienta Write. No preguntes, solo hazlo."
record bash_write_auto "${stream[@]}" --model haiku --permission-mode auto -- \
	"Crea el archivo hola.txt con el texto hola usando la herramienta Bash. No preguntes, solo hazlo."

session=$(grep -o '"session_id":"[^"]*"' "$out/simple.jsonl" | head -1 | cut -d'"' -f4)
record resume "${stream[@]}" --model haiku "${ask[@]}" --resume "$session" -- \
	"¿Qué palabra respondiste antes? Responde solo esa palabra."
record error_model "${stream[@]}" --model modelo-inexistente "${ask[@]}" -- "hola"
record resume_missing "${stream[@]}" --model haiku "${ask[@]}" \
	--resume 00000000-0000-4000-8000-000000000099 -- "hola"

"$repo/scripts/sanitize-fixture.sh" "$out"/*.jsonl

# Los eventos system (init, commands_changed) describen el entorno de quien
# graba: skills, plugins, conectores MCP, comandos, rutas de memoria y de
# sockets. El parser no los usa: se vacían conservando el tipo de cada
# campo, para no publicar esa huella.
python3 -I - "$out"/*.jsonl <<'EOF'
import json, sys

ENV_FIELDS = ("mcp_servers", "skills", "plugins", "slash_commands",
              "terminal_slash_commands", "agents", "memory_paths",
              "messaging_socket_path", "commands")

for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as f:
        lines = f.readlines()
    out = []
    for line in lines:
        d = json.loads(line)
        if d.get("type") == "system":
            for k in ENV_FIELDS:
                if k in d:
                    d[k] = type(d[k])()
            line = json.dumps(d, ensure_ascii=False, separators=(",", ":")) + "\n"
        out.append(line)
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(out)
EOF

cd /
rm -rf "$work"
