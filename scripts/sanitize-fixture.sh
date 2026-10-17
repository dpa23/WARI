#!/usr/bin/env bash
# Reemplaza datos personales en salidas grabadas de CLIs antes de versionarlas
# (el repo es público): correos, rutas de usuario, el nombre de usuario local
# donde aparezca (p. ej. dentro de rutas temporales) y UUIDs (IDs de sesión o
# conversación). Cada UUID distinto recibe un marcador distinto, para que el
# archivo siga siendo coherente. Modifica los archivos en su lugar.
# Uso: scripts/sanitize-fixture.sh <archivo.jsonl>...
set -euo pipefail

if [[ $# -eq 0 ]]; then
	echo "uso: scripts/sanitize-fixture.sh <archivo.jsonl>..." >&2
	exit 2
fi

# Nombres muy cortos o "user" producirían reemplazos sin sentido.
local_user=${USER:-}
export SANITIZE_USER=""
if [[ ${#local_user} -ge 3 && $local_user != user ]]; then
	SANITIZE_USER=$local_user
fi

# Los UUID van primero: un nombre de usuario con aspecto hexadecimal no debe
# romperlos antes de reemplazarlos. El nombre solo se reemplaza como palabra
# completa (no dentro de "device" si el usuario es "dev").
for f in "$@"; do
	perl -pi -e '
		s{\b([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b}{
			$id{lc $1} //= sprintf("00000000-0000-0000-0000-%012d", ++$n)
		}gei;
		s{[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}}{user\@example.com}g;
		s{/(home|Users)/[^/"\\\s]+}{/$1/user}g;
		s{(?<![A-Za-z0-9])\Q$ENV{SANITIZE_USER}\E(?![A-Za-z0-9])}{user}g if length $ENV{SANITIZE_USER};
	' "$f"

	# Avisa si queda el nombre de usuario local en el archivo ("user" es el
	# propio marcador de reemplazo).
	if [[ -n ${USER:-} && $USER != user ]] && grep -Fq -- "$USER" "$f"; then
		echo "AVISO: $f todavía contiene \"$USER\"; revísalo a mano" >&2
	fi
done
