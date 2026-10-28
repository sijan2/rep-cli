package retention

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/bytedance/sonic"
)

const gcMaxValueBytes = 64 << 20

// Sessions can be a gigabyte on one physical line. Frame requests individually
// so memory grows with request metadata, rather than archived payload bytes.
type gcJSONReader struct {
	reader *bufio.Reader
	offset int64
}

func gcNewReader(reader io.Reader) *gcJSONReader {
	return &gcJSONReader{reader: bufio.NewReaderSize(reader, 64<<10)}
}

func (reader *gcJSONReader) gcPeek() (byte, error) {
	value, err := reader.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (reader *gcJSONReader) gcByte() (byte, error) {
	value, err := reader.reader.ReadByte()
	if err == nil {
		reader.offset++
	}
	return value, err
}

func (reader *gcJSONReader) gcSpace() error {
	for {
		value, err := reader.gcPeek()
		if err != nil {
			return err
		}
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return nil
		}
		if _, err := reader.gcByte(); err != nil {
			return err
		}
	}
}

func (reader *gcJSONReader) gcExpect(expected byte) error {
	if err := reader.gcSpace(); err != nil {
		return err
	}
	value, err := reader.gcByte()
	if err != nil {
		return err
	}
	if value != expected {
		return fmt.Errorf("invalid JSON at byte %d: expected %q", reader.offset, expected)
	}
	return nil
}

func (reader *gcJSONReader) gcRaw() ([]byte, error) {
	if err := reader.gcSpace(); err != nil {
		return nil, err
	}
	var raw bytes.Buffer
	depth, quoted, escaped := 0, false, false
	for {
		value, err := reader.gcPeek()
		if err == io.EOF && raw.Len() > 0 && !quoted && depth == 0 {
			break
		}
		if err != nil {
			return nil, err
		}
		if raw.Len() > 0 && !quoted && depth == 0 && (value == ',' || value == ']' || value == '}' || value == ' ' || value == '\t' || value == '\r' || value == '\n') {
			break
		}
		if raw.Len() >= gcMaxValueBytes {
			return nil, fmt.Errorf("individual JSON value exceeds %d bytes; file left untouched", gcMaxValueBytes)
		}
		if _, err = reader.gcByte(); err != nil {
			return nil, err
		}
		raw.WriteByte(value)
		if quoted {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				quoted = false
				if depth == 0 {
					break
				}
			}
			continue
		}
		switch value {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return reader.gcValid(raw.Bytes())
			}
		}
	}
	return reader.gcValid(raw.Bytes())
}

func (reader *gcJSONReader) gcValid(raw []byte) ([]byte, error) {
	if !sonic.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON ending at byte %d", reader.offset)
	}
	return raw, nil
}

func (reader *gcJSONReader) gcObject(handle func(string) error) error {
	if err := reader.gcExpect('{'); err != nil {
		return err
	}
	seen := map[string]bool{}
	if err := reader.gcSpace(); err != nil {
		return err
	}
	if value, _ := reader.gcPeek(); value == '}' {
		_, err := reader.gcByte()
		return err
	}
	for {
		raw, err := reader.gcRaw()
		if err != nil {
			return err
		}
		var key string
		if err := sonic.Unmarshal(raw, &key); err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("duplicate JSON key %q; file left untouched", key)
		}
		seen[key] = true
		if err := reader.gcExpect(':'); err != nil {
			return err
		}
		if err := handle(key); err != nil {
			return err
		}
		if err := reader.gcSpace(); err != nil {
			return err
		}
		value, err := reader.gcByte()
		if err != nil {
			return err
		}
		if value == '}' {
			return nil
		}
		if value != ',' {
			return fmt.Errorf("invalid object separator at byte %d", reader.offset)
		}
	}
}

func (reader *gcJSONReader) gcArray(handle func() error) error {
	if err := reader.gcSpace(); err != nil {
		return err
	}
	if value, _ := reader.gcPeek(); value == 'n' {
		raw, err := reader.gcRaw()
		if err == nil && string(raw) != "null" {
			err = fmt.Errorf("expected array or null")
		}
		return err
	}
	if err := reader.gcExpect('['); err != nil {
		return err
	}
	if err := reader.gcSpace(); err != nil {
		return err
	}
	if value, _ := reader.gcPeek(); value == ']' {
		_, err := reader.gcByte()
		return err
	}
	for {
		if err := handle(); err != nil {
			return err
		}
		if err := reader.gcSpace(); err != nil {
			return err
		}
		value, err := reader.gcByte()
		if err != nil {
			return err
		}
		if value == ']' {
			return nil
		}
		if value != ',' {
			return fmt.Errorf("invalid array separator at byte %d", reader.offset)
		}
	}
}
