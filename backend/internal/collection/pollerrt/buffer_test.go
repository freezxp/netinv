package pollerrt

import (
	"context"
	"errors"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

func testBatch(n int) wire.MetricBatch {
	s := make([]wire.Sample, n)
	for i := range s {
		s[i] = wire.Sample{DeviceID: "d_1", Name: "netinv_test", TSMillis: int64(i), Value: 1}
	}
	return wire.MetricBatch{PollerID: "p1", SiteID: "s1", Samples: s}
}

func TestBufferPutDrain(t *testing.T) {
	b, err := NewDiskBuffer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := b.Put(testBatch(10)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := b.Depth(); n != 3 {
		t.Fatalf("depth = %d, want 3", n)
	}
	var got []wire.MetricBatch
	replayed := b.Drain(context.Background(), func(batch wire.MetricBatch) error {
		got = append(got, batch)
		return nil
	})
	if replayed != 3 || len(got) != 3 {
		t.Fatalf("replayed = %d, want 3", replayed)
	}
	if n, _ := b.Depth(); n != 0 {
		t.Fatalf("depth after drain = %d, want 0", n)
	}
}

func TestBufferDrainStopsOnFailure(t *testing.T) {
	b, _ := NewDiskBuffer(t.TempDir())
	_ = b.Put(testBatch(1))
	_ = b.Put(testBatch(1))
	replayed := b.Drain(context.Background(), func(wire.MetricBatch) error {
		return errors.New("link down")
	})
	if replayed != 0 {
		t.Fatalf("replayed = %d, want 0", replayed)
	}
	if n, _ := b.Depth(); n != 2 {
		t.Fatalf("depth = %d, want 2 (nothing lost)", n)
	}
}

func TestBufferCapDropsOldest(t *testing.T) {
	b, _ := NewDiskBuffer(t.TempDir())
	b.MaxBytes = 2000 // tiny cap
	for range 10 {
		_ = b.Put(testBatch(10)) // ~700B each
	}
	n, bytes := b.Depth()
	if bytes > b.MaxBytes {
		t.Fatalf("bytes = %d over cap %d", bytes, b.MaxBytes)
	}
	if b.Dropped() == 0 {
		t.Fatal("expected drops at cap")
	}
	if n == 0 {
		t.Fatal("newest batches must survive")
	}
}
