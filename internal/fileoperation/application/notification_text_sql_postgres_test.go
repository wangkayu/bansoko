package application

import (
	"database/sql"
	"strings"
	"testing"
)

func TestNotificationTextSQLParity(t *testing.T) {
	_, _ = preparedPlanPGFixture(t)
	db := ownerTestDB(t)
	values := []string{"", "Show", "  Show  ", "中文标题", "/home/media", "../media", "a/b", "secret", "password=x", "bearer abc", "payload", "panic: x", "https://example.invalid", "magnet:abc", "C:\\media", "\\\\host\\media", "name\nline", "\u00a0Title\u00a0", strings.Repeat("a", 512), strings.Repeat("a", 513)}
	for _, space := range []rune{'\t', '\n', '\r', '\v', '\f', 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000} {
		values = append(values, string(space)+"Title"+string(space))
	}
	for r := rune(1); r <= 0x9f; r++ {
		if r < 0x20 || r >= 0x7f {
			values = append(values, "before"+string(r)+"after")
		}
	}
	for _, marker := range []string{"api_key=x", "api-key:x", "apikey=x", "passkey=x", "cookie=x", "credential=x", "credentials=x", "private_key=x", "authorization=x", "access_token=x", "client_secret=x", "access_key=x", "secret_key=x", "dsn=x", "TOKEN", "mytoken", "tokenized", "bearer", "bearerish", "~/file", "./file", "text /file", "text ../file", "payloading", "PANIC: x"} {
		values = append(values, marker)
	}
	values = append(values, strings.Repeat("中", 170), strings.Repeat("中", 171))
	// Migration 187: the B5c path rule. Alias slashes stay text, real paths
	// after a space, value punctuation or a full-width colon are masked.
	values = append(values, "[7³ACG] 【我推的孩子】/Oshi no Ko S01 [01-11][1080p]", "Title /Alias", "Fate/stay night", "间谍过家家/SPY×FAMILY/スパイ", "目标：/library/Show/ep.mkv", "see (/opt/x)", "x /tmp", "x /tmpfoo", "x /data-1", "x /a/b", "x　/a/b", "x /Users", "path=/srv/x")
	for _, value := range values {
		var got sql.NullString
		if err := db.QueryRow(`SELECT public.file_notification_text($1)`, value).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := notificationText(value)
		if want == nil {
			if got.Valid {
				t.Fatalf("%q sql=%q go=nil", value, got.String)
			}
		} else if !got.Valid || got.String != want.(string) {
			t.Fatalf("%q sql=%v go=%q", value, got, want)
		}
	}
}
