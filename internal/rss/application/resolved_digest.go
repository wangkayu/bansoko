package application

import (
	anime "bansoko/internal/anime/domain"
	"fmt"
)

func resolvedDigestChanged(before, after anime.ResolvedSections) (bool, error) {
	oldDigest, err := before.EffectiveDigest()
	if err != nil {
		return false, fmt.Errorf("digest handler resolved config: %w", err)
	}
	newDigest, err := after.EffectiveDigest()
	if err != nil {
		return false, fmt.Errorf("digest locked resolved config: %w", err)
	}
	return oldDigest != newDigest, nil
}
