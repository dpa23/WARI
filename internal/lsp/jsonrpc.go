// Package lsp es un cliente mínimo del Language Server Protocol: JSON-RPC 2.0
// sobre stdio con cabeceras Content-Length, escrito a mano con la biblioteca
// estándar. Cubre initialize/shutdown, sincronización completa de documentos,
// completion, definition y publishDiagnostics.
//
// El transporte trabaja sobre io.Reader/io.Writer, de modo que se prueba con
// pipes y un servidor falso; el lanzamiento de servidores reales pasa por la
// interfaz Process (ver process.go), que internal/proc debe satisfacer.
//
// Las posiciones LSP cuentan unidades UTF-16; ver ByteToUTF16 y UTF16ToByte.
package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrame acota el cuerpo de un mensaje: una cabecera corrupta no debe
// provocar una reserva de memoria enorme.
const maxFrame = 64 << 20

// ResponseError es el error JSON-RPC que devuelve el servidor.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("lsp: error del servidor %d: %s", e.Code, e.Message)
}

// Códigos JSON-RPC y LSP usados.
const (
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// message es la unión de petición, respuesta y notificación.
type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

// protocolError es un mensaje mal formado pero ya consumido por completo: se
// puede seguir leyendo. Cualquier otro error de readFrame es fatal porque el
// flujo quedó desincronizado.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return "lsp: " + e.msg }

// readFrame lee un mensaje con cabeceras Content-Length.
func readFrame(br *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, errors.New("lsp: cabecera demasiado larga")
			}
			return nil, err
		}
		s := strings.TrimRight(string(line), "\r\n")
		if s == "" {
			break
		}
		name, val, ok := strings.Cut(s, ":")
		if !ok {
			continue // línea de cabecera ilegible: se ignora
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lsp: Content-Length inválido %q", strings.TrimSpace(val))
			}
			length = n
		}
	}
	if length < 0 {
		return nil, &protocolError{"mensaje sin Content-Length"}
	}
	if length > maxFrame {
		return nil, fmt.Errorf("lsp: mensaje de %d bytes excede el límite", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return body, nil
}

// encodeFrame antepone la cabecera al cuerpo para escribirlo de una vez.
func encodeFrame(body []byte) []byte {
	frame := fmt.Appendf(make([]byte, 0, len(body)+32), "Content-Length: %d\r\n\r\n", len(body))
	return append(frame, body...)
}
