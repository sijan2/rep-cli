//go:build replab

package commands

import (
	"encoding/json"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

const labBuild = true

func findLabAndroidRequest(id string) *store.AndroidRequest {
	selected, err := scope.Current()
	if err != nil || selected.Scoped {
		return nil
	}
	data, err := store.LoadAndroidData()
	if err != nil {
		return nil
	}
	for _, pkg := range data.Packages {
		for i := range pkg.Requests {
			if pkg.Requests[i].ID == id {
				return &pkg.Requests[i]
			}
		}
	}
	return nil
}

func findLabBodyRequest(id string) *store.Request {
	ar := findLabAndroidRequest(id)
	if ar == nil {
		return nil
	}
	req := &store.Request{ID: ar.ID, Method: ar.Method, URL: ar.URL, Headers: ar.Headers, Body: ar.GetReqBody(), Response: &store.Response{Status: ar.Status, Body: ar.GetResBody()}}
	store.ComputeRequestFields(req)
	return req
}

func findLabRequest(id string) map[string]interface{} {
	ar := findLabAndroidRequest(id)
	if ar == nil {
		return nil
	}
	data, err := json.Marshal(ar)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}
