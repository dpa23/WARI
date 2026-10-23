#!/usr/bin/env bash
# Instala WARI como un comando del sistema: después basta escribir `wari`.
# Uso: scripts/install.sh [directorio]   (por omisión ~/.local/bin, o $GOBIN si existe)
set -euo pipefail

cd "$(dirname "$0")/.."
dest="${1:-${GOBIN:-$HOME/.local/bin}}"
mkdir -p "$dest"
GOBIN="$dest" go install ./cmd/wari
echo "wari instalado en $dest/wari"

case ":$PATH:" in
*":$dest:"*) ;;
*) echo "aviso: $dest no está en el PATH; agrega 'export PATH=\"$dest:\$PATH\"' a tu shell" ;;
esac

echo "iconos: WARI usa Nerd Font; si tu terminal no la tiene, scripts/install-font.sh la instala desde assets/fonts (sin red)"
