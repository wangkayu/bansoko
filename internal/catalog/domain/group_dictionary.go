package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// GroupDictionarySource names the layer a subgroup dictionary entry belongs to.
// The collected layer is the snapshot published with the release: it is
// reference data, never written, and cannot be removed. The user layer is the
// rows a user retrieved from the provider and added; those rows can be removed.
type GroupDictionarySource string

const (
	GroupDictionarySourceCollected GroupDictionarySource = "collected"
	GroupDictionarySourceUser      GroupDictionarySource = "user"
)

// ResolvedGroup is one provider subgroup resolved from a source outside the
// subgroup policy: the build-embedded collected snapshot, or a live provider
// read. Provider and GroupRef are the identity; DisplayName is display data
// that is never stored as identity and may be empty when the source publishes
// no name.
type ResolvedGroup struct {
	Provider    string
	GroupRef    string
	DisplayName string
	Available   bool
}

// GroupDictionaryEntry is one subgroup dictionary entry as the settings surface
// reads it: a resolved group plus the layer it came from. AddedAt is the zero
// time for the collected layer.
//
// The dictionary is a candidate set, not policy: a subgroup list stores only
// the key of an entry the user picked.
type GroupDictionaryEntry struct {
	Provider    string
	GroupRef    string
	DisplayName string
	Available   bool
	Source      GroupDictionarySource
	AddedAt     time.Time
}

// Key is the entry's cross-provider identity.
func (e GroupDictionaryEntry) Key() string {
	return e.Provider + ":" + e.GroupRef
}

func (e GroupDictionaryEntry) Validate() error {
	if strings.TrimSpace(e.Provider) == "" || strings.TrimSpace(e.GroupRef) == "" {
		return errors.New("group dictionary entry provider and group reference are required")
	}
	if len(e.GroupRef) > 256 || len(e.DisplayName) > 256 {
		return errors.New("group dictionary entry identity and display name are bounded to 256 characters")
	}
	switch e.Source {
	case GroupDictionarySourceCollected, GroupDictionarySourceUser:
	default:
		return fmt.Errorf("unsupported group dictionary source %q", e.Source)
	}
	return nil
}
