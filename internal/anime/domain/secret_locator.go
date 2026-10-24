package domain

import (
	"strings"

	"bansoko/internal/redact"
)

// SecretPlaceholderUnresolved is the field violation code of an RSS source
// locator that still holds the secret placeholder after resolution.
const SecretPlaceholderUnresolved = "SECRET_PLACEHOLDER_UNRESOLVED"

// DraftHasSecretPlaceholder reports whether any RSS source locator is the
// fixed placeholder a bearer without the secrets scope reads (SEC-033).
func DraftHasSecretPlaceholder(draft AnimeDraft) bool {
	for _, source := range draft.RssSources {
		if source.Locator == redact.SecretPlaceholder {
			return true
		}
	}
	return false
}

// ResolveSecretLocators puts the stored locator back into every RSS source of
// draft whose locator is the secret placeholder, taking it from the same
// source of current (the candidate's current revision). The same source is the
// one source of current with the same source_kind and, when the incoming
// source carries a locator_display, the same credential-free display; each
// stored source answers at most one placeholder. A placeholder with no such
// source, with more than one, or sharing its match with another placeholder
// stays in place and ValidateDraft then refuses it
// (SECRET_PLACEHOLDER_UNRESOLVED), so a guess is never written. The input
// draft is not modified.
func ResolveSecretLocators(draft AnimeDraft, current AnimeDraft) AnimeDraft {
	if !DraftHasSecretPlaceholder(draft) {
		return draft
	}
	sources := append([]RSSSourceDraft(nil), draft.RssSources...)
	matchOf := map[int]int{}
	claims := map[int]int{}
	for index, source := range sources {
		if source.Locator != redact.SecretPlaceholder {
			continue
		}
		match, matches := -1, 0
		for storedIndex, stored := range current.RssSources {
			if stored.SourceKind != source.SourceKind || stored.Locator == "" || stored.Locator == redact.SecretPlaceholder {
				continue
			}
			if source.LocatorDisplay != nil && strings.TrimSpace(*source.LocatorDisplay) != "" &&
				SanitizeLocatorDisplay(*source.LocatorDisplay) != LocatorDisplay(stored.Locator) {
				continue
			}
			match = storedIndex
			matches++
		}
		if matches == 1 {
			matchOf[index] = match
			claims[match]++
		}
	}
	for index, storedIndex := range matchOf {
		if claims[storedIndex] == 1 {
			sources[index].Locator = current.RssSources[storedIndex].Locator
		}
	}
	draft.RssSources = sources
	return draft
}
