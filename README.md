<p align="center">
 <a href="#quick-start">Quick start</a> •
 <a href="#building-locally">Building locally</a> •
 <a href="#usage">Usage</a> •
 <a href="#telemetry">Telemetry</a> •
 <a href="#shutdown">Shutdown</a> •
 <a href="#contribution">Contribution</a> •
 <a href="#license">License</a>
</p>
<h1 align="center">
  ingestlab
</h1>
<h4 align="center">A stream impairment and conformance lab: publish media to a live endpoint, inject timed metadata and SCTE-35, and break the stream on purpose to see what downstream does about it.</h4>
<p align="center">
  <a href="./LICENSE"><img src="https://img.shields.io/badge/License-AGPL_v3-yellow.svg" alt="License: AGPL v3"></a>
</p>

Written in Go against FFmpeg 8.x.

## Quick start

The Docker image is the shortest way in: it carries its own FFmpeg with everything ingestlab
can use, including SRT, RTMPS, libx264 and the [SCTE-35 muxer patch](#injected-metadata), so
Docker is the only thing to install.

Publish a file from a local folder, here with the [timecode band](#timecode-band) burnt in:

```sh
docker run -it --rm -v ~/videos/:/var/videos/ ghcr.io/ceebluetv/ingestlab \
    /var/videos/<your file> rtmp://<host>/live/<your stream> --epoch
```

Each [release](https://github.com/CeeblueTV/ingestlab/releases) is published for
`linux/amd64` as `ghcr.io/ceebluetv/ingestlab:<version>`, and the newest as `latest`. To build
the image yourself instead:

```sh
git clone https://github.com/CeeblueTV/ingestlab.git
cd ingestlab
docker build -t ingestlab -f docker/Dockerfile .
```

> [!NOTE]
> The first build compiles FFmpeg and takes several minutes.

> [!TIP]
> Inside the container `localhost` is the container itself. To reach a server running on the
> same machine, add `--network host` (Linux) or publish to `host.docker.internal` (Docker
> Desktop).

## Building locally

### Prerequisites

- [Go](https://go.dev/dl/) 1.25 or later
- A C compiler and `pkg-config`, since the FFmpeg bindings use cgo
- FFmpeg 8.x shared libraries and headers

FFmpeg is optional piece by piece, depending on what you use:

| For | FFmpeg needs |
|---|---|
| `srt://` | `--enable-libsrt` |
| `rtmps://` | `--enable-openssl` |
| `--epoch` | `--enable-libx264` |
| SCTE-35 over MPEG-TS | [patches/ffmpeg-mpegts-scte35.patch](patches/ffmpeg-mpegts-scte35.patch) |

### FFmpeg

FFmpeg 8 is newer than most distribution packages, so build it from source. On Debian or
Ubuntu:

```sh
sudo apt-get install build-essential nasm pkg-config libsrt-openssl-dev libssl-dev libx264-dev
curl -fsSL https://ffmpeg.org/releases/ffmpeg-8.0.tar.xz | tar -xJ
cd ffmpeg-8.0
patch -p1 < path/to/ingestlab/patches/ffmpeg-mpegts-scte35.patch   # optional
./configure --prefix=/usr/local --enable-shared --disable-static \
    --enable-gpl --enable-version3 --enable-libx264 --enable-libsrt --enable-openssl
make -j"$(nproc)" && sudo make install && sudo ldconfig
```

### Build

```sh
git clone https://github.com/CeeblueTV/ingestlab.git
cd ingestlab
export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig
go build ./cmd/ingestlab
```

Or install it straight into `$GOPATH/bin`:

```sh
PKG_CONFIG_PATH=/usr/local/lib/pkgconfig go install github.com/CeeblueTV/ingestlab/cmd/ingestlab@latest
```

> [!TIP]
> No server is needed to try it: the destination can be a file.
> ```sh
> ./ingestlab video.mkv /tmp/out.flv --vod --loop 0
> ```
> If the binary cannot find `libavformat`, run `sudo ldconfig` or set
> `LD_LIBRARY_PATH=/usr/local/lib`.

### Tests

```sh
go test ./...
```

The read-back tests publish a real file and assert on what arrived. They skip unless
`INGESTLAB_SAMPLE` points at a source whose first audio stream is AAC, since FLV rejects
Opus. One of them runs in real time; `-short` skips it.

## Usage

```sh
ingestlab <source> <destination> [options]
```

```sh
ingestlab video.mkv rtmp://host/live/key
ingestlab video.mkv srt://host:9000 --loop 0
ingestlab video.mkv rtmp://host/live/key --hold-tracks video --hold-at 20s,60s
```

Options come after the source and destination.

Durations accept a unit anywhere: `10s`, `1500ms`, `2m`, `1h`. A bare number is
milliseconds.

Every random draw comes from `--seed`, and every schedule runs on the media clock except
burst delivery, which models the network and therefore runs on the wall clock. A run with
the same seed and the same source reproduces.

### Source and output

| Option | Default | Meaning |
|---|---|---|
| `--format` | from the destination | output container |
| `--map-all` | off | map every input stream, not just the first audio and video |
| `--map-timed-data` | off | also map input subtitle and data streams |
| `--vod` | off | send as fast as the output accepts, without real-time pacing |
| `--loop` | `-1` | extra passes over the input, `-1` for endless |
| `--output-ts-offset` | `0` | delay every output timestamp |
| `--seed` | `0` | seed for every random draw; `0` picks one and logs it |
| `-v` | off | verbose logging, FFmpeg's own included |
| `--version` | | print the version and exit; given alone |

### Delivery impairments

These change *when* a packet that already exists leaves the process. Timestamps written on
the wire and timestamps used for pacing are separate: an offset moves the first, never the
second.

| Option | Default | Meaning |
|---|---|---|
| `--jitter` | off | deliver in back-to-back bursts of up to this much buffered media |
| `--jitter-min` | `0` | shortest burst; each one lasts a random time between this and `--jitter` |
| `--cadence-jitter` | off | wobble each video output timestamp by ±this much; audio stays steady |
| `--audio-offset`, `--video-offset` | `0` | shift one track's output timestamps to build an A/V desync |
| `--hold-tracks` | none | tracks to freeze: `audio,video,subtitle,data` (`data` covers the injected tracks) |
| `--hold-duration` | `10s` | length of each hold; `0` freezes for good and drops |
| `--hold-duration-min` | unset | when set, each hold lasts a random time between this and `--hold-duration` |
| `--hold-every` | `30s` | start a hold this often |
| `--hold-at` | unset | explicit hold times, e.g. `20s,60s`; overrides `--hold-every` |

A hold never rewrites a timestamp. The held track's packets pile up, the other tracks keep
flowing, and the burst arrives seconds behind with its original timing — which is what the
server has to cope with.

The muxer's own interleave buffer (`max_interleave_delta`) is held to 100 ms, well below
libavformat's 10 s default. It is delay we would otherwise add and then measure, and it
blunts these impairments: a 5 s hold measures 4.89 s behind at 100 ms and 4.49 s at 500 ms.

### Track lifecycle faults

All of these are off by default.

| Option | Meaning |
|---|---|
| `--start-audio-after`, `--start-video-after` | withhold a track until this much media has passed |
| `--stop-media-after` | stop sending audio and video; injected metadata keeps flowing |
| `--error-media-after` | replace the video payload with garbage |
| `--crash-after` | drop the connection without a trailer and exit 3 |

These run on the media clock: `--output-ts-offset` is a wire offset and does not move the
instant `--stop-media-after` fires.

### Injected metadata

| Option | Default | Meaning |
|---|---|---|
| `--meta-tracks` | `metadata` | tracks to inject: `metadata`, `scte35`, `id3`, `subtitles`, `none` |
| `--interval` | `1s` | how often to emit; `0` announces the track and leaves it empty |
| `--start-meta-after` | `0` | delay the first emission |
| `--onfi` | off | send the timecode as `onFI` instead of `onMetaData` |
| `--id3-description` | `ceeblue.metadata` | TXXX description for the `id3` track |
| `--scte-id` | `0` | first SCTE-35 event id, incremented per cue |
| `--scte-duration` | `4s` | length of each SCTE-35 break |
| `--scte-announce` | `4s` | how far ahead of the break the cue is sent |
| `--scte-oncuepoint` | off | send `onCuePoint` (AWS Elemental) instead of `onAdCue` over FLV |

FLV carries one metadata track at a time; MPEG-TS carries all of them. `id3` needs MPEG-TS.
`scte35` emits `onAdCue` (or `onCuePoint`) over FLV, and a `splice_insert` section over
MPEG-TS — the latter needs [patches/ffmpeg-mpegts-scte35.patch](patches/ffmpeg-mpegts-scte35.patch),
which is still not upstream as of FFmpeg 9.0. An unpatched build writes the cue as a private
data stream, packagers will not see it, and ingestlab says so on startup. With that
track on, `--interval` must be at least `--scte-duration`: `auto_return` closes each break
where it says it will, so a shorter interval would announce the next break inside the one
still running.

### Timecode band

`--epoch` burns the wall clock into the top of the picture as 32 black-and-white blocks, one
per bit, `day(5) hour(5) minute(6) second(6) millisecond(10)` UTC with black for 1. A player
reads it off the rendered frame, so the difference between it and the clock is end-to-end
latency measured through every stage the viewer's video goes through — ingest, server,
packaging, network, jitter buffer, decoder, compositor.

| Option | Default | Meaning |
|---|---|---|
| `--epoch` | off | draw the band, which costs a decode and a re-encode |
| `--epoch-gop` | `1.6s` | keyframe interval of the re-encoded video |
| `--epoch-bitrate` | `4000000` | constant bitrate of the re-encoded video, in bits per second |

It is off by default and everything else is a true passthrough: with `--epoch` unset no
decoder, encoder or filter graph is allocated at all, and the source's own bitstream — its
SPS/PPS, profile, GOP structure — reaches the server untouched. That matters for any test
that exists to ingest one exact bitstream, so reach for the band only when latency is the
thing being measured.

The clock is read after pacing, at the last instant before the frame is encoded, and every
impairment acts on the picture afterwards. A hold therefore shows up *in* the measurement
rather than hiding inside it. The block size is `floor(width/32)`, so a width that is a
multiple of 32 lines up exactly; anything else leaves a margin and ingestlab says so.

## Telemetry

| Option | Default | Meaning |
|---|---|---|
| `--events` | unset | path of an `events.jsonl` of what the run actually did |
| `--summary` | unset | path of a `summary.json` written when the run ends |

The event log lets a reader of the stream assert against ground truth instead of inferring
it from symptoms:

```json
{"t_wall":"2026-09-20T20:21:13.102Z","t_media":20,"kind":"hold.start","tracks":["video"],"until":25}
{"t_wall":"2026-09-20T20:21:13.107Z","t_media":25,"kind":"hold.burst","held_seconds":5,"packets":150}
```

Kinds: `stream.start`, `stream.end`, `loop`, `hold.start`, `hold.burst`, `hold.drop`,
`jitter.burst`, `fault.injected`, `scte35.cue`, `meta.sent`.

## Shutdown

SIGINT and SIGTERM write the trailer and unpublish cleanly, within a few seconds.
`--crash-after` is the explicit way to vanish mid-stream instead.

## Contribution

All contributions are welcome. Please see [our contribution guide](CONTRIBUTING.md) for
details.

## License

[GNU Affero General Public License v3.0](LICENSE) or later. The Docker image also links
x264 and x265, which are GPL-2.0-or-later and combine with it under GPL-3.0.
