package commands

import (
	"io"
	"net/http"

	"os"

	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func replayOriginalFixture(body []byte) *store.Request {
	return &store.Request{
		ID: "fixture", Method: http.MethodGet,
		Response:            &store.Response{Status: 200, Body: string(body)},
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))},
	}
}

func replayCaptureOutput(t *testing.T, run func() error) []byte {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
