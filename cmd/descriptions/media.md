# rep media

Inspect a single `webrtc_media` recording or assemble its ordered binary
MediaRecorder chunks into one container file. Select a workspace and task.

```sh
rep media RECORD --saved ARCHIVE_HASH --info
rep media RECORD --saved ARCHIVE_HASH --require-complete --save recording.webm
```

Capture with `browser open`, `browse`, `browser fetch`, or `browser action`
and `--webrtc-media`. Start observation before the application creates its peer
connections. Each record contains one audio or video track and one direction.
Use `rep stream RECORD --saved HASH` for bounded chunk descriptors or individual
chunk bytes. MediaRecorder timeslices may only be playable when concatenated.

The source is `browser_media_recorder`, payload semantics `reencoded_media`,
and scope `recorded_media_interval`. Chromium records clones of existing tracks.
This does not request microphone/camera access. It observes media already
available to the application. Incoming recordings contain decoded, re-encoded
media; outgoing recordings observe a local track before transport. Neither
establishes the exact original RTP codec frames or complete call coverage.

`--require-complete` checks closed interval coverage, contiguous chunk indices,
binary encoding, and the final recorder's chunk and byte counts. Completeness covers the recording interval;
media fidelity, renderer trust, decoder playability and network completeness are
separate. A partial export can be unreadable and retains its reasons in JSON.
`container_playability` stays `not_verified`; this command does not run a decoder.

`--save PATH` creates a new private file without replacing files or symlinks.
Chunks are decoded through a bounded buffer, written in order, hashed, synced
and atomically published. JSON reports path, `artifact_sha256`, `artifact_bytes`,
`chunks`, `recorded_bytes`, `assembly_complete`, and coverage. `--run RUN_ID`
additionally records the export and imports the assembled file into run evidence.
Aliases such as `--saved latest` resolve once; output and run evidence retain the
selected archive's hash ID.

Default output budget is 8192 bytes, adjustable with `--max-bytes` (2048–65536).
Metadata pages contain no audio/video bytes. Archive lookup reads the selected
archive; its retained base64 data is still held in memory during export. The
copying stage uses fixed buffers and does not allocate a second full recording.
