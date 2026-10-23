# Fuentes incluidas

JetBrainsMono Nerd Font (Regular y Bold): JetBrains Mono parcheada con los
glifos de Nerd Fonts (https://www.nerdfonts.com). WARI la usa para los iconos
del panel de agentes y del árbol de archivos (`-icons nerd`).

- Licencia: SIL Open Font License 1.1 (`OFL.txt`), que permite redistribuirla.
- Origen: release JetBrainsMono de Nerd Fonts (https://github.com/ryanoasis/nerd-fonts).
- No se incrusta en el binario: es para instalarla en el sistema.

## Instalación

```sh
scripts/install-font.sh
```

Copia las dos `.ttf` a `~/.local/share/fonts` (Linux) o `~/Library/Fonts`
(macOS), sin red. Después elige "JetBrainsMono Nerd Font" en la configuración
de tu terminal. Sin ella, usa `-icons emoji` o `-icons ascii`.
