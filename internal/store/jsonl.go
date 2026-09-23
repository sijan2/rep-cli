package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func appendJSONL(path string, v interface{}) error {
	return appendJSONLStream(path, func(writer io.Writer) error { return json.NewEncoder(writer).Encode(v) })
}

// Stage complete entries before taking the append lock. Readers and writers
// coordinate on the log descriptor; errors roll back this append's bytes.
func appendJSONLStream(path string, encode func(io.Writer) error) error {
	if err := EnsureStoreDir(); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".rep-session-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = staged.Close(); _ = os.Remove(staged.Name()) }()
	buffer := bufio.NewWriterSize(staged, 64<<10)
	if err = encode(buffer); err != nil {
		return err
	}
	if err = buffer.Flush(); err != nil {
		return err
	}
	if _, err = staged.Seek(0, io.SeekStart); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err = io.Copy(file, staged); err == nil {
		err = file.Sync()
	}
	if err != nil {
		_ = file.Truncate(offset)
		_ = file.Sync()
	}
	return err
}

func readJSONLLines(path string, handle func(line []byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				if err := handle(line); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
