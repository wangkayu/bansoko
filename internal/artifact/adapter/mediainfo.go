package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/download/domain"
)

const (
	// defaultMediaInfoTimeout bounds one probe. MediaInfo reads container
	// headers and index structures, not the whole file, so a healthy call takes
	// well under a second; the limit only has to stop a hung process.
	defaultMediaInfoTimeout = 15 * time.Second
	// maxMediaInfoOutput bounds the JSON read from the tool. A feature film
	// with hundreds of chapters and tracks stays far below it.
	maxMediaInfoOutput = 2 << 20
	// mediaInfoWaitDelay is how long a killed process may keep the output pipe
	// open before the call stops waiting for it.
	mediaInfoWaitDelay = 2 * time.Second
)

// OSMediaProber reads a local video file's execution-time media facts with the
// MediaInfo command line tool (`mediainfo --Output=JSON <path>`).
//
// The downloaded file is untrusted input: the tool is started directly with the
// absolute path as a single argument (no shell, so no quoting or expansion; an
// absolute path can never be read as an option), with a minimal environment,
// no stdin, a per-call timeout and bounded stdout. Every failure is typed
// (domain.ErrMediaProbe*) and never carries the raw tool output or the path.
type OSMediaProber struct {
	// Binary is the MediaInfo executable; empty means "mediainfo" on PATH.
	Binary string
	// Timeout bounds one probe; zero means defaultMediaInfoTimeout.
	Timeout time.Duration
	// MaxOutputBytes bounds the tool output; zero means maxMediaInfoOutput.
	MaxOutputBytes int
}

// Probe implements the artifact application's MediaProber port.
func (p OSMediaProber) Probe(ctx context.Context, path string) (domain.MediaFacts, error) {
	if err := ctx.Err(); err != nil {
		return domain.MediaFacts{}, err
	}
	if strings.TrimSpace(path) == "" {
		return domain.MediaFacts{}, fmt.Errorf("%w: path is required", domain.ErrMediaProbeFailed)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return domain.MediaFacts{}, fmt.Errorf("%w: resolve path", domain.ErrMediaProbeFailed)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return domain.MediaFacts{}, fmt.Errorf("%w: not a regular file", domain.ErrMediaProbeFailed)
	}

	binary := p.Binary
	if binary == "" {
		binary = "mediainfo"
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultMediaInfoTimeout
	}
	limit := p.MaxOutputBytes
	if limit <= 0 {
		limit = maxMediaInfoOutput
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, binary, "--Output=JSON", absolute)
	cmd.Env = []string{"LC_ALL=C"}
	// A grandchild that keeps the output pipe open must not hold the call past
	// its deadline: Wait stops waiting for the copy this long after the kill.
	cmd.WaitDelay = mediaInfoWaitDelay
	// Standard input and error are the null device. Standard output is copied
	// through a bounded writer that stops the process on overflow.
	output := &boundedOutput{limit: limit, stop: cancel}
	cmd.Stdout = output
	if err := cmd.Start(); err != nil {
		return domain.MediaFacts{}, fmt.Errorf("%w: %s", domain.ErrMediaProbeUnavailable, startFailureClass(err))
	}
	waitErr := cmd.Wait()

	if err := ctx.Err(); err != nil {
		return domain.MediaFacts{}, err
	}
	switch {
	case output.overflow:
		return domain.MediaFacts{}, fmt.Errorf("%w: output exceeds %d bytes", domain.ErrMediaProbeOutputInvalid, limit)
	case errors.Is(callCtx.Err(), context.DeadlineExceeded):
		return domain.MediaFacts{}, domain.ErrMediaProbeTimeout
	case waitErr != nil:
		return domain.MediaFacts{}, fmt.Errorf("%w: tool exited with an error", domain.ErrMediaProbeFailed)
	}
	return parseMediaInfo(output.buffer.Bytes())
}

// boundedOutput collects the tool's standard output up to limit bytes. The
// first byte past the limit marks the output oversized and stops the process;
// nothing past the limit is kept.
type boundedOutput struct {
	limit    int
	buffer   bytes.Buffer
	overflow bool
	stop     context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.overflow {
		return 0, errMediaInfoOutputLimit
	}
	if b.buffer.Len()+len(p) > b.limit {
		b.overflow = true
		b.stop()
		return 0, errMediaInfoOutputLimit
	}
	return b.buffer.Write(p)
}

var errMediaInfoOutputLimit = errors.New("media tool output limit exceeded")

func startFailureClass(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return "not found"
	case errors.Is(err, os.ErrPermission):
		return "not executable"
	default:
		return "cannot start"
	}
}

// mediaInfoDocument is the part of `mediainfo --Output=JSON` that is read.
// Tracks decode into raw maps so an unexpected value type in a field that is
// not read cannot invalidate the whole document.
type mediaInfoDocument struct {
	Media struct {
		Track []map[string]json.RawMessage `json:"track"`
	} `json:"media"`
}

// parseMediaInfo maps MediaInfo JSON to the four facts. A file without a video
// or audio stream simply leaves that fact empty; only unusable JSON is an
// error. When several streams of a kind exist, the stream flagged Default is
// used, else the first one.
func parseMediaInfo(output []byte) (domain.MediaFacts, error) {
	var document mediaInfoDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return domain.MediaFacts{}, fmt.Errorf("%w: malformed JSON", domain.ErrMediaProbeOutputInvalid)
	}
	var general, video, audio mediaInfoTrack
	var videos, audios []mediaInfoTrack
	for _, raw := range document.Media.Track {
		track := mediaInfoTrack(raw)
		switch strings.ToLower(track.get("@type")) {
		case "general":
			if general == nil {
				general = track
			}
		case "video":
			videos = append(videos, track)
		case "audio":
			audios = append(audios, track)
		}
	}
	video = preferredTrack(videos)
	audio = preferredTrack(audios)

	return domain.MediaFacts{
		Resolution: resolutionClass(video),
		VideoCodec: videoCodecName(video),
		AudioCodec: audioCodecName(audio),
		Container:  containerName(general),
		HDR:        hdrFact(video),
	}, nil
}

// hdrFact normalizes the video track's HDR condition to the catalog vocabulary
// (hdr10, hlg, dovi, a comma-joined list of them in the document's order, or a
// lower-cased token for something the table does not know; SDR and absent are
// empty). Both MediaInfo markers are read case-insensitively;
// HDR_Format_Compatibility is the shorter one and is preferred:
//
//	"Dolby Vision" → dovi; combined "HDR10, Dolby Vision" → hdr10,dovi
//	"SMPTE ST 2086" / "HDR10" → hdr10; "HLG" → hlg; "SDR"/absent → ""
func hdrFact(video mediaInfoTrack) string {
	if video == nil {
		return ""
	}
	if compatibility := video.get("HDR_Format_Compatibility"); compatibility != "" {
		return normalizeHDRList(compatibility)
	}
	// A tool that answers no compatibility field (older versions…): the
	// longer HDR_Format carries the same words ("Dolby Vision, Version 1.0,
	// dvhe.05, BL+RPU", "SMPTE ST 2086, HDR10 compatible", "HLG"), so the
	// known ones are matched inside it instead of split off it.
	format := video.get("HDR_Format")
	if format == "" {
		return ""
	}
	lower := strings.ToLower(format)
	switch {
	case strings.Contains(lower, "dolby vision"):
		return "dovi"
	case strings.Contains(lower, "sdr"):
		return ""
	case strings.Contains(lower, "hlg"):
		return "hlg"
	case strings.Contains(lower, "hdr10"), strings.Contains(lower, "st 2086"):
		return "hdr10"
	}
	return hdrUnknown(format)
}

// normalizeHDRList maps one HDR_Format_Compatibility value to the catalog
// vocabulary, joining the readable parts with commas in the document's order
// ("HDR10, Dolby Vision" → "hdr10,dovi") and de-duplicating.
func normalizeHDRList(value string) string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var token string
		switch strings.ToLower(part) {
		case "dolby vision":
			token = "dovi"
		case "hdr10", "smpte st 2086":
			token = "hdr10"
		case "hlg":
			token = "hlg"
		case "sdr":
			token = ""
		default:
			token = hdrUnknown(part)
		}
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return strings.Join(out, ",")
}

// hdrUnknown reduces an unread HDR value to a lower-cased, file-name-safe
// token (spaces to dashes), so a value is readable without being guessed.
func hdrUnknown(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.ToLower(value)), " ", "-")
}

type mediaInfoTrack map[string]json.RawMessage

// get returns a string field, or "" when it is absent or not a string.
func (t mediaInfoTrack) get(key string) string {
	raw, ok := t[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// preferredTrack picks the stream flagged Default, else the first stream.
func preferredTrack(tracks []mediaInfoTrack) mediaInfoTrack {
	for _, track := range tracks {
		if strings.EqualFold(track.get("Default"), "Yes") {
			return track
		}
	}
	if len(tracks) > 0 {
		return tracks[0]
	}
	return nil
}

// resolutionTiers are the canonical resolution classes, largest first. A video
// belongs to a tier when its height or its width reaches 95% of the tier's
// nominal frame, so cropped and letterboxed frames (1920x800, 1280x536,
// 1440x1080) land in the class they are released as. Names follow the release
// title vocabulary (1080p, 720p, ...); interlacing is not distinguished.
var resolutionTiers = []struct {
	name          string
	width, height int
}{
	{"4320p", 7680, 4320},
	{"2160p", 3840, 2160},
	{"1440p", 2560, 1440},
	{"1080p", 1920, 1080},
	{"720p", 1280, 720},
	{"576p", 1024, 576},
	{"540p", 960, 540},
	{"480p", 848, 480},
	{"360p", 640, 360},
}

func resolutionClass(video mediaInfoTrack) string {
	if video == nil {
		return ""
	}
	width := positiveInt(video.get("Width"))
	height := positiveInt(video.get("Height"))
	if width == 0 && height == 0 {
		return ""
	}
	for _, tier := range resolutionTiers {
		if height*100 >= tier.height*95 || width*100 >= tier.width*95 {
			return tier.name
		}
	}
	return ""
}

func positiveInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 || parsed > 1<<20 {
		return 0
	}
	return parsed
}

// videoCodecName maps MediaInfo's Format to the release-title vocabulary
// (HEVC, AVC, AV1, MPEG2, XVID). A format outside the table keeps MediaInfo's
// own name reduced to an upper-case token.
func videoCodecName(video mediaInfoTrack) string {
	if video == nil {
		return ""
	}
	format := video.get("Format")
	switch strings.ToLower(format) {
	case "":
		return ""
	case "hevc":
		return "HEVC"
	case "avc":
		return "AVC"
	case "av1":
		return "AV1"
	case "mpeg video":
		switch {
		case strings.Contains(video.get("Format_Version"), "2"):
			return "MPEG2"
		case strings.Contains(video.get("Format_Version"), "1"):
			return "MPEG1"
		}
		return "MPEG"
	case "mpeg-4 visual":
		marker := strings.ToLower(video.get("CodecID") + " " + video.get("Encoded_Library_Name") + " " + video.get("Format_Settings"))
		if strings.Contains(marker, "xvid") {
			return "XVID"
		}
		return "MPEG4"
	}
	return codecToken(format)
}

// audioCodecName maps MediaInfo's Format to the release-title vocabulary
// (AAC, AC3, EAC3, FLAC, MP3, OPUS, DTS, TRUEHD, PCM).
func audioCodecName(audio mediaInfoTrack) string {
	if audio == nil {
		return ""
	}
	format := audio.get("Format")
	switch strings.ToLower(format) {
	case "":
		return ""
	case "aac":
		return "AAC"
	case "ac-3":
		return "AC3"
	case "e-ac-3":
		return "EAC3"
	case "flac":
		return "FLAC"
	case "opus":
		return "OPUS"
	case "dts":
		return "DTS"
	case "mlp fba":
		return "TRUEHD"
	case "pcm":
		return "PCM"
	case "mpeg audio":
		profile := strings.ToLower(audio.get("Format_Profile"))
		switch {
		case strings.Contains(profile, "layer 3"):
			return "MP3"
		case strings.Contains(profile, "layer 2"):
			return "MP2"
		case strings.Contains(profile, "layer 1"):
			return "MP1"
		}
		return "MPEGAUDIO"
	}
	return codecToken(format)
}

// containerName maps the General stream's Format to the lower-case container
// name users know from file extensions (mkv, mp4, ...).
func containerName(general mediaInfoTrack) string {
	if general == nil {
		return ""
	}
	format := general.get("Format")
	switch strings.ToLower(format) {
	case "":
		return ""
	case "matroska":
		return "mkv"
	case "webm":
		return "webm"
	case "mpeg-4":
		if strings.EqualFold(general.get("Format_Profile"), "QuickTime") {
			return "mov"
		}
		return "mp4"
	case "avi":
		return "avi"
	case "mpeg-ts":
		return "ts"
	case "bdav":
		return "m2ts"
	case "mpeg-ps":
		return "mpg"
	case "flash video":
		return "flv"
	case "ogg":
		return "ogg"
	case "realmedia":
		return "rm"
	case "windows media":
		return "wmv"
	}
	return strings.ToLower(codecToken(format))
}

// codecToken reduces an unmapped MediaInfo format name to an upper-case
// alphanumeric token of at most 16 characters ("" when nothing is left), so a
// value is always safe inside a file name.
func codecToken(format string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(format) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			if out.Len() >= 16 {
				break
			}
		}
	}
	return out.String()
}
