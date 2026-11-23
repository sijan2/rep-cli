package scene

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Ref is a parsed handle. Kind "n" names a DOM node (Frame6 + Backend); kind
// "r" names a record by its join key.
type Ref struct {
	Kind    string
	Frame6  string
	Backend int
	Key     string
}

// Locator says how to find a record's container in a live document. Exactly
// one of Attr/Value (element carrying attribute Attr equal to Value), Href
// (anchor wrapping <time> whose href contains Href) or bare Value (any
// attribute equal to Value) is meaningful.
type Locator struct {
	Attr  string
	Value string
	Href  string
}

var (
	frame6Pattern = regexp.MustCompile(`^[0-9a-f]{6}$`)
	nsPattern     = regexp.MustCompile(`^[a-z0-9]+$`)
	// Reddit fullnames: the type prefix is part of the key the DOM carries.
	redditFullname = regexp.MustCompile(`^t[1-6]_[A-Za-z0-9]+$`)
	statusID       = regexp.MustCompile(`^[0-9]{6,}$`)
)

const maxHandleBytes = 256

// Frame6 identifies a document: the same frame after a navigation gets a new
// loaderId, so node handles from the old document stop resolving.
func Frame6(frameID, loaderID string) string {
	sum := sha256.Sum256([]byte(frameID + "|" + loaderID))
	return hex.EncodeToString(sum[:])[:6]
}

// NodeHandle formats n_<frame6>.<backendNodeId>.
func NodeHandle(frame6 string, backend int) string {
	return "n_" + frame6 + "." + strconv.Itoa(backend)
}

// RecordHandle formats r_<ns>_<key>. Reddit passes ns "" because its keys are
// fullnames that already carry the namespace (t1_abc), which keeps handles
// byte-identical to records.Record.R from the network decoder.
func RecordHandle(ns, key string) string {
	if ns == "" {
		return "r_" + key
	}
	return "r_" + ns + "_" + key
}

// ParseHandle accepts n_<frame6>.<backendNodeId> and r_<ns>_<key>. For record
// handles Key is the join key as the DOM and records.Record.DOMKey carry it:
// a Reddit fullname keeps its t1_ prefix, every other namespace is stripped.
func ParseHandle(s string) (Ref, error) {
	if len(s) > maxHandleBytes || strings.ContainsAny(s, " \t\r\n") {
		return Ref{}, errors.New("scene: malformed handle")
	}
	switch {
	case strings.HasPrefix(s, "n_"):
		rest := s[2:]
		dot := strings.IndexByte(rest, '.')
		if dot < 0 {
			return Ref{}, fmt.Errorf("scene: node handle %q lacks .backendNodeId", s)
		}
		frame6, id := rest[:dot], rest[dot+1:]
		if !frame6Pattern.MatchString(frame6) {
			return Ref{}, fmt.Errorf("scene: node handle %q has a bad frame id", s)
		}
		backend, err := strconv.Atoi(id)
		if err != nil || backend <= 0 || strings.HasPrefix(id, "+") || strings.HasPrefix(id, "0") {
			return Ref{}, fmt.Errorf("scene: node handle %q has a bad backendNodeId", s)
		}
		return Ref{Kind: "n", Frame6: frame6, Backend: backend}, nil
	case strings.HasPrefix(s, "r_"):
		rest := s[2:]
		sep := strings.IndexByte(rest, '_')
		if sep <= 0 || sep == len(rest)-1 {
			return Ref{}, fmt.Errorf("scene: record handle %q lacks a namespace or key", s)
		}
		ns := rest[:sep]
		if !nsPattern.MatchString(ns) {
			return Ref{}, fmt.Errorf("scene: record handle %q has a bad namespace", s)
		}
		if redditFullname.MatchString(rest) {
			return Ref{Kind: "r", Key: rest}, nil
		}
		return Ref{Kind: "r", Key: rest[sep+1:]}, nil
	}
	return Ref{}, fmt.Errorf("scene: handle %q must start with n_ or r_", s)
}

// Locate turns a record handle or a bare join key into the DOM test act uses
// to confirm a control still belongs to its record, or to reveal the record.
func Locate(handleOrKey string) (Locator, error) {
	key := handleOrKey
	ns := ""
	if strings.HasPrefix(handleOrKey, "r_") {
		ref, err := ParseHandle(handleOrKey)
		if err != nil {
			return Locator{}, err
		}
		key = ref.Key
		rest := handleOrKey[2:]
		if sep := strings.IndexByte(rest, '_'); sep > 0 && !redditFullname.MatchString(rest) {
			ns = rest[:sep]
		}
	}
	if key == "" || len(key) > maxHandleBytes || strings.ContainsAny(key, " \t\r\n\"'<>\\") {
		return Locator{}, errors.New("scene: malformed record key")
	}
	switch {
	case redditFullname.MatchString(key):
		return Locator{Attr: "thingid", Value: key}, nil
	case ns == "tw" || (ns == "" && statusID.MatchString(key)):
		return Locator{Href: "/status/" + key}, nil
	}
	return Locator{Value: key}, nil
}
