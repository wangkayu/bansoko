package application

import anime "bansoko/internal/anime/domain"

func metadataVersionOf(m qualificationMaterial) any {
	if m.config.kind == "owner" {
		return m.metadataVersion
	}
	return nil
}

// Held is a captured fact. Only a one-shot (a temporary owner that never
// enters the library) bypasses its release verdict; the automatic entry and the
// manual retry both respect it. Missing bindings stay missing in the frozen
// native input.
func admissionMetadataHold(c admissionCapture) anime.MetadataHold {
	return anime.MetadataHoldFromBindings(c.material.bgm, c.material.tmdb)
}
func bypassMetadataHold(mode string) bool {
	return mode == "one_shot_seed" || mode == "one_shot_snapshot"
}
