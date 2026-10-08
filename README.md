# goqrexfil

A mini project to exfiltrate data via QR codes - I just like writing code around data exfiltration.

The whole idea in this one is that the data can be exfiltrated via a different cover channel using video recording
device and therefore will not trigger any classic network monitoring alerts.

In a first phase using --client, the code allows to take a file from stdin, cut it in pieces and display chunks as QR
codes one at a time in your terminal. This allows you to create a video on your phone, just ensure that you are zooming
and have the whole terminal in your focus.

In a second phase using --server, you can use the same code elsewhere on a system you control and run a web server that
will allow you to upload your video for processing. We rely on ffmpeg to extract frames from the recording and then a
library to extract QR codes from the frames (minus potential duplicates). Finally, the payload is rebuilt from being
retrieved by reading the data from the QR codes and a file is created with the original data.

# Caveats/TODO

* Works with text and binary files (PDFs, random data) - verified by the built-in self-test and the
  end-to-end video tests in `goqrexfil_test.go`.
* Each QR code carries a sequence number, so the server reassembles chunks in order and tells you
  exactly which ones are missing if a frame is dropped.

# Reliability: missing chunks and resume

If the video misses a frame, the server reports the gap instead of silently producing a corrupt
file:

```
[*] Received 29/38 chunks - MISSING: 4,8,12,16,20,24,28,32,36
```

The upload page (and the `/missing` endpoint) then gives you the exact chunk numbers to re-record.
Re-run the client with `--resume` to display only those chunks, record a second short video, and
upload it again:

```
cat top.secret.file | ./goqrexfil --client --resume 4,8,12,16,20,24,28,32,36
```

# Self-test (no camera needed)

Verify the whole encode/decode pipeline locally - useful for debugging without a phone:

```
cat top.secret.file | ./goqrexfil --selftest
[*] PASS: round-trip OK, payload hash <hex>
```

# Example

## Part 1: Convert file into QR codes and video record

1. use goqrexfil in client mode
2. start a video recording with phone, point at the shell window

Example:

```
➜ cat top.secret.file | ./goqrexfil --client
-= goqrexfil =-
[*] Client mode: ON
[*] Payload is in 8 chunks, video recording time estimate:


---=== 5 seconds to use CTRL+C if you want to abort ===---
```

Start recording a video now, QR codes will be displayed on the console and stop the video at the end.

## Have server ready to receive and process your video

1. use goqrexfil in server mode
2. From your phone, go to your server domain/ip on port 9999 e.g. http://1.2.3.4:9999/ and upload the video:

Example:

```
➜ ./goqrexfil -server
-= goqrexfil =-
[*] Server mode: ON
2020/04/25 11:42:15
[*] File received
[*] Frames extracted

public/004.png has payload.. Adding
public/005.png has payload.. Adding

[*] Payload retrieved (Wrote 824 bytes): payload.raw.
[GIN] 2020/04/25 - 11:42:19 | 200 |  4.336634181s |    172.16.0.110 | POST     "/upload"

^C⏎
```

## Retrieving payload

Download it from the server (`http://1.2.3.4:9999/payload`), or read the local
`./payload/payload.bin` on the machine running the server:

```➜ cat payload/payload.bin
------------|


----|  Intro

Writing shellcode for the MIPS/Irix platform is not much different from writing
shellcode for the x86 architecture.  There are, however, a few tricks worth
knowing when attempting to write clean shellcode (which does not have any NULL
bytes and works completely independent from it's position).

This small paper will provide you with a crash course on writing IRIX
shellcode for use in exploits.  It covers the basic stuff you need to know to
start writing basic IRIX shellcode.  It is divided into the following sections:

    - The IRIX operating system
    - MIPS archstages the MIPS design
      has reflected this on the instructions itself: every instruction is
      32 bits broad (4 bytes), and can be divided most of the times into
      segments which correspond with each pipestage..```
