package migrations

import "testing"

// A managed anime owns at most one subscription, with a closed state set and a
// required instance label for custom origins.
func TestSubscriptionConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("managed anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Title')`)
	p.accept("custom subscription", `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-1', 'anime-1', 'custom', 'Custom', 'active')`)
	p.reject("second subscription for the same anime",
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-2', 'anime-1', 'custom', 'Other', 'active')`)
	p.reject("invalid subscription state",
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-3', 'anime-1', 'custom', 'Other', 'paused')`)
	p.accept("second anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-2', 'Title 2')`)
	p.reject("custom subscription without instance label",
		`INSERT INTO subscriptions(id, managed_anime_id, origin, state) VALUES ('sub-4', 'anime-2', 'custom', 'active')`)
	p.reject("empty anime title",
		`INSERT INTO managed_anime(id, title) VALUES ('anime-3', '  ')`)
	p.accept("pending subscription", `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-5', 'anime-2', 'custom', 'Pending', 'pending')`)
	p.accept("pending starts", `UPDATE subscriptions SET state = 'active' WHERE id = 'sub-5'`)
}

// A subscription origin is one of the closed origin labels.
func TestSubscriptionOriginLabels(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("managed anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-origin', 'Title')`)
	p.accept("oneshot origin", `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-oneshot', 'anime-origin', 'oneshot', 'One shot', 'active')`)
	p.accept("second anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-origin-2', 'Title 2')`)
	p.reject("retired agent origin",
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-agent', 'anime-origin-2', 'agent', 'Agent', 'active')`)
}

// TMDB binding is atomic: a partial tuple is not persistable.
func TestAnimeMetadataTMDBBindingIsAtomic(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("managed anime", `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Title')`)
	p.reject("partial TMDB tuple",
		`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id) VALUES ('anime-1', '12345')`)
	p.accept("complete TMDB tuple",
		`INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season) VALUES ('anime-1', 'bgm-1', '12345', 'tv', 1)`)
}
