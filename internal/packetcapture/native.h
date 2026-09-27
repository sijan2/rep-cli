#ifndef REP_PACKET_CAPTURE_H
#define REP_PACKET_CAPTURE_H
#include <stdint.h>

struct rep_pcap_options {
    const char *interface_name;
    const char *filter;
    int output_fd;
    int cancel_fd;
    int ready_fd;
    int snaplen;
    int buffer_bytes;
    uint64_t duration_ns;
    uint64_t max_packets;
    uint64_t max_bytes;
};

struct rep_pcap_result {
    int datalink;
    int pcap_valid;
    int permission_denied;
    int stats_available;
    uint64_t packets;
    uint64_t captured_bytes;
    uint64_t original_bytes;
    uint64_t truncated_packets;
    uint64_t stats_received;
    uint64_t stats_dropped;
    uint64_t stats_interface_dropped;
    int64_t observation_started_ns;
    char stop_reason[32];
    char error[512];
    char warning[256];
};

int rep_pcap_capture(const struct rep_pcap_options *, struct rep_pcap_result *);
int rep_pcap_replay(const char *, const struct rep_pcap_options *, struct rep_pcap_result *);
#endif
