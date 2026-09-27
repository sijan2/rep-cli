//go:build darwin && cgo

#include "native.h"
#include <pcap/pcap.h>
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

struct rep_writer {
    pcap_t *capture;
    pcap_dumper_t *dump;
    FILE *file;
    const struct rep_pcap_options *options;
    struct rep_pcap_result *result;
    uint64_t deadline;
    uint64_t file_bytes;
    int stopped;
};

struct rep_output {
    int fd;
    int closed;
};

// libpcap closes the supplied FILE on some pcap_dump_fopen failures, but not
// all. A native cookie stream makes that ownership transfer observable, so a
// header failure cannot produce either a double fclose or a leaked descriptor.
static int rep_output_write(void *opaque, const char *bytes, int length) {
    struct rep_output *output = opaque;
    int written = 0;
    while (written < length) {
        ssize_t count = write(output->fd, bytes + written, (size_t)(length - written));
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) {
            if (count == 0) errno = EIO;
            return written ? written : -1;
        }
        written += (int)count;
    }
    return written;
}

static int rep_output_close(void *opaque) {
    struct rep_output *output = opaque;
    output->closed = 1;
    return close(output->fd);
}

static uint64_t rep_now(clockid_t clock) {
    struct timespec value;
    if (clock_gettime(clock, &value) != 0) return 0;
    return (uint64_t)value.tv_sec * 1000000000ULL + value.tv_nsec;
}

static void rep_stop(struct rep_writer *writer, const char *reason) {
    snprintf(writer->result->stop_reason, sizeof(writer->result->stop_reason), "%s", reason);
    writer->stopped = 1;
    pcap_breakloop(writer->capture);
}

// Runs wholly in C: one bounded stdio buffer and no Go callback or per-packet
// JSON/base64 conversion. The classic pcap record preserves caplen and wirelen.
static void rep_packet(unsigned char *opaque, const struct pcap_pkthdr *header, const unsigned char *bytes) {
    struct rep_writer *writer = (struct rep_writer *)opaque;
    struct rep_pcap_result *result = writer->result;
    if (writer->stopped) return;
    if (rep_now(CLOCK_MONOTONIC) >= writer->deadline) { rep_stop(writer, "duration_limit"); return; }
    const uint64_t needed = 16ULL + header->caplen;
    if (needed > writer->options->max_bytes - writer->file_bytes) { rep_stop(writer, "byte_limit"); return; }
    pcap_dump((unsigned char *)writer->dump, header, bytes);
    if (ferror(writer->file)) {
        snprintf(result->error, sizeof(result->error), "writing pcap failed: %s", strerror(errno));
        rep_stop(writer, "write_error");
        return;
    }
    writer->file_bytes += needed;
    result->packets++;
    result->captured_bytes += header->caplen;
    result->original_bytes += header->len;
    if (header->caplen < header->len) result->truncated_packets++;
    if (result->packets >= writer->options->max_packets) rep_stop(writer, "packet_limit");
    else if (writer->file_bytes >= writer->options->max_bytes) rep_stop(writer, "byte_limit");
}

// Both live collection and private offline tests use this writer, callback,
// cancellation check, and stop handling. Offline replay never opens an interface.
static int rep_write_capture(pcap_t *capture, const struct rep_pcap_options *options,
                             struct rep_pcap_result *result, int live) {
    result->datalink = pcap_datalink(capture);
    const int capture_fd = live ? pcap_get_selectable_fd(capture) : -1;
    if (live && capture_fd < 0) { snprintf(result->error, sizeof(result->error), "interface has no cancellable selectable capture descriptor"); return -1; }
    pcap_dumper_t *dump = NULL;
    FILE *file = NULL;
    char *output_buffer = NULL;
    int status = -1;
    struct rep_output output = {.fd=dup(options->output_fd), .closed=0};
    if (output.fd < 0) { snprintf(result->error, sizeof(result->error), "cannot duplicate output descriptor: %s", strerror(errno)); goto done; }
    file = funopen(&output, NULL, rep_output_write, NULL, rep_output_close);
    if (!file) { close(output.fd); snprintf(result->error, sizeof(result->error), "cannot open output stream: %s", strerror(errno)); goto done; }
    output_buffer = malloc(1024 * 1024);
    if (!output_buffer || setvbuf(file, output_buffer, _IOFBF, 1024 * 1024) != 0) {
        snprintf(result->error, sizeof(result->error), "cannot allocate buffered pcap writer"); goto done;
    }
    dump = pcap_dump_fopen(capture, file);
    if (!dump) {
        if (output.closed) file = NULL;
        snprintf(result->error, sizeof(result->error), "cannot write pcap header: %s", pcap_geterr(capture)); goto done;
    }
    result->pcap_valid = 1;
    result->observation_started_ns = (int64_t)rep_now(CLOCK_REALTIME);
    if (options->ready_fd >= 0) {
        if (pcap_dump_flush(dump) != 0) {
            snprintf(result->error, sizeof(result->error), "flushing initial pcap header failed: %s", strerror(errno)); goto done;
        }
        const unsigned char ready = 1;
        ssize_t written;
        do { written = write(options->ready_fd, &ready, 1); } while (written < 0 && errno == EINTR);
        if (written != 1) {
            snprintf(result->error, sizeof(result->error), "capture readiness notification failed: %s", strerror(errno)); goto done;
        }
    }
    struct rep_writer writer = {.capture=capture, .dump=dump, .file=file, .options=options, .result=result,
        .deadline=rep_now(CLOCK_MONOTONIC)+options->duration_ns, .file_bytes=24};
    struct pollfd descriptors[2] = {{.fd=capture_fd, .events=POLLIN}, {.fd=options->cancel_fd, .events=POLLIN}};
    status = 0;
    // The first nonblocking read is immediate. Live capture and offline replay
    // then share the same full-batch drain decision, which lets the synthetic
    // burst fixture detect a poll-before-drain regression without BPF access.
    int drain = 1;
    if (options->max_bytes == 24) rep_stop(&writer, "byte_limit");
    while (!writer.stopped) {
        uint64_t now = rep_now(CLOCK_MONOTONIC);
        if (now >= writer.deadline) { rep_stop(&writer, "duration_limit"); break; }
        int timeout = drain ? 0 : (int)((writer.deadline - now + 999999) / 1000000);
        if (timeout > 100) timeout = 100;
        int ready = poll(descriptors, 2, timeout);
        if (ready < 0) {
            if (errno == EINTR) continue;
            snprintf(result->error, sizeof(result->error), "capture poll failed: %s", strerror(errno)); rep_stop(&writer, "read_error"); break;
        }
        if (descriptors[1].revents) { rep_stop(&writer, "cancelled"); break; }
        if (descriptors[0].revents & (POLLERR | POLLHUP | POLLNVAL)) {
            snprintf(result->error, sizeof(result->error), "capture descriptor became unavailable"); rep_stop(&writer, "read_error"); break;
        }
        // A full batch can leave packets in libpcap's user-space buffer while
        // the fd is no longer readable. Drain immediately, checking cancellation
        // and the deadline every batch; only block once that buffer is drained.
        int dispatched = pcap_dispatch(capture, 64, rep_packet, (unsigned char *)&writer);
        drain = dispatched == 64;
        if (dispatched == PCAP_ERROR && !writer.stopped) {
            snprintf(result->error, sizeof(result->error), "capture read failed: %s", pcap_geterr(capture)); rep_stop(&writer, "read_error");
        } else if (dispatched == PCAP_ERROR_BREAK && !writer.stopped) {
            snprintf(result->error, sizeof(result->error), "capture stopped without a recorded stop reason"); rep_stop(&writer, "read_error");
        } else if (!live && dispatched >= 0 && dispatched < 64 && !writer.stopped) {
            // Offline dispatch returns short only at EOF (or an explicit
            // callback break handled above), so no idle wait is needed.
            rep_stop(&writer, "offline_end");
        }
    }
    struct pcap_stat stats;
    if (live && pcap_stats(capture, &stats) == 0) {
        result->stats_available = 1; result->stats_received = stats.ps_recv;
        result->stats_dropped = stats.ps_drop; result->stats_interface_dropped = stats.ps_ifdrop;
    }
    if (pcap_dump_flush(dump) != 0) {
        snprintf(result->error, sizeof(result->error), "flushing pcap failed: %s", strerror(errno));
        snprintf(result->stop_reason, sizeof(result->stop_reason), "%s", "write_error");
    }
    if (result->error[0]) status = -1;
    if (strcmp(result->stop_reason, "write_error") == 0) result->pcap_valid = 0;
done:
    if (dump) { pcap_dump_close(dump); file = NULL; }
    if (file) fclose(file);
    free(output_buffer);
    return status;
}

// Compile for the same link type used to enforce the filter. libpcap may place
// the program in the kernel or evaluate it in user space; either way only
// accepted records reach the writer. The offline test seam exercises this same
// compile/install path without opening a capture device.
static int rep_apply_filter(pcap_t *capture, const char *expression, struct rep_pcap_result *result) {
    struct bpf_program filter;
    if (pcap_compile(capture, &filter, expression, 1, PCAP_NETMASK_UNKNOWN) != 0) {
        snprintf(result->error, sizeof(result->error), "invalid BPF filter: %s", pcap_geterr(capture));
        return -1;
    }
    int status = pcap_setfilter(capture, &filter);
    pcap_freecode(&filter);
    if (status != 0) snprintf(result->error, sizeof(result->error), "cannot apply capture filter: %s", pcap_geterr(capture));
    return status;
}

int rep_pcap_capture(const struct rep_pcap_options *options, struct rep_pcap_result *result) {
    memset(result, 0, sizeof(*result));
    result->datalink = -1;
    snprintf(result->stop_reason, sizeof(result->stop_reason), "%s", "setup_error");
    char error[PCAP_ERRBUF_SIZE] = {0};
    pcap_t *capture = pcap_create(options->interface_name, error);
    int status = -1;
    if (!capture) { snprintf(result->error, sizeof(result->error), "%s", error); goto done; }
    if (pcap_set_snaplen(capture, options->snaplen) != 0 ||
        pcap_set_promisc(capture, 0) != 0 ||
        pcap_set_timeout(capture, 100) != 0 ||
        pcap_set_buffer_size(capture, options->buffer_bytes) != 0) {
        snprintf(result->error, sizeof(result->error), "cannot configure libpcap: %s", pcap_geterr(capture)); goto done;
    }
    status = pcap_activate(capture);
    if (status < 0) {
        result->permission_denied = status == PCAP_ERROR_PERM_DENIED || status == PCAP_ERROR_PROMISC_PERM_DENIED;
        snprintf(result->error, sizeof(result->error), "cannot activate interface: %s", pcap_geterr(capture));
        status = -1; goto done;
    }
    if (status > 0) snprintf(result->warning, sizeof(result->warning), "%s", pcap_statustostr(status));
    // Never dispatch/read before the explicit libpcap BPF filter is installed.
    if (rep_apply_filter(capture, options->filter, result) != 0) { status = -1; goto done; }
    if (pcap_setnonblock(capture, 1, error) != 0) {
        snprintf(result->error, sizeof(result->error), "cannot apply nonblocking capture mode: %s", error); status = -1; goto done;
    }
    status = rep_write_capture(capture, options, result, 1);
done:
    if (capture) pcap_close(capture);
    return status;
}

// Package-private self-test hook: caller supplies an existing local synthetic
// pcap and an output fd. Not exported by the Go API or exposed by the CLI.
int rep_pcap_replay(const char *path, const struct rep_pcap_options *options, struct rep_pcap_result *result) {
    memset(result, 0, sizeof(*result)); result->datalink = -1;
    snprintf(result->stop_reason, sizeof(result->stop_reason), "%s", "setup_error");
    char error[PCAP_ERRBUF_SIZE] = {0};
    pcap_t *capture = pcap_open_offline(path, error);
    if (!capture) { snprintf(result->error, sizeof(result->error), "%s", error); return -1; }
    int status = rep_apply_filter(capture, options->filter, result);
    if (status == 0) status = rep_write_capture(capture, options, result, 0);
    pcap_close(capture);
    return status;
}
