package commands

import (
	"os"

	"github.com/repplus/rep-cli/internal/store"
)

func loadLiveExport(livePath string) (store.Export, error) {
	var export store.Export
	file, err := os.Open(livePath)
	if err != nil {
		return export, err
	}
	defer file.Close()
	return store.DecodeExport(file)
}
