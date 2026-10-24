package organizingpolicy

// Binding is the deterministic binder's output for one subtitle in this
// Artifact. Binder ambiguity is preserved, never resolved by basename here.
type BindingState string

const (
	Bound     BindingState = "bound"
	Ambiguous BindingState = "ambiguous"
	Missing   BindingState = "missing"
)

type Subtitle struct {
	Ref      FileRef
	Eligible bool
}
type Binding struct {
	Subtitle FileRef
	Video    FileRef
	State    BindingState
}
type SubtitleDecision struct {
	Subtitle FileRef
	Video    FileRef
	Allowed  bool
	Reason   string
}

// AdmitSubtitles checks binder evidence against the filtered subtitle set and
// final video set (after movie/identity decisions). All refs must be from the
// same immutable Artifact. It emits evidence only, not an operation/dependency.
// Production wiring must lock the final video target and bind its verified
// operation/adoption evidence before admitting subtitle execution.
func AdmitSubtitles(subtitles []Subtitle, finalVideos []FileRef, bindings []Binding) []SubtitleDecision {
	videos := make(map[FileRef]bool, len(finalVideos))
	for _, ref := range finalVideos {
		if ref != "" {
			videos[ref] = true
		}
	}
	bySubtitle := make(map[FileRef]Binding, len(bindings))
	duplicates := map[FileRef]bool{}
	for _, b := range bindings {
		if _, exists := bySubtitle[b.Subtitle]; exists {
			duplicates[b.Subtitle] = true
		}
		bySubtitle[b.Subtitle] = b
	}
	result := make([]SubtitleDecision, 0, len(subtitles))
	for _, s := range subtitles {
		d := SubtitleDecision{Subtitle: s.Ref, Reason: "filtered"}
		if s.Eligible {
			b, exists := bySubtitle[s.Ref]
			switch {
			case duplicates[s.Ref]:
				d.Reason = "binding_ambiguous"
			case !exists || b.State == Missing:
				d.Reason = "binding_missing"
			case b.State == Ambiguous:
				d.Reason = "binding_ambiguous"
			case b.State != Bound:
				d.Reason = "binding_invalid"
			case !videos[b.Video]:
				d.Reason = "video_not_final"
			default:
				d.Video = b.Video
				d.Allowed = true
				d.Reason = "bound_final_video"
			}
		}
		result = append(result, d)
	}
	return result
}
