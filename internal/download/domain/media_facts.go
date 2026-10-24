package domain

import "errors"

// Keys under which a file's execution-time media facts are stored in
// artifact_files.facts. They are the same keys the naming render paths and
// notifications read (media.resolution, media.video_codec, media.audio_codec,
// media.container, media.hdr), so the stored object is the single source for
// all of them.
const (
	MediaFactResolution = "resolution"
	MediaFactVideoCodec = "video_codec"
	MediaFactAudioCodec = "audio_codec"
	MediaFactContainer  = "container"
	MediaFactHDR        = "hdr"
)

// MediaFacts are the execution-time facts read from the file's own streams.
// An empty field means "no value": the tool was unavailable, the file has no
// such stream, or the stream is not recognised. HDR empty also covers SDR.
// Values use the same vocabulary as the template catalog samples (1080p, HEVC,
// AAC, mkv; HDR as the catalog tokens hdr10, hlg, dovi or their comma-joined
// list).
type MediaFacts struct {
	Resolution string
	VideoCodec string
	AudioCodec string
	Container  string
	HDR        string
}

// Entries returns the non-empty facts keyed by their artifact_files.facts key.
func (m MediaFacts) Entries() map[string]string {
	entries := make(map[string]string, 5)
	for key, value := range map[string]string{
		MediaFactResolution: m.Resolution,
		MediaFactVideoCodec: m.VideoCodec,
		MediaFactAudioCodec: m.AudioCodec,
		MediaFactContainer:  m.Container,
		MediaFactHDR:        m.HDR,
	} {
		if value != "" {
			entries[key] = value
		}
	}
	return entries
}

// IsZero reports whether no fact has a value.
func (m MediaFacts) IsZero() bool { return m == MediaFacts{} }

// Typed probe failures. Adapters wrap these so the application can log a
// bounded error class without exposing raw errors or private paths.
var (
	// ErrMediaProbeUnavailable means the probe tool is missing or cannot start.
	ErrMediaProbeUnavailable = errors.New("media probe tool is unavailable")
	// ErrMediaProbeTimeout means the probe exceeded its per-call time limit.
	ErrMediaProbeTimeout = errors.New("media probe timed out")
	// ErrMediaProbeFailed means the tool ran but exited with an error.
	ErrMediaProbeFailed = errors.New("media probe failed")
	// ErrMediaProbeOutputInvalid means the output was oversized or malformed.
	ErrMediaProbeOutputInvalid = errors.New("media probe output is invalid")
)
