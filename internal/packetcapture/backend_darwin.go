//go:build darwin && cgo

package packetcapture

/*
#cgo LDFLAGS: -lpcap
#include <pcap/pcap.h>
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"
)

func Supported() bool        { return true }
func backendVersion() string { return C.GoString(C.pcap_lib_version()) }

func Interfaces() ([]Interface, error) {
	var head *C.pcap_if_t
	var message [C.PCAP_ERRBUF_SIZE]C.char
	if C.pcap_findalldevs(&head, &message[0]) != 0 {
		return nil, fmt.Errorf("enumerate capture interfaces: %s", C.GoString(&message[0]))
	}
	defer C.pcap_freealldevs(head)
	result := []Interface{}
	for current := head; current != nil; current = current.next {
		if len(result) >= 256 {
			return nil, fmt.Errorf("interface count exceeds bounded enumeration limit 256")
		}
		item := Interface{Name: C.GoString(current.name), Description: C.GoString(current.description),
			Loopback: current.flags&C.PCAP_IF_LOOPBACK != 0, Up: current.flags&C.PCAP_IF_UP != 0, Running: current.flags&C.PCAP_IF_RUNNING != 0}
		if len(item.Name) > 256 || len(item.Description) > 1024 {
			return nil, fmt.Errorf("interface metadata exceeds bounded enumeration limits")
		}
		result = append(result, item)
	}
	return result, nil
}

func captureNative(ctx context.Context, options Options, output *os.File) (nativeResult, error) {
	return runNative(ctx, options, output, "")
}

// replayNative is a package-private test seam for the actual native callback,
// writer, and limits. It only reads an offline file, without an interface.
func replayNative(ctx context.Context, options Options, output *os.File, input string) (nativeResult, error) {
	return runNative(ctx, options, output, input)
}

func runNative(ctx context.Context, options Options, output *os.File, input string) (nativeResult, error) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return nativeResult{}, err
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	done, cancellationDone := make(chan struct{}), make(chan struct{})
	if ctx.Err() != nil {
		_, _ = writeEnd.Write([]byte{1})
		close(cancellationDone)
	} else {
		go func() {
			defer close(cancellationDone)
			select {
			case <-ctx.Done():
				_, _ = writeEnd.Write([]byte{1})
			case <-done:
			}
		}()
	}
	defer func() { close(done); <-cancellationDone }()
	readyFD := -1
	if options.Ready != nil {
		readyRead, readyWrite, err := os.Pipe()
		if err != nil {
			return nativeResult{}, err
		}
		readyFD = int(readyWrite.Fd())
		readyDone := make(chan struct{})
		go func() {
			defer close(readyDone)
			var signal [1]byte
			if n, err := readyRead.Read(signal[:]); err == nil && n == 1 && signal[0] == 1 {
				options.Ready()
			}
		}()
		defer func() {
			_ = readyWrite.Close()
			<-readyDone
			_ = readyRead.Close()
		}()
	}
	device, filter := C.CString(options.Interface), C.CString(options.Filter)
	defer C.free(unsafe.Pointer(device))
	defer C.free(unsafe.Pointer(filter))
	settings := C.struct_rep_pcap_options{interface_name: device, filter: filter, output_fd: C.int(output.Fd()), cancel_fd: C.int(readEnd.Fd()), ready_fd: C.int(readyFD),
		snaplen: C.int(options.Snaplen), buffer_bytes: C.int(options.BufferBytes), duration_ns: C.uint64_t(options.Duration),
		max_packets: C.uint64_t(options.MaxPackets), max_bytes: C.uint64_t(options.MaxBytes)}
	var result C.struct_rep_pcap_result
	var code C.int
	if input == "" {
		code = C.rep_pcap_capture(&settings, &result)
	} else {
		path := C.CString(input)
		defer C.free(unsafe.Pointer(path))
		code = C.rep_pcap_replay(path, &settings, &result)
	}
	value := nativeResult{Link: linkType(int(result.datalink)), Packets: uint64(result.packets), CapturedBytes: uint64(result.captured_bytes),
		OriginalBytes: uint64(result.original_bytes), TruncatedPackets: uint64(result.truncated_packets), PCAPValid: result.pcap_valid != 0,
		StopReason: C.GoString(&result.stop_reason[0]), Stats: Statistics{Available: result.stats_available != 0, Received: uint64(result.stats_received),
			Dropped: uint64(result.stats_dropped), InterfaceDropped: uint64(result.stats_interface_dropped)}}
	if result.observation_started_ns != 0 {
		stamp := time.Unix(0, int64(result.observation_started_ns)).UTC()
		value.ObservationStartedAt = &stamp
	}
	if warning := C.GoString(&result.warning[0]); warning != "" {
		value.Warnings = []string{warning}
	}
	if code != 0 {
		message := C.GoString(&result.error[0])
		if result.permission_denied != 0 || strings.Contains(strings.ToLower(message), "permission denied") || strings.Contains(strings.ToLower(message), "operation not permitted") {
			return value, fmt.Errorf("%w: %s; this process cannot open the macOS BPF capture device", ErrPermission, message)
		}
		return value, fmt.Errorf("libpcap capture failed: %s", message)
	}
	return value, nil
}
