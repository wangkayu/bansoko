package application

import "testing"

func TestProjectSeedIdentity(t *testing.T) {
	source := []byte(`{"group":{"state":"known","value":" NEST "},"provider":{"state":"known","value":"mikan"},
"infohash_v1":{"state":"absent","value":null},"infohash_v2":{"state":"known","value":"abc"},"torrent_size":{"state":"known","value":1500}}`)
	release := []byte(`{"title_tags":{"tags":["resolution:1080p"],"value_tags":["group:NEST","version:v2"]},"anibt":null,"published_at":"2026-10-01T00:00:00Z"}`)
	seed := projectSeedIdentity(source, release)
	if seed == nil || seed.GroupName == nil || *seed.GroupName != "NEST" || *seed.Provider != "mikan" || *seed.SizeBytes != 1500 ||
		*seed.InfoHash != "abc" || *seed.ReleaseVersion != "v2" || len(seed.TitleTags) != 1 || len(seed.AniBTTags) != 0 || seed.PublishedAt == nil {
		t.Fatalf("seed = %+v", seed)
	}
	unnamed := []byte(`{"group":{"state":"known","value":"unknown"},"provider":{"state":"absent","value":null}}`)
	if got := projectSeedIdentity(unnamed, nil); got != nil {
		t.Fatalf("a placeholder group with nothing else reads as no identity: %+v", got)
	}
	if got := projectSeedIdentity(nil, []byte("not json")); got != nil {
		t.Fatalf("undecodable facts read as absent: %+v", got)
	}
}
