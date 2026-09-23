package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// EncodeExport writes one request at a time. This avoids a second serialized
// copy of every body in the capture; the largest request remains the encoding
// allocation unit. The caller controls atomic replacement and durability.
func EncodeExport(writer io.Writer, export Export) error {
	type exportMetadata Export
	header, err := json.Marshal(struct {
		*exportMetadata
		Requests []Request `json:"requests,omitempty"`
	}{exportMetadata: (*exportMetadata)(&export)})
	if err != nil {
		return err
	}
	buffer := bufio.NewWriterSize(writer, 64<<10)
	if _, err = buffer.Write(header[:len(header)-1]); err != nil {
		return err
	}
	if _, err = buffer.WriteString(`,"requests":`); err != nil {
		return err
	}
	if export.Requests == nil {
		if _, err = buffer.WriteString("null"); err != nil {
			return err
		}
	} else {
		if err = buffer.WriteByte('['); err != nil {
			return err
		}
		for index := range export.Requests {
			if index > 0 {
				if err = buffer.WriteByte(','); err != nil {
					return err
				}
			}
			request, err := json.Marshal(&export.Requests[index])
			if err != nil {
				return err
			}
			if _, err = buffer.Write(request); err != nil {
				return err
			}
		}
		if err = buffer.WriteByte(']'); err != nil {
			return err
		}
	}
	if _, err = buffer.WriteString("}\n"); err != nil {
		return err
	}
	return buffer.Flush()
}

// DecodeExport consumes exactly one complete JSON object, including trailing
// whitespace through EOF. It decodes requests separately to avoid buffering an
// entire capture inside encoding/json. No partial export is returned on error.
func DecodeExport(reader io.Reader) (Export, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	export, err := decodeExportObject(decoder)
	if err != nil {
		return Export{}, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		if err == nil {
			return Export{}, fmt.Errorf("export contains trailing JSON")
		}
		return Export{}, fmt.Errorf("export contains invalid trailing data: %w", err)
	}
	return export, nil
}

func decodeExportObject(decoder *json.Decoder) (Export, error) {
	var export Export
	token, err := decoder.Token()
	if err != nil {
		return export, err
	}
	if token != json.Delim('{') {
		return export, fmt.Errorf("capture export must be an object")
	}
	value := reflect.ValueOf(&export).Elem()
	kind := value.Type()
	fields := make(map[string]int, kind.NumField())
	for index := 0; index < kind.NumField(); index++ {
		fields[strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]] = index
	}
	seen := make(map[string]bool)
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return export, err
		}
		name, ok := field.(string)
		if !ok {
			return export, fmt.Errorf("invalid export field name")
		}
		if seen[name] {
			return export, fmt.Errorf("capture export contains duplicate field %q", name)
		}
		seen[name] = true
		if name == "requests" {
			token, err = decoder.Token()
			if err != nil {
				return export, err
			}
			if token == nil {
				continue
			}
			if token != json.Delim('[') {
				return export, fmt.Errorf("capture requests must be an array")
			}
			export.Requests = []Request{}
			for decoder.More() {
				var request Request
				if err = decoder.Decode(&request); err != nil {
					return export, err
				}
				export.Requests = append(export.Requests, request)
			}
			if _, err = decoder.Token(); err != nil {
				return export, err
			}
		} else if index, ok := fields[name]; ok {
			if err = decoder.Decode(value.Field(index).Addr().Interface()); err != nil {
				return export, err
			}
		} else if err = skipExportValue(decoder, 0); err != nil {
			return export, err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return export, err
	}
	return export, nil
}

// Unknown future metadata is skipped by tokens, without materializing a large
// subtree. Current metadata retains the ordinary typed struct representation.
func skipExportValue(decoder *json.Decoder, depth int) error {
	if depth > 1000 {
		return fmt.Errorf("unknown export metadata nesting exceeds 1000 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected export metadata delimiter")
	}
	for decoder.More() {
		if delim == '{' {
			if _, err = decoder.Token(); err != nil {
				return err
			}
		}
		if err = skipExportValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
