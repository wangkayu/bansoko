package domain

import (
	"fmt"

	"bansoko/internal/qualityprofile"
)

func validateBusinessReplacement(value ReplacementValue) error {
	if _, err := qualityprofile.Parse(value.QualityProfile); err != nil {
		return fmt.Errorf("quality profile: %w", err)
	}
	return nil
}
