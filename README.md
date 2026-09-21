# ingestlab

A stream impairment and conformance lab: publish media to a live endpoint, inject timed
metadata and SCTE-35, and break the stream on purpose to see what downstream does about it.

Written in Go against FFmpeg 8.x.

## Status

Early, and usable. The impairment vocabulary, the metadata injection and the event log
work and are tested. An optional in-process transcode and a burnt-in timecode band for
measuring glass-to-glass latency are planned, not built.

## Build

Needs FFmpeg 8.x development libraries.

```sh
PKG_CONFIG_PATH=/usr/local/lib/pkgconfig go build ./cmd/ingestlab
```

SCTE-35 over MPEG-TS additionally needs a patched FFmpeg — see
[patches/](patches/ffmpeg-mpegts-scte35.patch). Everything else runs on a stock build.

```sh
cd path/to/FFmpeg && git apply path/to/ingestlab/patches/ffmpeg-mpegts-scte35.patch
make -j"$(nproc)" && sudo make install
```

## Use

```sh
ingestlab video.mkv rtmp://host/live/key
ingestlab video.mkv srt://host:9000 --loop 0
ingestlab video.mkv rtmp://host/live/key --hold-tracks video --hold-at 20s,60s
```

Durations accept a unit anywhere: `10s`, `1500ms`, `2m`, `1h`. A bare number is
milliseconds.

Every random draw comes from `--seed`, and every schedule runs on the media clock except
burst delivery, which models the network and therefore runs on the wall clock. A run with
the same seed and the same source reproduces.

### Source and output

| Option | Meaning |
|---|---|
| `--format` | output container; guessed from the destination scheme when unset |
| `--map-all` | map every input stream, not just the first audio and video |
| `--map-timed-data` | also map input subtitle and data streams |
| `--vod` | send as fast as the output accepts, without real-time pacing |
| `--loop` | extra passes over the input, `-1` for endless |
| `--output-ts-offset` | delay every output timestamp |
| `--seed` | seed for every random draw; `0` picks one and logs it |

### Delivery impairments

These change *when* a packet that already exists leaves the process. Timestamps written on
the wire and timestamps used for pacing are separate: an offset moves the first, never the
second.

| Option | Meaning |
|---|---|
| `--jitter`, `--jitter-min` | deliver in back-to-back bursts of up to this much buffered media |
| `--cadence-jitter` | wobble each video output timestamp by ±this much; audio stays steady |
| `--audio-offset`, `--video-offset` | shift one track's output timestamps to build an A/V desync |
| `--hold-tracks` | tracks to freeze: `audio,video,subtitle,data` (`data` covers the injected tracks) |
| `--hold-duration` | length of each hold; `0` freezes for good and drops (default `10s`) |
| `--hold-duration-min` | when set, each hold lasts a random time up to `--hold-duration` |
| `--hold-every` | start a hold this often (default `30s`) |
| `--hold-at` | explicit hold times, e.g. `20s,60s`; overrides `--hold-every` |

A hold never rewrites a timestamp. The held track's packets pile up, the other tracks keep
flowing, and the burst arrives seconds behind with its original timing — which is what the
server has to cope with.

The muxer's own interleave buffer (`max_interleave_delta`) is held to 100 ms, well below
libavformat's 10 s default. It is delay we would otherwise add and then measure, and it
blunts these impairments: a 5 s hold measures 4.89 s behind at 100 ms and 4.49 s at 500 ms.

### Track lifecycle faults

| Option | Meaning |
|---|---|
| `--start-audio-after`, `--start-video-after` | withhold a track until this much media has passed (ENG-241) |
| `--stop-media-after` | stop sending audio and video; injected metadata keeps flowing |
| `--error-media-after` | replace the video payload with garbage (ENG-664) |
| `--crash-after` | drop the connection without a trailer and exit 3 |

These run on the media clock: `--output-ts-offset` is a wire offset and does not move the
instant `--stop-media-after` fires.

### Injected metadata

| Option | Meaning |
|---|---|
| `--meta-tracks` | `metadata`, `scte35`, `id3`, `subtitles`, `none` (default `metadata`) |
| `--interval` | how often to emit; `0` announces the track and leaves it empty (default `1s`) |
| `--start-meta-after` | delay the first emission |
| `--onfi` | send the timecode as `onFI` instead of `onMetaData` |
| `--id3-description` | TXXX description for the `id3` track |
| `--scte-id`, `--scte-duration`, `--scte-announce`, `--scte-oncuepoint` | SCTE-35 cue shape |

FLV carries one metadata track at a time; MPEG-TS carries all of them. `id3` needs MPEG-TS.
`scte35` emits `onAdCue` (or `onCuePoint`) over FLV, and a `splice_insert` section over
MPEG-TS — the latter needs [patches/ffmpeg-mpegts-scte35.patch](patches/ffmpeg-mpegts-scte35.patch),
which is still not upstream as of FFmpeg 9.0. An unpatched build writes the cue as a private
data stream, packagers will not see it, and ingestlab says so on startup. With that
track on, `--interval` must be at least `--scte-duration`: `auto_return` closes each break
where it says it will, so a shorter interval would announce the next break inside the one
still running.

### Telemetry

`--events` writes an `events.jsonl` of what the run actually did, so a reader of the stream
can assert against ground truth instead of inferring it from symptoms:

```json
{"t_wall":"2026-09-20T20:21:13.102Z","t_media":20,"kind":"hold.start","tracks":["video"],"until":25}
{"t_wall":"2026-09-20T20:21:13.107Z","t_media":25,"kind":"hold.burst","held_seconds":5,"packets":150}
```

Kinds: `stream.start`, `stream.end`, `loop`, `hold.start`, `hold.burst`, `hold.drop`,
`jitter.burst`, `fault.injected`, `scte35.cue`, `meta.sent`. `--summary` writes a
`summary.json` of the run.

## Shutdown

SIGINT and SIGTERM write the trailer and unpublish cleanly, within a few seconds.
`--crash-after` is the explicit way to vanish mid-stream instead.

## Licence

Source is MIT. A distributed Docker image links x264/x265 and is therefore GPL-3.0.
