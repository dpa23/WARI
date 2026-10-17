#!/usr/bin/env bash
# Genera por stdout un archivo Java de exactamente 5000 líneas.
# Determinista: cada ejecución produce el mismo contenido.
# Uso: scripts/gen-java-fixture.sh > testdata/java/Grande.java
set -euo pipefail
export LC_ALL=C

awk '
function L(s) { print s; n++ }

function method(k,   r) {
    r = k % 4
    if (r == 0) {
        L("    /**")
        L("     * Calcula el acumulado del bloque " k ".")
        L("     * @param valor límite superior del recorrido")
        L("     * @return suma acumulada")
        L("     */")
        L("    public int calcular" k "(int valor) {")
        L("        int total = 0;")
        L("        for (int i = 0; i < valor; i++) {")
        L("            if (i % 2 == 0) {")
        L("                total += i;")
        L("            }")
        L("        }")
        L("        return total;")
        L("    }")
    } else if (r == 1) {
        L("    /**")
        L("     * Devuelve la etiqueta del código " k ".")
        L("     * @param codigo código numérico de entrada")
        L("     * @return etiqueta en español")
        L("     */")
        L("    public String etiqueta" k "(int codigo) {")
        L("        switch (codigo) {")
        L("            case 0:")
        L("                return \"cero\";")
        L("            case 1:")
        L("                return \"uno\";")
        L("            case 2:")
        L("                return \"año\";")
        L("            default:")
        L("                return \"canción\";")
        L("        }")
        L("    }")
    } else if (r == 2) {
        L("    /**")
        L("     * Agrupa los valores del bloque " k " por paridad.")
        L("     * @param datos valores de entrada")
        L("     * @return mapa con las claves \"par\" e \"impar\"")
        L("     */")
        L("    public Map<String, List<Integer>> agrupar" k "(List<Integer> datos) {")
        L("        Map<String, List<Integer>> grupos = new HashMap<>();")
        L("        grupos.put(\"par\", new ArrayList<>());")
        L("        grupos.put(\"impar\", new ArrayList<>());")
        L("        for (Integer dato : datos) {")
        L("            if (dato != null && dato % 2 == 0) {")
        L("                grupos.get(\"par\").add(dato);")
        L("            } else if (dato != null) {")
        L("                grupos.get(\"impar\").add(dato);")
        L("            }")
        L("        }")
        L("        return grupos;")
        L("    }")
    } else {
        L("    /**")
        L("     * Construye un mensaje de estado para el registro " k ".")
        L("     * @param nombre nombre del usuario")
        L("     * @param activo indica si la cuenta está activa")
        L("     * @return mensaje localizado")
        L("     */")
        L("    public String mensaje" k "(String nombre, boolean activo) {")
        L("        StringBuilder sb = new StringBuilder();")
        L("        sb.append(\"Usuario: \").append(nombre);")
        L("        if (activo) {")
        L("            sb.append(\" - cuenta activa\");")
        L("        } else {")
        L("            sb.append(\" - cuenta inactiva, revise el año de alta\");")
        L("        }")
        L("        int intentos = 0;")
        L("        while (intentos < 3) {")
        L("            intentos++;")
        L("        }")
        L("        return sb.toString();")
        L("    }")
    }
    L("")
}

BEGIN {
    n = 0
    L("package com.empresa.demo;")
    L("")
    L("import java.util.ArrayList;")
    L("import java.util.HashMap;")
    L("import java.util.List;")
    L("import java.util.Map;")
    L("")
    L("/**")
    L(" * Clase de demostración con muchos métodos para probar el editor.")
    L(" * Contiene comentarios Javadoc, estructuras de control y textos en español.")
    L(" */")
    L("public class Grande {")

    k = 0
    while (4999 - n > 40) {
        k++
        method(k)
    }

    # Cierre: 9 líneas fijas más relleno hasta dejar la línea 4999 como último
    # carácter antes de la llave final de la clase (línea 5000).
    r = 4999 - n
    L("    /**")
    L("     * Cierre de la clase, con texto de relleno.")
    L("     */")
    L("    public String cierre(int n) {")
    for (i = 1; i <= r - 9; i++) {
        L("        // Nota " i ": línea de relleno para completar el archivo.")
    }
    L("        if (n > 0) {")
    L("            return \"canción\";")
    L("        }")
    L("        return \"año\";")
    L("    }")
    L("}")
}
'
