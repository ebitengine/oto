# Oto (音)

[![GoDoc](https://godoc.org/github.com/hajimehoshi/oto?status.svg)](http://godoc.org/github.com/hajimehoshi/oto)

A low-level library to play sound. This package offers `io.WriteCloser` to play PCM sound.

## Platforms

* Windows
* macOS
* Linux
* FreeBSD
* Android
* iOS
* (Modern) web browsers (powered by [GopherJS](https://github.com/gopherjs/gopherjs))

## Prerequisite

### Linux

libasound2-dev is required. On Ubuntu or Debian, run this command:

```sh
apt install libasound2-dev
```

In most cases this command must be run by root user or through `sudo` command.

### FreeBSD

OpenAL is required. Install openal-soft:

```sh
pkg install openal-soft
```
