package mkvfixture

import (
	"bytes"
	"testing"
)

// readSize decodes the EBML variable-length size at data[0].
func readSize(t *testing.T, data []byte) (size uint64, length int) {
	t.Helper()
	first := data[0]
	for length = 1; length <= 8; length++ {
		if first&(0x80>>uint(length-1)) != 0 {
			break
		}
	}
	if length > 8 {
		t.Fatalf("invalid size marker %#x", first)
	}
	size = uint64(first) & ((0x80 >> uint(length-1)) - 1)
	for _, b := range data[1:length] {
		size = size<<8 | uint64(b)
	}
	return size, length
}

func TestBuildProducesAWellFormedContainer(t *testing.T) {
	data := HEVC1080pAAC("")
	if !bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		t.Fatalf("missing the EBML magic: % x", data[:4])
	}
	// The EBML header is followed by a Segment whose declared size covers the
	// rest of the file exactly.
	headerSize, headerLen := readSize(t, data[4:])
	segmentAt := 4 + headerLen + int(headerSize)
	if !bytes.HasPrefix(data[segmentAt:], []byte{0x18, 0x53, 0x80, 0x67}) {
		t.Fatalf("no Segment after the header: % x", data[segmentAt:segmentAt+4])
	}
	segmentSize, segmentLen := readSize(t, data[segmentAt+4:])
	if got, want := int(segmentSize), len(data)-segmentAt-4-segmentLen; got != want {
		t.Fatalf("segment size = %d, want %d", got, want)
	}
	for _, id := range []string{"V_MPEGH/ISO/HEVC", "A_AAC"} {
		if !bytes.Contains(data, []byte(id)) {
			t.Fatalf("codec id %q is missing", id)
		}
	}
}

func TestBuildOmitsTracksWithoutACodec(t *testing.T) {
	data := Build(Spec{Width: 1280, Height: 720, VideoCodecID: "V_MPEG4/ISO/AVC"})
	if bytes.Contains(data, []byte("A_AAC")) {
		t.Fatal("an audio track appeared without an audio codec")
	}
}

func TestSizeBytesUsesTheShortestFormAndSkipsTheReservedValue(t *testing.T) {
	for size, want := range map[uint64][]byte{
		0:     {0x80},
		126:   {0xFE},
		127:   {0x40, 0x7F}, // 0x7F in one byte would mean "unknown size"
		16382: {0x7F, 0xFE},
	} {
		if got := sizeBytes(size); !bytes.Equal(got, want) {
			t.Fatalf("sizeBytes(%d) = % x, want % x", size, got, want)
		}
	}
}

func TestTitleMakesFixturesDiffer(t *testing.T) {
	a, b := HEVC1080pAAC("run a"), HEVC1080pAAC("run b")
	if bytes.Equal(a, b) {
		t.Fatal("different titles produced identical bytes")
	}
	if !bytes.Contains(a, []byte("run a")) {
		t.Fatal("the title is missing")
	}
}
