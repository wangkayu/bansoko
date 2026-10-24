package domain

import (
	"errors"
	"strings"

	providerdomain "bansoko/internal/provider/domain"
)

type LogicalGroupKind string

const (
	LogicalGroupProvider LogicalGroupKind = "provider"
	LogicalGroupCustom   LogicalGroupKind = "custom_rss"
)

// LogicalGroupIdentity is a strict sum type. Provider group identities and
// user-declared custom group names never substitute for one another.
type LogicalGroupIdentity struct {
	Kind            LogicalGroupKind
	ProviderGroup   *providerdomain.GroupRef
	CustomGroupName string
}

func (i LogicalGroupIdentity) Validate() error {
	switch i.Kind {
	case LogicalGroupProvider:
		if i.ProviderGroup == nil || strings.TrimSpace(i.CustomGroupName) != "" {
			return errors.New("provider logical group requires only a provider group reference")
		}
		return i.ProviderGroup.Validate()
	case LogicalGroupCustom:
		if i.ProviderGroup != nil || strings.TrimSpace(i.CustomGroupName) == "" || len(i.CustomGroupName) > 256 {
			return errors.New("custom RSS logical group requires only a group name of at most 256 bytes")
		}
	default:
		return errors.New("logical group kind is required")
	}
	return nil
}
