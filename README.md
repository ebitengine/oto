# Oto (v2)

[![Go Reference](https://pkg.go.dev/badge/github.com/hajimehoshi/oto/v2.svg)](https://pkg.go.dev/github.com/hajimehoshi/oto/v2)

A low-level library to play sound.

## Platforms

 * Windows
 * macOS
 * Linux
 * FreeBSD
 * OpenBSD
 * Android
 * iOS
 * WebAssembly

## Prerequisite

### macOS

Oto requies `AudioToolbox.framework`, but this is automatically linked.

### iOS

Oto requies these frameworks:

 * `AVFoundation.framework`
 * `AudioToolbox.framework`

Add them to "Linked Frameworks and Libraries" on your Xcode project.

### Linux

ALSA is required. On Ubuntu or Debian, run this command:

```sh
apt install libasound2-dev
```

In most cases this command must be run by root user or through `sudo` command.

### FreeBSD, OpenBSD

BSD systems are not tested well. If ALSA works, Oto should work.

## Crosscompiling

To crosscompile, make sure the libraries for the target architecture are installed, and set `CGO_ENABLED=1` as Go disables [Cgo](https://golang.org/cmd/cgo/#hdr-Using_cgo_with_the_go_command) on crosscompiles by default.
