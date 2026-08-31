package cdp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	webSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxFrameBytes = 32 * 1024 * 1024
)

type Version struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	UserAgent            string `json:"User-Agent"`
	V8Version            string `json:"V8-Version"`
	WebKitVersion        string `json:"WebKit-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type Target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type ProtocolError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("CDP error %d: %s", e.Code, e.Message)
}

type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ProtocolError  `json:"error,omitempty"`
}

func HTTPBase(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

func Origin(port int) string {
	return HTTPBase(port)
}

func GetVersion(ctx context.Context, port int) (Version, error) {
	var result Version
	if err := getJSON(ctx, HTTPBase(port)+"/json/version", &result); err != nil {
		return Version{}, err
	}
	if result.WebSocketDebuggerURL == "" {
		return Version{}, fmt.Errorf("CDP version response has no browser WebSocket URL")
	}
	return result, nil
}

func GetTargets(ctx context.Context, port int) ([]Target, error) {
	var result []Target
	if err := getJSON(ctx, HTTPBase(port)+"/json/list", &result); err != nil {
		return nil, err
	}
	return result, nil
}

func getJSON(ctx context.Context, endpoint string, out interface{}) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", endpoint, response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxFrameBytes))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func Call(ctx context.Context, webSocketURL, origin, method string, params interface{}) (json.RawMessage, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("CDP method is required")
	}
	conn, reader, err := dialWebSocket(ctx, webSocketURL, origin)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := map[string]interface{}{"id": int64(1), "method": method}
	if params != nil {
		request["params"] = params
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(conn, 0x1, payload); err != nil {
		return nil, fmt.Errorf("send CDP request: %w", err)
	}
	for {
		opcode, message, err := readMessage(reader, conn)
		if err != nil {
			return nil, fmt.Errorf("read CDP response: %w", err)
		}
		if opcode != 0x1 {
			continue
		}
		var decoded response
		if json.Unmarshal(message, &decoded) != nil || decoded.ID != 1 {
			continue
		}
		if decoded.Error != nil {
			return nil, decoded.Error
		}
		if len(decoded.Result) == 0 {
			return json.RawMessage(`{}`), nil
		}
		return decoded.Result, nil
	}
}

func dialWebSocket(ctx context.Context, rawURL, origin string) (net.Conn, *bufio.Reader, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if parsed.Scheme != "ws" {
		return nil, nil, fmt.Errorf("unsupported WebSocket scheme %q", parsed.Scheme)
	}
	hostname := parsed.Hostname()
	if hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1" {
		return nil, nil, fmt.Errorf("refusing non-loopback CDP endpoint %q", parsed.Host)
	}
	address := parsed.Host
	if parsed.Port() == "" {
		address = net.JoinHostPort(hostname, "80")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := parsed.RequestURI()
	if path == "" {
		path = "/"
	}
	request := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: parsed.Path, RawQuery: parsed.RawQuery},
		Host:   parsed.Host,
		Header: http.Header{
			"Upgrade":               {"websocket"},
			"Connection":            {"Upgrade"},
			"Sec-Websocket-Key":     {key},
			"Sec-Websocket-Version": {"13"},
		},
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.URL.Path = path
	request.URL.RawQuery = ""
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n", path, parsed.Host); err != nil {
		conn.Close()
		return nil, nil, err
	}
	for name, values := range request.Header {
		for _, value := range values {
			if _, err := fmt.Fprintf(conn, "%s: %s\r\n", name, value); err != nil {
				conn.Close()
				return nil, nil, err
			}
		}
	}
	if _, err := io.WriteString(conn, "\r\n"); err != nil {
		conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	httpResponse, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if httpResponse.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 4096))
		httpResponse.Body.Close()
		conn.Close()
		return nil, nil, fmt.Errorf("WebSocket upgrade rejected: %s: %s", httpResponse.Status, strings.TrimSpace(string(body)))
	}
	expected := sha1.Sum([]byte(key + webSocketGUID))
	wantAccept := base64.StdEncoding.EncodeToString(expected[:])
	if httpResponse.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		conn.Close()
		return nil, nil, fmt.Errorf("invalid WebSocket accept header")
	}
	return conn, reader, nil
}

func writeFrame(writer io.Writer, opcode byte, payload []byte) error {
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("WebSocket payload exceeds %d bytes", maxFrameBytes)
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 0x80|127)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
		header = append(header, size[:]...)
	}
	header = append(header, mask...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(masked)
	return err
}

func readMessage(reader *bufio.Reader, writer io.Writer) (byte, []byte, error) {
	var opcode byte
	var message []byte
	for {
		fin, frameOpcode, payload, err := readFrame(reader)
		if err != nil {
			return 0, nil, err
		}
		switch frameOpcode {
		case 0x8:
			return 0, nil, io.EOF
		case 0x9:
			if err := writeFrame(writer, 0xA, payload); err != nil {
				return 0, nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1, 0x2:
			opcode = frameOpcode
			message = append(message[:0], payload...)
		case 0x0:
			if opcode == 0 {
				return 0, nil, errors.New("unexpected WebSocket continuation frame")
			}
			message = append(message, payload...)
		default:
			continue
		}
		if len(message) > maxFrameBytes {
			return 0, nil, fmt.Errorf("WebSocket message exceeds %d bytes", maxFrameBytes)
		}
		if fin {
			return opcode, message, nil
		}
	}
}

func readFrame(reader io.Reader) (bool, byte, []byte, error) {
	var first [2]byte
	if _, err := io.ReadFull(reader, first[:]); err != nil {
		return false, 0, nil, err
	}
	fin := first[0]&0x80 != 0
	opcode := first[0] & 0x0f
	masked := first[1]&0x80 != 0
	length := uint64(first[1] & 0x7f)
	switch length {
	case 126:
		var size [2]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(size[:]))
	case 127:
		var size [8]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(size[:])
	}
	if length > maxFrameBytes {
		return false, 0, nil, fmt.Errorf("WebSocket frame exceeds %d bytes", maxFrameBytes)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}
