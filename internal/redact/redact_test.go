package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsSensitiveMarkers(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"url with userinfo", "fetch https://user:pass@example.test/private"},
		{"plain https url", "see https://example.test/feed for details"},
		{"any scheme url", "connect postgres://user:pass@db.internal/app"},
		{"magnet link", "magnet:?xt=urn:btih:deadbeef"},
		{"api_key assignment", "request failed api_key=AKIAIOSFODNN7EXAMPLE"},
		{"api_key json shape", `payload {"api_key":"AKIAIOSFODNN7EXAMPLE"}`},
		{"apikey assignment", "provider replied apikey:abcdef0123456789"},
		{"passkey assignment", "download passkey=0123456789abcdef"},
		{"cookie assignment", "header cookie:session=opaque"},
		{"credential assignment", "credential=svc-account"},
		{"credentials assignment", "credentials : svc-account"},
		{"private_key assignment", "private_key=-----BEGIN PRIVATE KEY-----"},
		{"password assignment", "reason password=hunter2"},
		{"token assignment", "query token=abcdef"},
		{"secret assignment", "body secret=abcdef"},
		{"authorization header", "header authorization:Bearer abcdef"},
		{"access token assignment", "access_token=abcdef"},
		{"client secret assignment", "client_secret=abcdef"},
		{"access key assignment", "access-key=abcdef"},
		{"secret key assignment", "secret_key=abcdef"},
		{"dsn assignment", "dsn=postgres://user:pass@host/db"},
		{"bearer word", "upstream returned Bearer abcdef for the refresh"},
		{"unix home path", "failed at /home/alice/private/torrent"},
		{"unix users path", "stored under /Users/alice/Downloads"},
		{"unix private path", "anchor /private/var/folders missing"},
		{"unix downloads path", "source /downloads/show/file.mkv"},
		{"unix library path", "target /library/show/file.mkv"},
		{"unix path after bracket", "see (/opt/app/config) for context"},
		{"windows drive path", `open C:\Users\alice\Downloads\show.mkv`},
		{"windows drive forward slash", "open D:/data/private/show.mkv"},
		{"unc path", `copy \\nas\share\private\show.mkv`},
		{"tilde relative path", "config ~/.config/app/settings.toml"},
		{"dotdot relative path", "escape via ../secrets/key.pem"},
		{"dot relative path", "loaded ./secrets/key.pem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.value, 256); got != Redacted {
				t.Fatalf("Text(%q) = %q, want %q", tc.value, got, Redacted)
			}
		})
	}
}

func TestTextKeepsOrdinaryText(t *testing.T) {
	cases := []string{
		"RSS 刷新完成，未发现新内容",
		"Season 2 第 3 集已入库",
		"release filtered by group rule",
		"score 12/20 above cutoff",
		"and/or group selection",
		"v2.1.4 migration applied",
		"reason code group_filtered",
		"SampleGroup 字幕组作品",
	}
	for _, value := range cases {
		if got := Text(value, 256); got != value {
			t.Fatalf("Text(%q) = %q, want the value unchanged", value, got)
		}
	}
}

func TestTextTruncatesByRunesSafely(t *testing.T) {
	// Rune truncation must never split a multi-byte sequence.
	value := strings.Repeat("番", 300)
	got := Text(value, 256)
	if got != strings.Repeat("番", 256) {
		t.Fatalf("rune truncation mismatch: got %d runes", len([]rune(got)))
	}

	mixed := strings.Repeat("ab番", 200)
	got = Text(mixed, 256)
	if len([]rune(got)) != 256 {
		t.Fatalf("mixed truncation must keep exactly 256 runes, got %d", len([]rune(got)))
	}

	// A truncation cut must leave valid UTF-8 behind.
	if !isValidUTF8String(got) {
		t.Fatalf("truncated value is not valid UTF-8: %q", got)
	}
}

func TestTextBoundsAndControls(t *testing.T) {
	if got := Text("hello", 0); got != "" {
		t.Fatalf("maxRunes 0 must yield empty, got %q", got)
	}
	if got := Text("hello", -3); got != "" {
		t.Fatalf("negative maxRunes must yield empty, got %q", got)
	}
	// Control characters fold to spaces and the value is trimmed.
	got := Text("  line one\r\nline two\x00tail  ", 256)
	if got != "line one  line two tail" {
		t.Fatalf("control characters not folded: %q", got)
	}
	// Invalid UTF-8 is repaired instead of crashing or leaking raw bytes.
	got = Text("bad \xff\xfe bytes", 256)
	if !isValidUTF8String(got) {
		t.Fatalf("invalid UTF-8 survived sanitisation: %q", got)
	}
	// Whitespace-only input collapses to empty, never to the redaction marker.
	if got := Text("   ", 256); got != "" {
		t.Fatalf("whitespace-only input must be empty, got %q", got)
	}
}

func isValidUTF8String(value string) bool {
	for _, r := range value {
		if r == '�' {
			continue
		}
		if r < 0 {
			return false
		}
	}
	return true
}

// TestTitleSlashesAreNotPrivatePaths pins the AniBT/Mikan title shapes that
// separate aliases with a slash. They are ordinary text in every policy.
func TestTitleSlashesAreNotPrivatePaths(t *testing.T) {
	for _, title := range []string{
		"[7³ACG] 【我推的孩子】/Oshi no Ko S01 [01-11][1080p][简繁英字幕]",
		"[北宇治字幕组] 葬送的芙莉莲/进击的巨人 [01][WebRip][1080p]",
		"[SampleGroup] Kaijuu 8-gou /怪獸8號 - 01 [1080P][Baha][WEB-DL]",
		"[Group] 间谍过家家/SPY×FAMILY/スパイファミリー - 12",
		"Fate/stay night [Unlimited Blade Works]",
		"[SampleGroup] 进击的巨人 - 03",
		"[Sub] Title [1080p]/[简繁内封]",
	} {
		for name, policy := range map[string]Policy{"reject": Reject, "mask": Mask, "fragments": MaskFragments} {
			got, matched := Apply(title, 512, policy, Redacted)
			if matched || got != title {
				t.Errorf("%s: Apply(%q) = %q matched=%v, want unchanged", name, title, got, matched)
			}
		}
	}
}

// TestMaskFragmentsKeepsSurroundingText proves that only the sensitive span is
// replaced and the secret itself never survives.
func TestMaskFragmentsKeepsSurroundingText(t *testing.T) {
	cases := []struct {
		name, value, want, secret string
	}{
		{"url", "[Group] Show - 01 http://tracker.example/announce?passkey=abc [1080p]", "[Group] Show - 01 <redacted> [1080p]", "passkey=abc"},
		{"https userinfo", "fetch https://user:pw-secret@example.test/x done", "fetch <redacted> done", "pw-secret"},
		{"home path", "[Group] Show /home/alice/leak.mkv - 01", "[Group] Show <redacted> - 01", "alice"},
		{"absolute path after colon", "移动失败：/library/Show/S01/ep.mkv 权限不足", "移动失败：<redacted> 权限不足", "/library/Show"},
		{"two segment path", "open /srv2/hidden/file: denied", "open <redacted> denied", "hidden"},
		{"token assignment", "Show token=abcdef0123 - 01", "Show <redacted> - 01", "abcdef0123"},
		{"bearer", "Show Bearer abc.def.ghi - 01", "Show <redacted> - 01", "abc.def.ghi"},
		{"api key json", `reply {"api_key":"AKIA0123"} end`, `reply {"<redacted>} end`, "AKIA0123"},
		{"magnet", "Show magnet:?xt=urn:btih:deadbeef 01", "Show <redacted> 01", "deadbeef"},
		{"windows path", `copy C:\Users\alice\x.mkv now`, "copy <redacted> now", "alice"},
		{"dot relative", "escape ../secret-dir/key.pem now", "escape <redacted> now", "secret-dir"},
		{"panic dump", "worker panic: goroutine 7 secret-state", "worker <redacted>", "secret-state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := Apply(tc.value, 512, MaskFragments, Redacted)
			if !matched {
				t.Fatalf("Apply(%q) did not report a match", tc.value)
			}
			if got != tc.want {
				t.Fatalf("Apply(%q) = %q, want %q", tc.value, got, tc.want)
			}
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q survived: %q", tc.secret, got)
			}
			if _, again := Apply(got, 512, Reject, ""); again {
				t.Fatalf("masked output still trips the detector: %q", got)
			}
		})
	}
}

// TestMaskFragmentsCoversEveryDetectorMarker keeps MaskFragments at least as
// strong as Mask over the shared marker corpus.
func TestMaskFragmentsCoversEveryDetectorMarker(t *testing.T) {
	for _, value := range []string{
		"fetch https://user:pass@example.test/private", "connect postgres://user:pass@db.internal/app",
		"request failed api_key=AKIAIOSFODNN7EXAMPLE", `payload {"api_key":"AKIAIOSFODNN7EXAMPLE"}`,
		"header cookie:session=opaque", "credentials : svc-account", "private_key=-----BEGIN PRIVATE KEY-----",
		"header authorization:Bearer abcdef", "upstream returned Bearer abcdef for the refresh",
		"failed at /home/alice/private/torrent", "stored under /Users/alice/Downloads",
		"see (/opt/app/config) for context", "open D:/data/private/show.mkv",
		`copy \\nas\share\private\show.mkv`, "config ~/.config/app/settings.toml",
		"loaded ./secrets/key.pem", "invalid token abcdef", "dsn missing", "x/home/y",
	} {
		got, matched := Apply(value, 4096, MaskFragments, Redacted)
		if !matched {
			t.Errorf("MaskFragments missed %q", value)
		}
		if _, again := Apply(got, 4096, Reject, ""); again {
			t.Errorf("MaskFragments(%q) = %q still sensitive", value, got)
		}
	}
}
