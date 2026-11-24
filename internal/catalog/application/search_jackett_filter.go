package application

import (
	"strconv"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

// Jackett active category blacklist: Jackett's aggregate `all` endpoint cannot
// reliably exclude non-video or raw-disc releases upstream, and several anime
// trackers (e.g. U2) co-tag BDMV, DVDISO, Modded Blu-rays and external disc
// structures with standard `5070` (TV/Anime). Filtering therefore checks every
// category on an item across two layers:
//
//  1. Standard Torznab numeric ranges (1000..1999 Console, 3000..3999 Audio
//     except 3020 Audio/Video, 4000..4999 PC, 7000..7999 Books). Evaluated on
//     raw SeedCategoryIDs so it works even if per-indexer caps cannot be read.
//  2. Caps-resolved category names (including 100000+ custom tracker sections)
//     for raw discs, external disc structures, books/comics/image packs, and
//     pure audio/software/games. Site-ID agnostic so newly added trackers
//     inherit the same policy without configuration.
//
// Video categories (anime and non-anime movies, TV, documentaries, variety,
// live concerts, MV, and `8000` Other) are kept; items with no categories fail
// open and are kept.

const torznabCategoryAudioVideo = 3020

var jackettBlacklistedCJKTerms = []string{
	// Raw optical discs & modded disc structures.
	"原盘", "原盤", "加流重灌",
	// External disc / subtitle structures.
	"外挂结构", "外掛結構", "外挂字幕", "外掛字幕",
	// Books, comics, light novels, magazines, artbooks & image packs.
	"漫画", "漫畫", "电子书", "電子書", "轻小说", "輕小說", "小说", "小說",
	"绘本", "繪本", "画集", "畫集", "图包", "圖包", "杂志", "雜誌",
	"书籍", "書籍", "文学", "文學",
	// Pure audio, software & games.
	"音乐", "音樂", "有声书", "有聲書", "软件", "軟件", "游戏", "遊戲",
}

var jackettBlacklistedASCIIPhrases = []string{
	"modded blu ray",
	"modded blu rays",
	"modded bluray",
	"modded blurays",
	"full disc",
	"full bd",
	"full bluray",
	"full blu ray",
	"bd iso",
	"dvd iso",
	"uhd disc",
	"blu ray disc",
	"bluray disc",
	"e book",
	"e books",
}

var jackettBlacklistedASCIITokens = map[string]struct{}{
	// Raw discs.
	"bdmv": {}, "bdiso": {}, "dvdiso": {}, "uhdiso": {}, "iso": {},
	"dvd5": {}, "dvd9": {}, "bd25": {}, "bd50": {}, "bd66": {}, "bd100": {},
	// External subtitle-only sections.
	"subtitle": {}, "subtitles": {},
	// Books, comics, literature & image sets.
	"book": {}, "books": {}, "ebook": {}, "ebooks": {},
	"comic": {}, "comics": {}, "manga": {}, "mangas": {},
	"literature": {}, "novel": {}, "novels": {},
	"magazine": {}, "magazines": {}, "mags": {},
	"artbook": {}, "artbooks": {}, "picture": {}, "pictures": {},
	"photo": {}, "photos": {}, "graphics": {}, "imageset": {},
	// Pure audio formats (unconditionally non-video).
	"audiobook": {}, "audiobooks": {}, "mp3": {}, "flac": {},
	// Software & games.
	"game": {}, "games": {}, "software": {},
	"application": {}, "applications": {},
	"console": {}, "pc": {}, "0day": {},
}

// jackettBlacklistedAudioTokens match pure audio categories unless the same
// category name also marks video content (`video` or `mv`, e.g. `Audio/Video`,
// `Anime - Anime Music Video`).
var jackettBlacklistedAudioTokens = map[string]struct{}{
	"audio": {}, "music": {}, "ost": {}, "soundtrack": {},
}

func filterJackettItemsByCategory(items []acquisitiondomain.FeedItem) []acquisitiondomain.FeedItem {
	if len(items) == 0 {
		return items
	}
	kept := make([]acquisitiondomain.FeedItem, 0, len(items))
	for _, item := range items {
		if isJackettItemBlacklistedByCategory(item) {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func isJackettItemBlacklistedByCategory(item acquisitiondomain.FeedItem) bool {
	for _, rawID := range item.SeedCategoryIDs {
		if isBlacklistedJackettCategoryID(rawID) {
			return true
		}
	}
	for _, cat := range item.SeedCategories {
		if isBlacklistedJackettCategoryID(cat.ID) || isBlacklistedJackettCategoryName(cat.Name) {
			return true
		}
	}
	return false
}

func isBlacklistedJackettCategoryID(rawID string) bool {
	id, err := strconv.Atoi(strings.TrimSpace(rawID))
	if err != nil || id <= 0 {
		return false
	}
	switch {
	case id >= 1000 && id < 2000:
		return true
	case id >= 3000 && id < 4000:
		return id != torznabCategoryAudioVideo
	case id >= 4000 && id < 5000:
		return true
	case id >= 7000 && id < 8000:
		return true
	default:
		return false
	}
}

func isBlacklistedJackettCategoryName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, term := range jackettBlacklistedCJKTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	tokens := asciiCategoryTokens(lower)
	if len(tokens) == 0 {
		return false
	}
	hasVideoToken := false
	for _, token := range tokens {
		if token == "video" || token == "mv" {
			hasVideoToken = true
			break
		}
	}
	for _, token := range tokens {
		if _, blocked := jackettBlacklistedASCIITokens[token]; blocked {
			return true
		}
		if !hasVideoToken {
			if _, audioBlocked := jackettBlacklistedAudioTokens[token]; audioBlocked {
				return true
			}
		}
	}
	if len(tokens) >= 2 {
		padded := " " + strings.Join(tokens, " ") + " "
		for _, phrase := range jackettBlacklistedASCIIPhrases {
			if strings.Contains(padded, " "+phrase+" ") {
				return true
			}
		}
	}
	return false
}

func asciiCategoryTokens(lower string) []string {
	var tokens []string
	start := -1
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		isWord := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if isWord {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			tokens = append(tokens, lower[start:i])
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, lower[start:])
	}
	return tokens
}
