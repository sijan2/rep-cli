package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
)

// One physical JSONL record, serialized one request at a time. The outer
// override hides the original requests field while preserving future metadata.
func encodeSessionEntry(writer io.Writer, session *Session) error {
	type sessionMetadata Session
	header, err := json.Marshal(struct {
		*sessionMetadata
		Requests []Request `json:"requests,omitempty"`
	}{sessionMetadata: (*sessionMetadata)(session)})
	if err != nil {
		return err
	}
	if _, err = io.WriteString(writer, `{"action":"session","session":`); err != nil {
		return err
	}
	if _, err = writer.Write(header[:len(header)-1]); err != nil {
		return err
	}
	if _, err = io.WriteString(writer, `,"requests":[`); err != nil {
		return err
	}
	for index := range session.Requests {
		if index > 0 {
			if _, err = io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		var body []byte
		body, err = json.Marshal(&session.Requests[index])
		if err != nil {
			return err
		}
		if _, err = writer.Write(body); err != nil {
			return err
		}
	}
	_, err = io.WriteString(writer, "]}}\n")
	return err
}

func readSessionEntries(path string, handle func(sessionLogEntry) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	decoder := json.NewDecoder(file)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if token != json.Delim('{') {
			return fmt.Errorf("session log entry must be an object")
		}
		var entry sessionLogEntry
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			switch key {
			case "action":
				err = decoder.Decode(&entry.Action)
			case "timestamp":
				err = decoder.Decode(&entry.Timestamp)
			case "session":
				entry.Session, err = decodeSessionStream(decoder)
			default:
				var ignored json.RawMessage
				err = decoder.Decode(&ignored)
			}
			if err != nil {
				return err
			}
		}
		if _, err = decoder.Token(); err != nil {
			return err
		}
		if err = handle(entry); err != nil {
			return err
		}
	}
}

func decodeSessionStream(decoder *json.Decoder) (*Session, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("session must be an object")
	}
	session := &Session{}
	value := reflect.ValueOf(session).Elem()
	kind := value.Type()
	fields := make(map[string]int, kind.NumField())
	for index := 0; index < kind.NumField(); index++ {
		fields[strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]] = index
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if key == "requests" {
			token, err = decoder.Token()
			if err != nil {
				return nil, err
			}
			if token == nil {
				continue
			}
			if token != json.Delim('[') {
				return nil, fmt.Errorf("session requests must be an array")
			}
			for decoder.More() {
				var request Request
				if err = decoder.Decode(&request); err != nil {
					return nil, err
				}
				session.Requests = append(session.Requests, request)
			}
			if _, err = decoder.Token(); err != nil {
				return nil, err
			}
		} else if index, ok := fields[key.(string)]; ok {
			if err = decoder.Decode(value.Field(index).Addr().Interface()); err != nil {
				return nil, err
			}
		} else {
			var ignored json.RawMessage
			if err = decoder.Decode(&ignored); err != nil {
				return nil, err
			}
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	return session, nil
}
