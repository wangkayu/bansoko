package domain

import "testing"

func TestParseAniBTTorrentURLField(t *testing.T) {
	rss := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:anibt="https://anibt.net/xmlns/rss/1.0/">
  <channel>
    <item>
      <title>[Nix-Raws] Test Episode 06</title>
      <link>https://anibt.net/release/rel_test123</link>
      <guid>rel_test123</guid>
      <anibt:torrentUrl>https://anibt.net/api/torrent/rel_test123.torrent</anibt:torrentUrl>
      <anibt:infohash>abc123def456</anibt:infohash>
    </item>
  </channel>
</rss>`

	result, err := ParseFeedDetailed([]byte(rss))
	if err != nil {
		t.Fatalf("ParseFeedDetailed failed: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(result.Items))
	}

	item := result.Items[0]
	expected := "https://anibt.net/api/torrent/rel_test123.torrent"
	if item.TorrentURL != expected {
		t.Errorf("TorrentURL: got %q, want %q", item.TorrentURL, expected)
	}
	if item.TorrentInfoHash != "abc123def456" {
		t.Errorf("TorrentInfoHash: got %q, want %q", item.TorrentInfoHash, "abc123def456")
	}
	if item.TorrentMetadataURL() != expected {
		t.Errorf("TorrentMetadataURL: got %q, want %q", item.TorrentMetadataURL(), expected)
	}
}
