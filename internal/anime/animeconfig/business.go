package animeconfig

import animedomain "bansoko/internal/anime/domain"

// The domain owns the frozen value so AnimeDraft can carry it without a cycle.
type BusinessSnapshot = animedomain.BusinessSnapshot
type ReplacementValue = animedomain.ReplacementValue
type ProgramReferences = animedomain.ProgramReferences
