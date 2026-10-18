package dap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrame acota el cuerpo de un mensaje: una cabecera corrupta no debe
// provocar una reserva de memoria enorme.
const maxFrame = 64 << 20

// protocolError es un mensaje mal formado que no desincroniza el flujo: se
// puede seguir leyendo. Cualquier otro error de readFrame es fatal.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return "dap: " + e.msg }

// readFrame lee un mensaje con cabeceras Content-Length (el mismo
// encuadre que LSP).
func readFrame(br *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, errors.New("dap: cabecera demasiado larga")
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
				return nil, fmt.Errorf("dap: Content-Length inválido %q", strings.TrimSpace(val))
			}
			length = n
		}
	}
	if length < 0 {
		return nil, &protocolError{"mensaje sin Content-Length"}
	}
	if length > maxFrame {
		return nil, fmt.Errorf("dap: mensaje de %d bytes excede el límite", length)
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
