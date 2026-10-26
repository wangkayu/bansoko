package domain

import "testing"

func TestSelectSingleEnabledConnectionUsesSavedKindWithoutImplicitChoice(t *testing.T) {
	connections := []Connection{
		{ID: "disabled", Kind: ConnectionAniBT, State: ConnectionDisabled},
		{ID: "mikan", Kind: ConnectionMikan, State: ConnectionConfigured},
		{ID: "anibt", Kind: ConnectionAniBT, State: ConnectionDegraded},
	}
	selected, err := SelectSingleEnabledConnection(connections, ConnectionAniBT)
	if err != nil || selected.ID != "anibt" {
		t.Fatalf("single enabled connection was not selected: %+v err=%v", selected, err)
	}
	connections = append(connections, Connection{ID: "anibt-2", Kind: ConnectionAniBT, State: ConnectionConfigured})
	if _, err := SelectSingleEnabledConnection(connections, ConnectionAniBT); err == nil {
		t.Fatal("multiple enabled connections were selected implicitly")
	}
	if _, err := SelectSingleEnabledConnection(connections, ConnectionTMDB); err == nil {
		t.Fatal("missing enabled connection was accepted")
	}
}

func TestDefaultConnectionForKindReturnsOfficialEndpointWithDirectRoute(t *testing.T) {
	cases := []struct {
		kind             ConnectionKind
		wantEndpoint     string
		wantNetworkRoute string
	}{
		{ConnectionMikan, "https://mikanani.me", ""},
		{ConnectionAniBT, "https://anibt.net", ""},
		{ConnectionNyaa, "https://nyaa.si", ""},
		{ConnectionBGM, "https://api.bgm.tv", ""},
		{ConnectionTMDB, "https://api.themoviedb.org/3", ""},
	}
	for _, tc := range cases {
		connection, ok := DefaultConnectionForKind(tc.kind)
		if !ok {
			t.Fatalf("%s default connection must be available", tc.kind)
		}
		if connection.Endpoint != tc.wantEndpoint {
			t.Fatalf("%s default endpoint = %q, want %q", tc.kind, connection.Endpoint, tc.wantEndpoint)
		}
		if connection.NetworkRouteID != tc.wantNetworkRoute {
			t.Fatalf("%s default route = %q, want empty direct route", tc.kind, connection.NetworkRouteID)
		}
		if connection.State != ConnectionConfigured {
			t.Fatalf("%s default state = %q, want configured", tc.kind, connection.State)
		}
	}
	if _, ok := DefaultConnectionForKind(ConnectionQBittorrent); ok {
		t.Fatal("qBittorrent must not have a code-default fallback; it requires explicit configuration")
	}
	if _, ok := DefaultConnectionForKind(ConnectionHTTPFeed); ok {
		t.Fatal("rss_http must not have a code-default fallback; it requires explicit configuration")
	}
}

func TestDefaultConnectionSeedsListsEveryProviderMetadataKind(t *testing.T) {
	seeds := DefaultConnectionSeeds()
	seen := make(map[ConnectionKind]bool, len(seeds))
	for _, seed := range seeds {
		if seed.ID == "" || seed.Name == "" || seed.Endpoint == "" {
			t.Fatalf("default connection seed %+v must have id, name and endpoint", seed)
		}
		if seed.Endpoint != DefaultEndpointForKind(seed.Kind) {
			t.Fatalf("seed endpoint for %s = %q, want %q", seed.Kind, seed.Endpoint, DefaultEndpointForKind(seed.Kind))
		}
		if seen[seed.Kind] {
			t.Fatalf("default connection seed kind %s is duplicated", seed.Kind)
		}
		seen[seed.Kind] = true
	}
	for _, kind := range []ConnectionKind{ConnectionMikan, ConnectionAniBT, ConnectionNyaa, ConnectionBGM, ConnectionTMDB} {
		if !seen[kind] {
			t.Fatalf("default connection seed list is missing kind %s", kind)
		}
	}
	for _, kind := range []ConnectionKind{ConnectionQBittorrent, ConnectionHTTPFeed} {
		if seen[kind] {
			t.Fatalf("default connection seed list must not include %s; it requires explicit configuration", kind)
		}
	}
}
