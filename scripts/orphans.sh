#!/usr/bin/env bash
# Lista los procesos que siguen vivos en un grupo de procesos (incluidos
# zombis: un zombi indica que faltó un Wait). Sale con 1 si queda alguno.
# Uso: scripts/orphans.sh <pgid>
set -euo pipefail

pgid=${1:?uso: scripts/orphans.sh <pgid>}

# Un -o por columna: en BSD ps el texto tras "=" se toma como encabezado
# hasta el final del argumento.
left=$(ps -e -o pid= -o pgid= -o state= -o comm= | awk -v g="$pgid" '$2 == g')
if [[ -n $left ]]; then
	printf 'procesos vivos en el grupo %s:\n  PID  PGID S COMM\n%s\n' "$pgid" "$left"
	exit 1
fi
echo "grupo $pgid: sin procesos"
