#!/usr/bin/env bash
# Reemplaza datos personales en salidas grabadas de CLIs antes de versionarlas
# (el repo es público): correos, rutas de usuario y UUIDs (IDs de sesión o
# conversación). Cada UUID distinto recibe un marcador distinto, para que el
# archivo siga siendo coherente. Modifica los archivos en su lugar.
# Uso: scripts/sanitize-fixture.sh <archivo.jsonl>...
set -euo pipefail

if [[ $# -eq 0 ]]; then
	echo "uso: scripts/sanitize-fixture.sh <archivo.jsonl>..." >&2
	exit 2
fi

for f in "$@"; do
	perl -pi -e '
		s{[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}}{user\@example.com}g;
		s{/(home|Users)/[^/"\\\s]+}{/$1/user}g;
		s{\b([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b}{
			$id{lc $1} //= sprintf("00000000-0000-0000-0000-%012d", ++$n)
		}gei;
	' "$f"

	# Avisa si queda el nombre de usuario local en el archivo ("user" es el
	# propio marcador de reemplazo).
	if [[ -n ${USER:-} && $USER != user ]] && grep -Fq -- "$USER" "$f"; then
		echo "AVISO: $f todavía contiene \"$USER\"; revísalo a mano" >&2
	fi
done
