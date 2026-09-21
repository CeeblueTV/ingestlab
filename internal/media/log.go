package media

import (
	"log/slog"
	"strings"

	"github.com/asticode/go-astiav"
)

// RouteLog sends libav's own messages through our logger, so a run produces one stream of
// structured output instead of two interleaved ones. verbose lifts libav to debug.
func RouteLog(log *slog.Logger, verbose bool) {
	level := astiav.LogLevelWarning
	if verbose {
		level = astiav.LogLevelVerbose
	}
	astiav.SetLogLevel(level)
	astiav.SetLogCallback(func(c astiav.Classer, l astiav.LogLevel, _, msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" {
			return
		}
		attrs := []any{"src", "libav"}
		if c != nil {
			if cl := c.Class(); cl != nil {
				attrs = append(attrs, "component", cl.Name())
			}
		}
		switch {
		case l <= astiav.LogLevelError:
			log.Error(msg, attrs...)
		case l <= astiav.LogLevelWarning:
			log.Warn(msg, attrs...)
		case l <= astiav.LogLevelInfo:
			log.Info(msg, attrs...)
		default:
			log.Debug(msg, attrs...)
		}

		// mpegtsenc says this for any codec it has no stream type for. For SCTE-35 it means
		// the build is missing patches/ffmpeg-mpegts-scte35.patch, which is worth spelling
		// out: a patched build says nothing here, so this is the only honest signal we get.
		if strings.Contains(msg, "scte_35") && strings.Contains(msg, "private data stream") {
			log.Warn("this FFmpeg has no SCTE-35 passthrough: apply "+
				"patches/ffmpeg-mpegts-scte35.patch, or the cue goes out as private data "+
				"and packagers will not see it", attrs...)
		}
	})
}
