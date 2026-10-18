package lsp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func frameReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

func TestReadFrame(t *testing.T) {
	body := `{"jsonrpc":"2.0"}`
	good := "Content-Length: " + "17" + "\r\n\r\n" + body
	if len(body) != 17 {
		t.Fatalf("len=%d", len(body))
	}
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string // "" ok, "proto" protocolError, "fatal" otro
	}{
		{"normal", good, body, ""},
		{"minúsculas y espacios", "content-length:  17 \r\n\r\n" + body, body, ""},
		{"con Content-Type", "Content-Length: 17\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n" + body, body, ""},
		{"solo LF", "Content-Length: 17\n\n" + body, body, ""},
		{"línea sin dos puntos", "basura\r\nContent-Length: 17\r\n\r\n" + body, body, ""},
		{"sin Content-Length", "X-Foo: 1\r\n\r\n", "", "proto"},
		{"longitud no numérica", "Content-Length: abc\r\n\r\n", "", "fatal"},
		{"longitud negativa", "Content-Length: -4\r\n\r\n", "", "fatal"},
		{"longitud enorme", "Content-Length: 99999999999\r\n\r\n", "", "fatal"},
		{"cuerpo truncado", "Content-Length: 50\r\n\r\n{}", "", "fatal"},
		{"EOF en cabecera", "Content-Len", "", "fatal"},
		{"vacío", "", "", "fatal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readFrame(frameReader(tt.in))
			var pe *protocolError
			switch tt.wantErr {
			case "":
				if err != nil || string(got) != tt.want {
					t.Fatalf("got %q, %v", got, err)
				}
			case "proto":
				if !errors.As(err, &pe) {
					t.Fatalf("err=%v, quería protocolError", err)
				}
			default:
				if err == nil || errors.As(err, &pe) {
					t.Fatalf("err=%v, quería error fatal", err)
				}
			}
		})
	}
}

func TestReadFrameHeaderTooLong(t *testing.T) {
	_, err := readFrame(frameReader("X: " + strings.Repeat("a", 100000) + "\r\n\r\n"))
	if err == nil {
		t.Fatal("quería error por cabecera larga")
	}
}

func TestReadFrameTruncatedIsUnexpectedEOF(t *testing.T) {
	_, err := readFrame(frameReader("Content-Length: 10\r\n\r\n"))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v", err)
	}
}

func TestEncodeFrameRoundTrip(t *testing.T) {
	body := []byte(`{"a":"ñ€😀"}`)
	got, err := readFrame(bufio.NewReader(strings.NewReader(string(encodeFrame(body)))))
	if err != nil || string(got) != string(body) {
		t.Fatalf("got %q, %v", got, err)
	}
}
