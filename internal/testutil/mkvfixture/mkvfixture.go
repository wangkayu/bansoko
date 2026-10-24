// Package mkvfixture builds a tiny but structurally valid Matroska file for
// tests that need a real media container: a header, segment info and a track
// list, with no frames. MediaInfo reads the stream facts from the track list, so
// the file reports the declared resolution and codecs without any encoder.
package mkvfixture

import (
	"encoding/binary"
	"math"
)

// Spec describes the tracks of one fixture.
type Spec struct {
	// Width and Height are the video track's pixel dimensions.
	Width, Height uint64
	// VideoCodecID and AudioCodecID are Matroska codec ids, for example
	// "V_MPEGH/ISO/HEVC" and "A_AAC". An empty id omits that track.
	VideoCodecID string
	AudioCodecID string
	// Title is stored in the segment info. It lets a test make otherwise
	// identical fixtures differ byte for byte (for example per run).
	Title string
}

// HEVC1080pAAC is an HEVC 1920x1080 video track with a stereo AAC audio track:
// the catalog sample values (1080p, HEVC, AAC, mkv). title may be empty.
func HEVC1080pAAC(title string) []byte {
	return Build(Spec{Width: 1920, Height: 1080, VideoCodecID: "V_MPEGH/ISO/HEVC", AudioCodecID: "A_AAC", Title: title})
}

// Build returns the Matroska bytes for spec.
func Build(spec Spec) []byte {
	var tracks []byte
	if spec.VideoCodecID != "" {
		tracks = append(tracks, element(0xAE,
			uintElement(0xD7, 1),   // TrackNumber
			uintElement(0x73C5, 1), // TrackUID
			uintElement(0x83, 1),   // TrackType: video
			uintElement(0x9C, 0),   // FlagLacing
			stringElement(0x86, spec.VideoCodecID),
			element(0xE0,
				uintElement(0xB0, spec.Width),  // PixelWidth
				uintElement(0xBA, spec.Height), // PixelHeight
			),
		)...)
	}
	if spec.AudioCodecID != "" {
		tracks = append(tracks, element(0xAE,
			uintElement(0xD7, 2),
			uintElement(0x73C5, 2),
			uintElement(0x83, 2), // TrackType: audio
			stringElement(0x86, spec.AudioCodecID),
			element(0xE1,
				floatElement(0xB5, 48000), // SamplingFrequency
				uintElement(0x9F, 2),      // Channels
			),
		)...)
	}

	header := element(0x1A45DFA3,
		uintElement(0x4286, 1), // EBMLVersion
		uintElement(0x42F7, 1), // EBMLReadVersion
		uintElement(0x42F2, 4), // EBMLMaxIDLength
		uintElement(0x42F3, 8), // EBMLMaxSizeLength
		stringElement(0x4282, "matroska"),
		uintElement(0x4287, 4), // DocTypeVersion
		uintElement(0x4285, 2), // DocTypeReadVersion
	)
	info := [][]byte{
		uintElement(0x2AD7B1, 1000000), // TimecodeScale
		floatElement(0x4489, 1000),     // Duration
		stringElement(0x4D80, "mkvfixture"),
		stringElement(0x5741, "mkvfixture"),
	}
	if spec.Title != "" {
		info = append(info, stringElement(0x7BA9, spec.Title)) // Title
	}
	segment := element(0x18538067,
		element(0x1549A966, info...), // Info
		element(0x1654AE6B, tracks),  // Tracks
	)
	return append(header, segment...)
}

// element encodes one EBML element: id, size and the concatenated children.
func element(id uint64, children ...[]byte) []byte {
	var payload []byte
	for _, child := range children {
		payload = append(payload, child...)
	}
	return encode(id, payload)
}

func encode(id uint64, payload []byte) []byte {
	out := idBytes(id)
	out = append(out, sizeBytes(uint64(len(payload)))...)
	return append(out, payload...)
}

func uintElement(id, value uint64) []byte {
	var payload []byte
	started := false
	for shift := 56; shift >= 0; shift -= 8 {
		b := byte(value >> uint(shift))
		if b != 0 || started || shift == 0 {
			payload = append(payload, b)
			started = true
		}
	}
	return encode(id, payload)
}

func floatElement(id uint64, value float64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, math.Float64bits(value))
	return encode(id, payload)
}

func stringElement(id uint64, value string) []byte {
	return encode(id, []byte(value))
}

// idBytes writes an element id with its marker bits intact (ids are 1 to 4
// bytes and already carry the length marker).
func idBytes(id uint64) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// sizeBytes writes an EBML variable-length size using the shortest form.
func sizeBytes(size uint64) []byte {
	for length := 1; length <= 8; length++ {
		// The all-ones value of a length is reserved for "unknown size".
		if size < (uint64(1)<<(7*uint(length)))-1 {
			out := make([]byte, length)
			value := size | (uint64(1) << (7 * uint(length)))
			for i := length - 1; i >= 0; i-- {
				out[i] = byte(value)
				value >>= 8
			}
			return out
		}
	}
	panic("mkvfixture: element too large")
}
