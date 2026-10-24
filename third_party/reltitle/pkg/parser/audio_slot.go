package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// extractTechAudioSlotAndRemoveFromTokens scans bracket/paren blocks and plain
// token segments for audio information, emits audio attributes, removes the
// consumed tokens, and returns the structured audio slot. The extraction
// machinery (segment collection, track parsing, language backfill) lives in
// audio_slot_extract.go.
func extractTechAudioSlotAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute, audioCodecCanonical map[string]string) *model.AudioSlot {
	if len(tokens) == 0 || attrs == nil {
		return nil
	}
	e := &audioSlotExtract{tokens: tokens, attrs: attrs, audioCodecCanonical: audioCodecCanonical}
	e.techStart, e.hasTechStart = firstTechTokenIndexFromAttrs(*attrs)
	if !e.hasTechStart {
		e.techStart = 0
	}
	allTracks := e.collectSegments()
	if len(allTracks) == 0 {
		return nil
	}
	e.backfillLanguage(allTracks)
	return &model.AudioSlot{Tracks: allTracks}
}

func audioCodecSpecificityRank(norm string) int {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(norm), "-", ""))
	switch n {
	case "DTSHDMA":
		return 5
	case "DTSHD", "TRUEHD":
		return 4
	case "EAC3":
		return 3
	case "DTS", "AC3", "AAC", "FLAC", "OPUS", "PCM", "MP3":
		return 2
	default:
		return 1
	}
}

func collapseOverlappingSpecificAudioCodecs(attrs []model.Attribute) []model.Attribute {
	if len(attrs) <= 1 {
		return attrs
	}
	drop := make([]bool, len(attrs))
	for i, a := range attrs {
		if a.Type != model.AttrAudioCodec {
			continue
		}
		if a.TokenStart < 0 || a.TokenEnd <= a.TokenStart {
			continue
		}
		ra := audioCodecSpecificityRank(a.Normalized)
		for j, b := range attrs {
			if i == j || b.Type != model.AttrAudioCodec {
				continue
			}
			if b.TokenStart < 0 || b.TokenEnd <= b.TokenStart {
				continue
			}
			rb := audioCodecSpecificityRank(b.Normalized)
			if rb <= ra {
				continue
			}
			if b.TokenStart <= a.TokenStart && b.TokenEnd >= a.TokenEnd {
				drop[i] = true
				break
			}
		}
	}
	kept := make([]model.Attribute, 0, len(attrs))
	for i, a := range attrs {
		if drop[i] {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}

func reconcileAudioSlotWithAttributes(slot *model.AudioSlot, attrs []model.Attribute) *model.AudioSlot {
	if slot == nil || len(slot.Tracks) == 0 || len(attrs) == 0 {
		return slot
	}
	canonCodec := func(s string) string {
		return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	}
	audioAttrs := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if a.Type != model.AttrAudioCodec {
			continue
		}
		if a.TokenStart < 0 || a.TokenEnd <= a.TokenStart {
			continue
		}
		audioAttrs = append(audioAttrs, a)
	}
	if len(audioAttrs) == 0 {
		return slot
	}
	out := make([]model.AudioTrack, 0, len(slot.Tracks))
	for _, tr := range slot.Tracks {
		if tr.TokenStart < 0 || tr.TokenEnd <= tr.TokenStart {
			out = append(out, tr)
			continue
		}
		keep := true
		trackRank := audioCodecSpecificityRank(tr.Codec)
		for _, a := range audioAttrs {
			if a.TokenStart <= tr.TokenStart && a.TokenEnd >= tr.TokenEnd && audioCodecSpecificityRank(a.Normalized) > trackRank {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, tr)
		}
	}
	for _, a := range audioAttrs {
		found := false
		for _, tr := range out {
			if canonCodec(tr.Codec) == canonCodec(a.Normalized) && tr.TokenStart == a.TokenStart && tr.TokenEnd == a.TokenEnd {
				found = true
				break
			}
		}
		if found {
			continue
		}
		out = append(out, model.AudioTrack{
			Codec:      a.Normalized,
			TokenStart: a.TokenStart,
			TokenEnd:   a.TokenEnd,
			Source:     model.AudioSourceAudioSlotReconcile,
		})
	}
	if len(out) == 0 {
		return nil
	}
	slot.Tracks = out
	return slot
}
