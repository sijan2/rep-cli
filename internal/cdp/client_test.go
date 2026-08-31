package cdp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCallWebSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			serverErr <- err
			return
		}
		if request.Header.Get("Origin") != "http://127.0.0.1:test" {
			serverErr <- fmt.Errorf("unexpected origin %q", request.Header.Get("Origin"))
			return
		}
		digest := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + webSocketGUID))
		accept := base64.StdEncoding.EncodeToString(digest[:])
		if _, err := fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
			serverErr <- err
			return
		}
		_, opcode, payload, err := readFrame(reader)
		if err != nil {
			serverErr <- err
			return
		}
		if opcode != 0x1 {
			serverErr <- fmt.Errorf("unexpected opcode %d", opcode)
			return
		}
		var command struct {
			ID     int64                  `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.Unmarshal(payload, &command); err != nil {
			serverErr <- err
			return
		}
		if command.ID != 1 || command.Method != "Browser.getVersion" {
			serverErr <- fmt.Errorf("unexpected command %+v", command)
			return
		}
		if err := writeServerFrame(conn, []byte(`{"method":"Target.targetCreated","params":{}}`)); err != nil {
			serverErr <- err
			return
		}
		if err := writeServerFrame(conn, []byte(`{"id":1,"result":{"product":"Chrome/test"}}`)); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := Call(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/test", "http://127.0.0.1:test", "Browser.getVersion", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["product"] != "Chrome/test" {
		t.Fatalf("unexpected result %s", result)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestCallRejectsNonLoopbackEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Call(ctx, "ws://example.com/devtools/browser/test", "http://example.com", "Browser.getVersion", nil)
	if err == nil {
		t.Fatal("expected non-loopback endpoint rejection")
	}
}

func writeServerFrame(writer io.Writer, payload []byte) error {
	payload = bytes.ReplaceAll(payload, []byte{'\\', '"'}, []byte{'"'})
	header := []byte{0x81}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 127)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
		header = append(header, size[:]...)
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
