#!/bin/sh
# Instala la JetBrainsMono Nerd Font incluida en assets/fonts (sin red).
set -eu

aqui=$(cd "$(dirname "$0")" && pwd)
origen="$aqui/../assets/fonts"

case "$(uname -s)" in
Darwin) destino="$HOME/Library/Fonts" ;;
Linux) destino="$HOME/.local/share/fonts" ;;
*) echo "install-font: sistema no soportado: $(uname -s)" >&2; exit 1 ;;
esac

mkdir -p "$destino"
cambio=0
for f in JetBrainsMonoNerdFont-Regular.ttf JetBrainsMonoNerdFont-Bold.ttf; do
	if [ ! -f "$origen/$f" ]; then
		echo "install-font: falta $origen/$f" >&2
		exit 1
	fi
	if [ -f "$destino/$f" ] && cmp -s "$origen/$f" "$destino/$f"; then
		echo "ya instalada: $f"
	else
		cp "$origen/$f" "$destino/$f"
		echo "instalada: $f -> $destino"
		cambio=1
	fi
done

if [ "$cambio" = 1 ] && [ "$(uname -s)" = Linux ] && command -v fc-cache >/dev/null 2>&1; then
	fc-cache -f "$destino"
fi
echo "Elige \"JetBrainsMono Nerd Font\" en la configuración de tu terminal."
