package store

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/scope"
)

const AndroidFileName = "android.json"

// AndroidRequest represents a captured Android HTTP request
type AndroidRequest struct {
	ID              string              `json:"id"`
	Method          string              `json:"method"`
	URL             string              `json:"url"`
	Headers         HeaderMap           `json:"headers,omitempty"`
	ReqHeaders      map[string][]string `json:"req_headers,omitempty"`
	ResHeaders      map[string][]string `json:"res_headers,omitempty"`
	Body            string              `json:"body,omitempty"`
	ReqBody         string              `json:"req_body,omitempty"`
	ResBody         string              `json:"res_body,omitempty"`
	ReqBodyEncoding string              `json:"req_body_encoding,omitempty"`
	ResBodyEncoding string              `json:"res_body_encoding,omitempty"`
	Response        *Response           `json:"response,omitempty"`
	Status          int                 `json:"status,omitempty"`
	Timestamp       int64               `json:"timestamp"`
	Package         string              `json:"package"`
	AppName         string              `json:"app_name,omitempty"`
	UID             int                 `json:"uid,omitempty"`
	BodyEncoding    string              `json:"body_encoding,omitempty"`
	Domain          string              `json:"domain,omitempty"`
	Path            string              `json:"path,omitempty"`
	Type            string              `json:"type,omitempty"`
	HasAuth         bool                `json:"has_auth,omitempty"`
	Skipped         bool                `json:"skipped,omitempty"`
}

// GetResBody returns the response body, decompressing if needed
func (r *AndroidRequest) GetResBody() string {
	if r.ResBodyEncoding == "" || !strings.HasPrefix(r.ResBodyEncoding, "gzip:") {
		return r.ResBody
	}
	// Decompress gzip+base64
	decoded, err := base64.StdEncoding.DecodeString(r.ResBody)
	if err != nil {
		return r.ResBody
	}
	reader, err := gzip.NewReader(bytes.NewReader(decoded))
	if err != nil {
		return r.ResBody
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return r.ResBody
	}
	return string(decompressed)
}

// GetReqBody returns the request body, decompressing if needed
func (r *AndroidRequest) GetReqBody() string {
	if r.ReqBodyEncoding == "" || !strings.HasPrefix(r.ReqBodyEncoding, "gzip:") {
		return r.ReqBody
	}
	decoded, err := base64.StdEncoding.DecodeString(r.ReqBody)
	if err != nil {
		return r.ReqBody
	}
	reader, err := gzip.NewReader(bytes.NewReader(decoded))
	if err != nil {
		return r.ReqBody
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return r.ReqBody
	}
	return string(decompressed)
}

// AndroidPackage groups requests by app
type AndroidPackage struct {
	Package  string           `json:"package"`
	AppName  string           `json:"app_name,omitempty"`
	Requests []AndroidRequest `json:"requests"`
	Domains  []string         `json:"domains,omitempty"`
	LastSeen int64            `json:"last_seen"`
}

// AndroidData is the android.json format
type AndroidData struct {
	Version    string                     `json:"version"`
	ExportedAt string                     `json:"exported_at"`
	DeviceID   string                     `json:"device_id,omitempty"`
	Packages   map[string]*AndroidPackage `json:"packages"`
	mu         sync.Mutex
}

var androidData *AndroidData

// GetAndroidFilePath returns path to android.json
func GetAndroidFilePath() (string, error) {
	if override := os.Getenv("REPANDROID_PATH"); override != "" {
		selected, err := scope.Current()
		if err != nil {
			return "", err
		}
		if selected.Scoped {
			return "", fmt.Errorf("REPANDROID_PATH cannot override a scoped store; unset REPANDROID_PATH or explicitly use --global")
		}
		return expandHomePath(override)
	}
	storePath, err := GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(storePath, AndroidFileName), nil
}

// LoadAndroidData loads android.json (fresh each time)
func LoadAndroidData() (*AndroidData, error) {
	filePath, err := GetAndroidFilePath()
	if err != nil {
		return nil, err
	}

	result := &AndroidData{
		Version:  "1.0",
		Packages: make(map[string]*AndroidPackage),
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	if err := sonic.Unmarshal(data, result); err != nil {
		return result, nil // Start fresh on parse error
	}

	if result.Packages == nil {
		result.Packages = make(map[string]*AndroidPackage)
	}
	return result, nil
}

// AddAndroidRequest adds a request to the appropriate package
func (a *AndroidData) AddAndroidRequest(req AndroidRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pkg := req.Package
	if pkg == "" {
		pkg = "unknown"
	}

	if a.Packages[pkg] == nil {
		a.Packages[pkg] = &AndroidPackage{
			Package:  pkg,
			Requests: []AndroidRequest{},
		}
	}

	p := a.Packages[pkg]
	if req.AppName != "" && p.AppName == "" {
		p.AppName = req.AppName
	}
	p.Requests = append(p.Requests, req)
	p.LastSeen = time.Now().UnixMilli()

	// Track unique domains
	if req.Domain != "" {
		found := false
		for _, d := range p.Domains {
			if d == req.Domain {
				found = true
				break
			}
		}
		if !found {
			p.Domains = append(p.Domains, req.Domain)
		}
	}
}

// Save writes android.json to disk
func (a *AndroidData) Save() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	filePath, err := GetAndroidFilePath()
	if err != nil {
		return err
	}

	a.ExportedAt = time.Now().Format(time.RFC3339)
	data, err := sonic.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

// GetPackageRequests returns requests for a specific package
func (a *AndroidData) GetPackageRequests(pkg string) []AndroidRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.Packages[pkg]; ok {
		return p.Requests
	}
	return nil
}

// GetAllRequests returns all requests across packages
func (a *AndroidData) GetAllRequests() []AndroidRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var all []AndroidRequest
	for _, p := range a.Packages {
		all = append(all, p.Requests...)
	}
	return all
}

// PackageStats returns summary stats
func (a *AndroidData) PackageStats() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()

	stats := make(map[string]int)
	for pkg, p := range a.Packages {
		stats[pkg] = len(p.Requests)
	}
	return stats
}
