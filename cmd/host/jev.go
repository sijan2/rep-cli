package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/repplus/rep-cli/internal/jev"
)

var jevSlots = make(chan struct{}, 4)

// Classifications run outside the capture read loop and live-data mutex so a
// slow cloud evaluation never delays captures or browser RPC responses.
func dispatchJev(ctx context.Context, message *Message, send func(interface{}) error) bool {
	if message == nil || message.Action != "jev_classify" {
		return false
	}
	response := map[string]interface{}{"action": "jev_classify", "id": message.ID, "success": false}
	if message.ID == "" || len(message.ID) > 128 || len(message.Params) > 32768 {
		response["error"] = "invalid Jev classification request"
		_ = send(response)
		return true
	}
	var input jev.TrafficInput
	decoder := json.NewDecoder(bytes.NewReader(message.Params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		response["error"] = "invalid Jev classification metadata"
		_ = send(response)
		return true
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		response["error"] = "invalid Jev classification metadata"
		_ = send(response)
		return true
	}
	select {
	case jevSlots <- struct{}{}:
	default:
		response["error"] = "Jev is busy; try again after the current decisions finish"
		_ = send(response)
		return true
	}
	go func() {
		defer func() { <-jevSlots }()
		config, err := jev.LoadConfig()
		if err != nil {
			response["error"] = err.Error()
			_ = send(response)
			return
		}
		result, err := jev.NewClient(config).Classify(ctx, input)
		if err != nil {
			response["error"] = err.Error()
		} else {
			response["success"] = true
			response["result"] = result
		}
		_ = send(response)
	}()
	return true
}
