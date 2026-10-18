#!/usr/bin/env bash
# Graba las salidas reales de `agy` que usan los tests del adaptador
# (testdata/agy/*.jsonl) y las sanitiza. Corre en un directorio fijo y neutro:
# el texto del modelo llega partido en deltas, y una ruta con el nombre del
# usuario partida entre dos deltas no se puede sanitizar después.
# Consume cuota de Google (modelo gemini-3.8-flash-low). El texto de los
# modelos varía entre corridas: tras regrabar hay que revisar los tests que
# comparan texto. Uso: scripts/record-agy-fixtures.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
out=$repo/testdata/agy
work=/tmp/wari-agy-fixture
model=gemini-3.8-flash-low

mkdir -p "$work" "$out"
# Solo se borran los archivos de las corridas anteriores de este directorio.
find "$work" -mindepth 1 -maxdepth 1 -type f -delete
cd "$work"
echo "contenido de prueba 42" >nota.txt

# record <nombre> <flags y prompt para agy>. El prompt va pegado al flag
# (-p="..."): así nunca se interpreta como otro flag. La salida con error es
# parte de algunos casos, así que el código de salida no corta el script.
record() {
	local name=$1 prompt=$2
	shift 2
	agy -p="$prompt" --output-format stream-json --print-timeout 180s "$@" \
		</dev/null >"$out/$name.jsonl" 2>"$work/$name.err" || true
	echo "$name: $(wc -l <"$out/$name.jsonl") líneas"
}

record simple "Responde solo: hola" --model $model --mode plan
record tool_read "Lee el archivo nota.txt con la herramienta view_file y responde solo con su contenido." \
	--model $model --mode plan
record write_accept "Crea el archivo hola.txt con el texto hola usando la herramienta write_to_file. No preguntes, solo hazlo." \
	--model $model --mode accept-edits
record edit_accept "Cambia en nota.txt el número 42 por 43 usando la herramienta replace_file_content. No preguntes, solo hazlo." \
	--model $model --mode accept-edits
# Una petición de cambio normal en plan: la CLI planea y no toca el proyecto.
record write_plan "Cambia en nota.txt el número 42 por 43." --model $model --mode plan
# --mode plan es una orden al modelo, no un bloqueo: si se le pide la
# herramienta de escritura por su nombre, escribe en el proyecto.
record write_plan_explicit "Crea el archivo plan_explicito.txt con el texto hola usando la herramienta write_to_file. No preguntes, solo hazlo." \
	--model $model --mode plan
record shell_denied "Ejecuta el comando 'echo hola > shell.txt' con la herramienta run_command. No preguntes, solo hazlo." \
	--model $model --mode accept-edits

conv=$(grep -o '"conversation_id":"[^"]*"' "$out/simple.jsonl" | head -1 | cut -d'"' -f4)
record resume "¿Qué palabra respondiste antes? Responde solo esa palabra." \
	--model $model --mode plan --conversation "$conv"
record resume_missing "hola" --model $model --mode plan \
	--conversation 00000000-0000-4000-8000-000000000099
record error_model "hola" --model modelo-inexistente --mode plan

"$repo/scripts/sanitize-fixture.sh" "$out"/*.jsonl

# El evento init describe el entorno de quien graba (lista de herramientas,
# plugins, MCP, comandos). El parser no lo usa: se vacían esos campos
# conservando su tipo.
python3 -I - "$out"/*.jsonl <<'PY'
import json, sys

ENV_FIELDS = ("tools", "expanded_commands", "plugins", "mcp_servers",
              "skills", "agents", "commands")

for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as f:
        lines = f.readlines()
    out = []
    for line in lines:
        d = json.loads(line)
        if d.get("event") == "init" and isinstance(d.get("init"), dict):
            for k in ENV_FIELDS:
                if k in d["init"]:
                    d["init"][k] = type(d["init"][k])()
            line = json.dumps(d, ensure_ascii=False, separators=(",", ":")) + "\n"
        out.append(line)
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(out)
PY
