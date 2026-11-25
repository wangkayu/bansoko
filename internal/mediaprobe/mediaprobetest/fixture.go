// Package mediaprobetest builds tiny real media containers for tests that need
// MediaInfo to recognize a file. The fixtures are header-only EBML/Matroska
// segments written from scratch, so they stay a few hundred bytes, need no
// checked-in blobs, and exercise the same parsing path as a real download.
package mediaprobetest

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Options describes the fixture to write. Zero values fall back to a Matroska
// file with one 1080p HEVC video track and one stereo 48 kHz AAC audio track
// and a 120 s duration.
type Options struct {
	// DocType is the EBML DocType: "matroska" (default) or "webm".
	DocType string
	// VideoCodec is the Matroska video CodecID, e.g. "V_MPEGH/ISO/HEVC".
	VideoCodec string
	// AudioCodec is the Matroska audio CodecID, e.g. "A_AAC".
	AudioCodec string
	// TailPadding appends that many bytes of EBML Void after the segment
	// header, so tests can push the file past a reader's chunk size.
	TailPadding int
}

func (o Options) withDefaults() Options {
	if o.DocType == "" {
		o.DocType = "matroska"
	}
	if o.VideoCodec == "" {
		o.VideoCodec = "V_MPEGH/ISO/HEVC"
	}
	if o.AudioCodec == "" {
		o.AudioCodec = "A_AAC"
	}
	return o
}

// WriteMatroska writes the fixture into dir/name and returns its path.
func WriteMatroska(t *testing.T, dir, name string, opts Options) string {
	t.Helper()
	opts = opts.withDefaults()

	var header ebml
	header.uintElem(0x4286, 1, 1)           // EBMLVersion
	header.uintElem(0x42F7, 1, 1)           // EBMLReadVersion
	header.uintElem(0x42F2, 4, 1)           // EBMLMaxIDLength
	header.uintElem(0x42F3, 8, 1)           // EBMLMaxSizeLength
	header.stringElem(0x4282, opts.DocType) // DocType
	header.uintElem(0x4287, 4, 1)           // DocTypeVersion
	header.uintElem(0x4285, 2, 1)           // DocTypeReadVersion

	info := leaf(func(e *ebml) {
		e.uintElem(0x2AD7B1, 1000000, 4) // TimecodeScale: 1 tick = 1 ms
		e.floatElem(0x4489, 120000)      // Duration: 120000 ms
		e.stringElem(0x4D80, "bansoko-test")
		e.stringElem(0x5741, "bansoko-test")
	})

	video := leaf(func(e *ebml) {
		e.uintElem(0xD7, 1, 1)      // TrackNumber
		e.uintElem(0x73C5, 1111, 4) // TrackUID
		e.uintElem(0x83, 1, 1)      // TrackType: video
		e.uintElem(0xB9, 1, 1)      // FlagEnabled
		e.uintElem(0x88, 1, 1)      // FlagDefault
		e.stringElem(0x86, opts.VideoCodec)
		e.stringElem(0x22B59C, "jpn")     // Language
		e.elem(0xE0, leaf(func(v *ebml) { // Video
			v.uintElem(0xB0, 1920, 2) // PixelWidth
			v.uintElem(0xBA, 1080, 2) // PixelHeight
		}))
	})

	audio := leaf(func(e *ebml) {
		e.uintElem(0xD7, 2, 1)
		e.uintElem(0x73C5, 2222, 4)
		e.uintElem(0x83, 2, 1) // TrackType: audio
		e.uintElem(0xB9, 1, 1)
		e.uintElem(0x88, 1, 1)
		e.stringElem(0x86, opts.AudioCodec)
		e.stringElem(0x22B59C, "jpn")
		e.elem(0xE1, leaf(func(a *ebml) { // Audio
			a.floatElem(0xB5, 48000) // SamplingFrequency
			a.uintElem(0x9F, 2, 1)   // Channels
		}))
	})

	tracks := leaf(func(e *ebml) {
		e.elem(0x1654AE6B, master( // Tracks
			leaf(func(te *ebml) { te.elem(0xAE, video) }), // TrackEntry
			leaf(func(te *ebml) { te.elem(0xAE, audio) }),
		))
	})

	segmentBody := master(
		leaf(func(e *ebml) { e.elem(0x1549A966, info) }), // Info
		tracks,
	)
	if opts.TailPadding > 0 {
		var pad ebml
		pad.id(0xEC) // Void
		pad.buf = append(pad.buf, 0x01)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(opts.TailPadding))
		pad.buf = append(pad.buf, b[1:]...)
		pad.buf = append(pad.buf, make([]byte, opts.TailPadding)...)
		segmentBody = append(segmentBody, pad.buf...)
	}

	var file ebml
	file.elem(0x1A45DFA3, header.buf)  // EBML header
	file.elem(0x18538067, segmentBody) // Segment

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, file.buf, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// ebml builds minimal EBML payloads. Sizes are written as the shortest legal
// vint, element IDs big-endian without leading zeros.
type ebml struct {
	buf []byte
}

func (e *ebml) id(id uint64) {
	switch {
	case id > 0xFFFFFF:
		e.buf = append(e.buf, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		e.buf = append(e.buf, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		e.buf = append(e.buf, byte(id>>8), byte(id))
	default:
		e.buf = append(e.buf, byte(id))
	}
}

func (e *ebml) size(n int) {
	switch {
	case n < 0x7F:
		e.buf = append(e.buf, 0x80|byte(n))
	case n < 0x3FFF:
		e.buf = append(e.buf, 0x40|byte(n>>8), byte(n))
	default:
		e.buf = append(e.buf, 0x01)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		e.buf = append(e.buf, b[1:]...)
	}
}

func (e *ebml) elem(id uint64, payload []byte) {
	e.id(id)
	e.size(len(payload))
	e.buf = append(e.buf, payload...)
}

func (e *ebml) uintElem(id, value uint64, width int) {
	payload := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		payload[i] = byte(value)
		value >>= 8
	}
	// Trim leading zero bytes but keep at least one.
	for len(payload) > 1 && payload[0] == 0 {
		payload = payload[1:]
	}
	e.elem(id, payload)
}

func (e *ebml) floatElem(id uint64, value float64) {
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], math.Float64bits(value))
	e.elem(id, payload[:])
}

func (e *ebml) stringElem(id uint64, value string) {
	e.elem(id, []byte(value))
}

func master(children ...[]byte) []byte {
	var out ebml
	for _, child := range children {
		out.buf = append(out.buf, child...)
	}
	return out.buf
}

func leaf(build func(e *ebml)) []byte {
	var e ebml
	build(&e)
	return e.buf
}
