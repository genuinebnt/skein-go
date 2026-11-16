package seed_test

import (
	"context"
	"testing"

	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// BenchmarkRestartFromSnapshot and BenchmarkRestartFromTables compare the
// two cold-start paths on the small seed (300 notes, 3,923 blocks).
func BenchmarkRestartFromSnapshot(b *testing.B) {
	w := loadForBench(b)
	var data []byte
	_ = w.index.Read(func(p *domain.Projection) error {
		var err error
		data, err = domain.EncodeSnapshot(p)
		return err
	})
	b.ReportMetric(float64(len(data)), "snapshot-bytes")
	for b.Loop() {
		if _, err := domain.DecodeSnapshot(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRestartFromTables(b *testing.B) {
	w := loadForBench(b)
	for b.Loop() {
		ix := app.NewIndex(w.store, app.IndexConfig{})
		if err := ix.Bootstrap(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func loadForBench(b *testing.B) *world {
	b.Helper()
	loadOnce.Do(func() { loaded, loadErr = load() })
	if loadErr != nil {
		b.Fatal(loadErr)
	}
	return loaded
}
