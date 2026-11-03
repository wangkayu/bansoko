package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/catalog/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/runtime/buildinfo"
	"github.com/PuerkitoBio/goquery"
)

// SynopsisReader is the optional per-provider port that reads one catalog
// entry's anime page for its synopsis and the facts the page states. It is
// separate from Reader so read-season fakes and future providers are not
// forced to implement it; the service answers a typed error when a configured
// provider lacks it.
type SynopsisReader interface {
	ReadSynopsis(ctx context.Context, providerEntryRef string) (domain.EntrySynopsis, error)
}

// maxSynopsisHTMLSize bounds one provider anime page. Mikan's page is tens of
// kilobytes and AniBT's is a few hundred, so this is generous while still
// refusing a runaway body.
const maxSynopsisHTMLSize = 8 << 20

// ReadSynopsis reads an AniBT entry's server-rendered anime page. The synopsis
// lives in the page's 「番剧简介」 section (the public API has no description),
// which is why this is a page parser rather than an API read.
func (r HTTPReader) ReadSynopsis(ctx context.Context, providerEntryRef string) (domain.EntrySynopsis, error) {
	providerEntryRef = strings.TrimSpace(providerEntryRef)
	if strings.TrimSpace(r.Endpoint) == "" || providerEntryRef == "" {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: AniBT catalog endpoint and entry reference are required", ErrProviderUpstream)
	}
	sourceURL, err := synopsisPageURL(r.Endpoint, "/anime/"+url.PathEscape(providerEntryRef))
	if err != nil {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: %v", ErrProviderUpstream, err)
	}
	document, err := readSynopsisDocument(ctx, r.Client, sourceURL, "AniBT")
	if err != nil {
		return domain.EntrySynopsis{}, err
	}
	result := domain.EntrySynopsis{
		Provider:         ProviderAniBT,
		ProviderEntryRef: providerEntryRef,
		SourceURL:        sourceURL,
		Synopsis:         anibtSynopsis(document),
		Episodes:         anibtEpisodeCount(document),
		Season:           anibtSeason(document),
		Tags:             anibtTags(document),
		OfficialSite:     anibtOfficialSite(document),
	}
	// AniBT's entry reference is the Bangumi subject id, so the Bangumi link is
	// assembled from it rather than borrowed from another provider.
	if parsed, parseErr := strconv.ParseUint(providerEntryRef, 10, 64); parseErr == nil && parsed > 0 {
		result.BGMURL = "https://bgm.tv/subject/" + strconv.FormatUint(parsed, 10)
	}
	return result, nil
}

// ReadSynopsis reads a Mikan entry's anime page. Mikan's synopsis is the
// 「概况介绍」 block followed by p.header2-desc; the page also states the
// broadcast weekday, total episode count, official site and Bangumi link.
func (r MikanHTTPReader) ReadSynopsis(ctx context.Context, providerEntryRef string) (domain.EntrySynopsis, error) {
	providerEntryRef = strings.TrimSpace(providerEntryRef)
	if strings.TrimSpace(r.Endpoint) == "" || providerEntryRef == "" {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: Mikan catalog endpoint and entry reference are required", ErrProviderUpstream)
	}
	sourceURL, err := synopsisPageURL(r.Endpoint, "/Home/Bangumi/"+url.PathEscape(providerEntryRef))
	if err != nil {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: %v", ErrProviderUpstream, err)
	}
	document, err := readSynopsisDocument(ctx, r.Client, sourceURL, "Mikan")
	if err != nil {
		return domain.EntrySynopsis{}, err
	}
	result := domain.EntrySynopsis{
		Provider:         ProviderMikan,
		ProviderEntryRef: providerEntryRef,
		SourceURL:        sourceURL,
		Synopsis:         mikanSynopsis(document),
	}
	document.Find("p.bangumi-info").Each(func(_ int, info *goquery.Selection) {
		label, value, ok := strings.Cut(strings.TrimSpace(info.Text()), "：")
		if !ok {
			return
		}
		label, value = strings.TrimSpace(label), strings.TrimSpace(value)
		switch label {
		case "放送日期":
			if result.Weekday == "" {
				result.Weekday = mikanWeekday(value)
			}
		case "总集数":
			if result.Episodes == nil {
				result.Episodes = positiveInt(value)
			}
		case "官方网站":
			if result.OfficialSite == "" {
				result.OfficialSite = firstHref(info)
			}
		case "Bangumi番组计划链接":
			if result.BGMURL == "" {
				result.BGMURL = firstHref(info)
			}
		}
	})
	return result, nil
}

func synopsisPageURL(endpoint, pagePath string) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return "", errors.New("provider endpoint must be an absolute HTTP URL without credentials")
	}
	base.Path = strings.TrimRight(base.Path, "/") + pagePath
	base.RawQuery = ""
	return base.String(), nil
}

func readSynopsisDocument(ctx context.Context, client *http.Client, sourceURL, site string) (*goquery.Document, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create %s synopsis request: %v", ErrProviderUpstream, site, err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch %s synopsis: %v", ErrProviderUpstream, site, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s synopsis returned HTTP %d", ErrProviderUpstream, site, response.StatusCode)
	}
	payload, err := readBoundedBody(response.Body, maxSynopsisHTMLSize)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s synopsis: %v", ErrProviderUpstream, site, err)
	}
	document, err := goquery.NewDocumentFromReader(strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s synopsis HTML: %v", ErrProviderUpstream, site, err)
	}
	return document, nil
}

func mikanSynopsis(document *goquery.Document) *string {
	value := strings.TrimSpace(document.Find("p.header2-desc").First().Text())
	if value == "" {
		return nil
	}
	return &value
}

func mikanWeekday(value string) string {
	switch strings.TrimSpace(value) {
	case "星期一":
		return "mon"
	case "星期二":
		return "tue"
	case "星期三":
		return "wed"
	case "星期四":
		return "thu"
	case "星期五":
		return "fri"
	case "星期六":
		return "sat"
	case "星期日", "星期天":
		return "sun"
	default:
		return ""
	}
}

func anibtSynopsis(document *goquery.Document) *string {
	var value string
	document.Find("h2").EachWithBreak(func(_ int, heading *goquery.Selection) bool {
		if strings.TrimSpace(heading.Text()) != "番剧简介" {
			return true
		}
		value = strings.TrimSpace(heading.NextAllFiltered("p").First().Text())
		return false
	})
	if value == "" {
		return nil
	}
	return &value
}

func anibtEpisodeCount(document *goquery.Document) *int {
	var episodes *int
	document.Find("span.tag.num").EachWithBreak(func(_ int, tag *goquery.Selection) bool {
		for _, field := range strings.Fields(tag.Text()) {
			if parsed := positiveInt(field); parsed != nil {
				episodes = parsed
				return false
			}
		}
		return true
	})
	return episodes
}

func anibtTags(document *goquery.Document) []string {
	tags := []string{}
	document.Find("span[title]").EachWithBreak(func(_ int, span *goquery.Selection) bool {
		if !strings.Contains(span.Text(), "标签") {
			return true
		}
		raw, ok := span.Attr("title")
		if !ok {
			return true
		}
		for _, part := range strings.Split(raw, "·") {
			if tag := strings.TrimSpace(part); tag != "" {
				tags = append(tags, tag)
			}
		}
		return false
	})
	return tags
}

func anibtOfficialSite(document *goquery.Document) string {
	site := ""
	document.Find("a").EachWithBreak(func(_ int, link *goquery.Selection) bool {
		if !strings.Contains(link.Text(), "官方网站") {
			return true
		}
		if href, ok := link.Attr("href"); ok {
			site = strings.TrimSpace(href)
			return false
		}
		return true
	})
	return site
}

func anibtSeason(document *goquery.Document) string {
	sticker := strings.TrimSpace(document.Find("span.anibt-detail-sticker").First().Text())
	if sticker == "" {
		return ""
	}
	yearEnd := strings.IndexFunc(sticker, func(r rune) bool { return r < '0' || r > '9' })
	if yearEnd != 4 {
		return ""
	}
	year := sticker[:4]
	term := map[string]string{"冬": "winter", "春": "spring", "夏": "summer", "秋": "fall"}[strings.TrimSpace(strings.TrimPrefix(sticker[4:], "年"))]
	if term == "" {
		// The sticker may be "2026 秋季": look past a leading season character.
		for _, candidate := range []string{"冬", "春", "夏", "秋"} {
			if strings.Contains(sticker[4:], candidate) {
				term = map[string]string{"冬": "winter", "春": "spring", "夏": "summer", "秋": "fall"}[candidate]
				break
			}
		}
	}
	if term == "" {
		return ""
	}
	return year + "-" + term
}

func firstHref(selection *goquery.Selection) string {
	href, _ := selection.Find("a").First().Attr("href")
	return strings.TrimSpace(href)
}

func positiveInt(value string) *int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return nil
	}
	return &parsed
}
