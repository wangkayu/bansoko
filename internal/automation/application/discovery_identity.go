package application

import (
	providerdomain "bansoko/internal/provider/domain"
	"errors"
)

func validateDiscoveryIdentity(result providerdomain.GroupReadResult, provider, entry string) error {
	if string(result.Provider) != provider || result.CatalogEntryRef != entry {
		return errors.New("provider discovery identity mismatch")
	}
	seen := map[string]bool{}
	for _, group := range result.Groups {
		if err := group.Ref.Validate(); err != nil {
			return err
		}
		if string(group.Ref.Provider) != provider {
			return errors.New("group provider identity mismatch")
		}
		if seen[group.Ref.Key()] {
			return errors.New("duplicate provider group identity")
		}
		seen[group.Ref.Key()] = true
	}
	return nil
}
