package domain

import (
	"strconv"
	"strings"
)

// GroupEntry is one provider subgroup that a source outside the subgroup policy
// resolved: the provider-namespaced key, the display name the provider
// publishes, and whether the provider currently serves it. It is
// transport-neutral; the HTTP boundary projects it onto the generated contract.
//
// DisplayName is display data only and is never persisted as an identity. It is
// empty when the source publishes no name for the key.
type GroupEntry struct {
	Ref         GroupRef
	DisplayName string
	Available   bool
}

// GroupQueryMatches reports whether one provider subgroup matches a free-form
// group query: the display name matches as a case-insensitive substring, a
// decimal query also matches a Mikan subgroup id as a prefix, and an AniBT slug
// matches as a substring. An empty query matches every group, which is what
// browsing one layer of the subgroup dictionary needs.
//
// The rule lives beside NewGroupRef because what a query can name is provider
// identity: a Mikan key is a number, so only a number can be an id prefix,
// while an AniBT key is a slug. Both the collected snapshot and the stored user
// layer are filtered by this one rule.
func GroupQueryMatches(provider Kind, key, name, query string) bool {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	key = strings.TrimSpace(key)
	if name = strings.TrimSpace(name); name != "" && strings.Contains(strings.ToLower(name), needle) {
		return true
	}
	if provider == ProviderMikan {
		return isDecimal(needle) && strings.HasPrefix(key, needle)
	}
	return strings.Contains(strings.ToLower(key), needle)
}

// GroupKeyLess orders two provider subgroup keys: numerically when both are
// decimal, so the Mikan ids 58 and 612 read 58 first, and lexicographically
// otherwise. Keys of two different providers are never compared.
func GroupKeyLess(left, right string) bool {
	leftID, leftErr := strconv.ParseUint(strings.TrimSpace(left), 10, 64)
	rightID, rightErr := strconv.ParseUint(strings.TrimSpace(right), 10, 64)
	if leftErr == nil && rightErr == nil {
		return leftID < rightID
	}
	return left < right
}

func isDecimal(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}
