# syntax=docker/dockerfile:1

# FFmpeg 8.x is newer than any distro package and the SCTE-35 muxer patch is not upstream,
# so the image builds its own. x264/x265 force --enable-gpl; GPL-3.0 combines with the
# AGPL-3.0 source. Never --enable-nonfree: it would make the image non-redistributable.
#
# OpenSSL is the TLS behind rtmps:// (and https://): without a TLS library libavformat has
# no tls protocol and every such URL fails with "Protocol not found". OpenSSL 3 is
# Apache-2.0, which FFmpeg accepts alongside --enable-gpl only with --enable-version3 —
# GPLv3, what the image already is. The runtime libssl3 comes with libsrt1.5-openssl.

ARG FFMPEG_VERSION=8.0
ARG GO_VERSION=1.25

FROM debian:trixie-slim AS ffmpeg
ARG FFMPEG_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl nasm patch pkg-config \
        libsrt-openssl-dev libssl-dev libx264-dev libx265-dev zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /ffmpeg
RUN curl -fsSL "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    | tar -xJ --strip-components=1

# Without this an SCTE-35 stream leaves as private data and no packager sees it.
COPY patches/ffmpeg-mpegts-scte35.patch .
RUN patch -p1 < ffmpeg-mpegts-scte35.patch

RUN ./configure \
        --prefix=/usr/local \
        --enable-gpl --enable-version3 \
        --enable-libx264 --enable-libx265 --enable-libsrt --enable-openssl \
        --enable-shared --disable-static \
        --disable-doc --disable-programs --disable-debug \
    && make -j"$(nproc)" && make install

FROM golang:${GO_VERSION}-trixie AS build
# The transitive SONAMEs of libavformat have to be resolvable at link time.
RUN apt-get update && apt-get install -y --no-install-recommends \
        pkg-config libsrt-openssl-dev libssl-dev libx264-dev libx265-dev \
    && rm -rf /var/lib/apt/lists/*
COPY --from=ffmpeg /usr/local /usr/local
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig LD_LIBRARY_PATH=/usr/local/lib

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# .git is not in the context, so the version has to come in from outside.
ARG VERSION
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ingestlab ./cmd/ingestlab

# CI's gate: `docker build --target test`. Tests run against the same patched FFmpeg.
FROM build AS test
RUN go vet ./... && go test -short ./...

FROM debian:trixie-slim
# ca-certificates: OpenSSL verifies the server's certificate, and needs the roots to do it.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libsrt1.5-openssl libssl3t64 libx264-164 libx265-215 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=ffmpeg /usr/local/lib/ /usr/local/lib/
COPY --from=build /out/ingestlab /usr/local/bin/ingestlab
RUN ldconfig

ENTRYPOINT ["ingestlab"]
