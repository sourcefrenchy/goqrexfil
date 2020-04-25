# goqrexfil
 A mini project to exfiltrate data via QR codes - I just like writing code around data exfiltration. 
 
 The whole idea in this one is that the data can be exfiltrated via a different cover channel using video recording device and therefore will not trigger any classic network monitoring alerts.
 
 In a first phase, the code allows to take a file from stdin, cut it in pieces and display chunks as QR codes one at a time in your terminal. This allows you to create a video on your phone, just ensure that you are zooming and have the whole terminal in your focus.
 
 In a second phase, you can use the same code to run a web server that will allow you to upload your video for processing. We rely on ffmpeg to extract frames from the recording and then a library to extract QR codes from the frames (minus potential duplicates). Finally, the payload is rebuilt from being retrieved by reading the data from the QR codes and a file is created with the original data.
 
# Caveats/TODO
* Currently very Alpha - works well with small text files, almost ok for PDFs/binary format (I need to have more time to debug this)
* Ugly code - I am not a developper, I normally use Python and wanted to try to learn Golang (be nice if you want to help with improving this ugly code)
 
# Example
## Part 1: Convert file into QR codes and video record
1. use goqrexfil in client mode 
2. start a video recording with phone, point at the shell window

Example:
`
➜ cat top.secret.file | ./goqrexfil --client
-= goqrexfil =-
[*] Client mode: ON
[*] Payload is in 8 chunks, video recording time estimate:


---=== 5 seconds to use CTRL+C if you want to abort ===---
`
Start recording a video now, QR codes will be displayed on the console and stop the video at the end.

## Have server ready to process your video
1. use goqrexfil in server mode
2. go to your server domain/ip on port 8888

Example:
`➜ cat top.secret.file | ./goqrexfil --client
-= goqrexfil =-
[*] Client mode: ON
[*] Payload is in 8 chunks, video recording time estimate:


---=== 5 seconds to use CTRL+C if you want to abort ===---
`
