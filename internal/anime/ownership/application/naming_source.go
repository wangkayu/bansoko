package application

import (
	"context"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
)

// EffectiveNamingRules returns the effective naming owner value for one managed
// anime. It reads only the rules owner, sparse overrides, and global sections;
// unlike the complete workspace read model, it does not require a Subscription.
func (s *Service) EffectiveNamingRules(ctx context.Context, managedAnimeID string) (naming.NamingRules, error) {
	rules, err := s.readRulesOwnerApplication(ctx, managedAnimeID)
	if err != nil {
		return naming.NamingRules{}, err
	}
	return animedomain.NamingRulesToDomain(rules.Effective.Naming), nil
}

// EffectiveCollisionPolicy returns the instance's preset file collision mode
// as the file executor policy: block (cancel), version or overwrite. Companion
// alignment uses it for a companion target that already holds an
// unmanaged file.
func (s *Service) EffectiveCollisionPolicy(ctx context.Context, managedAnimeID string) (string, error) {
	rules, err := s.readRulesOwnerApplication(ctx, managedAnimeID)
	if err != nil {
		return "", err
	}
	switch rules.Effective.FileEffect.CollisionPolicy {
	case animedomain.CollisionPolicyVersioned:
		return "version", nil
	case animedomain.CollisionPolicyOverwrite:
		return "overwrite", nil
	default:
		return "block", nil
	}
}
