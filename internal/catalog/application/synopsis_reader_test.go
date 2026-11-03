package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMikanHTTPReaderReadSynopsisReadsPageFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Home/Bangumi/301" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<div class="header2"><span class="header2-text">概况介绍</span></div>
<p class="header2-desc"> 一段简介

[简介原文]
原文 </p>
<p class="bangumi-info">放送日期：&#x661F;&#x671F;&#x4E8C;</p>
<p class="bangumi-info">总集数：13</p>
<p class="bangumi-info">放送开始：4/8/2014</p>
<p class="bangumi-info">官方网站：<br /><a class="w-other-c" href="http://www.black-bullet.net/">site</a></p>
<p class="bangumi-info">Bangumi番组计划链接：<br /><a class="w-other-c" href="http://bgm.tv/subject/84873">bgm</a></p>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "301")
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != ProviderMikan || result.ProviderEntryRef != "301" {
		t.Fatalf("identity = %+v", result)
	}
	if result.SourceURL != server.URL+"/Home/Bangumi/301" {
		t.Fatalf("source url = %q", result.SourceURL)
	}
	if result.Synopsis == nil || *result.Synopsis != "一段简介\n\n[简介原文]\n原文" {
		t.Fatalf("synopsis = %v", result.Synopsis)
	}
	if result.Weekday != "tue" {
		t.Fatalf("weekday = %q", result.Weekday)
	}
	if result.Episodes == nil || *result.Episodes != 13 {
		t.Fatalf("episodes = %v", result.Episodes)
	}
	if result.OfficialSite != "http://www.black-bullet.net/" || result.BGMURL != "http://bgm.tv/subject/84873" {
		t.Fatalf("links = %+v", result)
	}
	if len(result.Tags) != 0 || result.Season != "" {
		t.Fatalf("Mikan must not borrow tags or season: %+v", result)
	}
}

func TestMikanHTTPReaderReadSynopsisReturnsNilWithoutSynopsis(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<p class="bangumi-info">总集数：12</p>`))
	}))
	defer server.Close()

	result, err := (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Synopsis != nil {
		t.Fatalf("synopsis = %v, want nil", result.Synopsis)
	}
	if result.Episodes == nil || *result.Episodes != 12 {
		t.Fatalf("episodes = %v", result.Episodes)
	}
}

func TestHTTPReaderReadSynopsisReadsAniBTPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anime/505550" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<span class="anibt-detail-sticker sticker">2026 秋季</span>
<h1>暗机</h1>
<h2 class="sr-only">番剧简介</h2><p>第一段

[简介原文]
原文段落</p>
<span class="tag">TV动画</span><span class="tag num">14 集</span>
<span class="flex" title="TV · 原创 · 日本 · 机战 · 科幻"><span>标签：TV · 原创 · 日本 · 机战</span></span>
<a href="https://official.example/"><svg></svg><span>官方网站</span></a>`))
	}))
	defer server.Close()

	result, err := (HTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "505550")
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != ProviderAniBT || result.ProviderEntryRef != "505550" {
		t.Fatalf("identity = %+v", result)
	}
	if result.SourceURL != server.URL+"/anime/505550" {
		t.Fatalf("source url = %q", result.SourceURL)
	}
	if result.Synopsis == nil || *result.Synopsis != "第一段\n\n[简介原文]\n原文段落" {
		t.Fatalf("synopsis = %v", result.Synopsis)
	}
	if result.Season != "2026-fall" {
		t.Fatalf("season = %q", result.Season)
	}
	if result.Episodes == nil || *result.Episodes != 14 {
		t.Fatalf("episodes = %v", result.Episodes)
	}
	if result.OfficialSite != "https://official.example/" {
		t.Fatalf("official site = %q", result.OfficialSite)
	}
	if result.BGMURL != "https://bgm.tv/subject/505550" {
		t.Fatalf("bgm url = %q", result.BGMURL)
	}
	wantTags := []string{"TV", "原创", "日本", "机战", "科幻"}
	if len(result.Tags) != len(wantTags) {
		t.Fatalf("tags = %v", result.Tags)
	}
	for index, tag := range wantTags {
		if result.Tags[index] != tag {
			t.Fatalf("tags = %v, want %v", result.Tags, wantTags)
		}
	}
}

func TestHTTPReaderReadSynopsisReturnsNilWithoutSynopsis(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<h2 class="sr-only">番剧简介</h2><p></p>`))
	}))
	defer server.Close()

	result, err := (HTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Synopsis != nil {
		t.Fatalf("synopsis = %v, want nil", result.Synopsis)
	}
}

func TestSynopsisReadersWrapUpstreamHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := (HTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "1")
	if !errors.Is(err, ErrProviderUpstream) {
		t.Fatalf("AniBT err = %v, want ErrProviderUpstream", err)
	}
	_, err = (MikanHTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "1")
	if !errors.Is(err, ErrProviderUpstream) {
		t.Fatalf("Mikan err = %v, want ErrProviderUpstream", err)
	}
}

func TestSynopsisResultCarriesNoBGMURLWithoutNumericReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<h2 class="sr-only">番剧简介</h2><p>简介</p>`))
	}))
	defer server.Close()

	result, err := (HTTPReader{Endpoint: server.URL, Client: server.Client()}).ReadSynopsis(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if result.BGMURL != "" {
		t.Fatalf("bgm url = %q, want empty", result.BGMURL)
	}
}

var _ SynopsisReader = HTTPReader{}
var _ SynopsisReader = MikanHTTPReader{}
