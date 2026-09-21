# syntax=docker/dockerfile:1

# FFmpeg 8.x is newer than any distro package and the SCTE-35 muxer patch is not upstream,
# so the image builds its own. x264/x265 force --enable-gpl, which is why the published
# image is GPL-3.0 while the source stays MIT. Never --enable-nonfree: it would make the
# image non-redistributable.

ARG FFMPEG_VERSION=8.0
ARG GO_VERSION=1.25

FROM debian:trixie-slim AS ffmpeg
ARG FFMPEG_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl nasm patch pkg-config \
        libsrt-openssl-dev libx264-dev libx265-dev zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /ffmpeg
RUN curl -fsSL "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    | tar -xJ --strip-components=1

# Without this an SCTE-35 stream leaves as private data and no packager sees it.
COPY patches/ffmpeg-mpegts-scte35.patch .
RUN patch -p1 < ffmpeg-mpegts-scte35.patch

RUN ./configure \
        --prefix=/usr/local \
        --enable-gpl \
        --enable-libx264 --enable-libx265 --enable-libsrt \
        --enable-shared --disable-static \
        --disable-doc --disable-programs --disable-debug \
    && make -j"$(nproc)" && make install

FROM golang:${GO_VERSION}-trixie AS build
# The transitive SONAMEs of libavformat have to be resolvable at link time.
RUN apt-get update && apt-get install -y --no-install-recommends \
        pkg-config libsrt-openssl-dev libx264-dev libx265-dev \
    && rm -rf /var/lib/apt/lists/*
COPY --from=ffmpeg /usr/local /usr/local
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig LD_LIBRARY_PATH=/usr/local/lib

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/ingestlab ./cmd/ingestlab

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        libsrt1.5-openssl libx264-164 libx265-215 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=ffmpeg /usr/local/lib/ /usr/local/lib/
COPY --from=build /out/ingestlab /usr/local/bin/ingestlab
RUN ldconfig

ENTRYPOINT ["ingestlab"]
