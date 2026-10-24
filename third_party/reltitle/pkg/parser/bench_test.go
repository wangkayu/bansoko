package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// BenchmarkParseCorpus parses the whole golden corpus on each iteration.
//
//	go test ./pkg/parser -run '^$' -bench ParseCorpus -benchmem
func BenchmarkParseCorpus(b *testing.B) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		b.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	titles := readGoldenCorpus(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, title := range titles {
			if _, err := p.Parse(title); err != nil {
				b.Fatalf("parse %q: %v", title, err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(titles)), "ns/title")
}
