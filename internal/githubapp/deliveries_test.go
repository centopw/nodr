package githubapp_test

import (
	"context"
	"sync"
	"testing"

	"github.com/centopw/nodr/internal/githubapp"
)

func TestMemoryDeliveries_RecordsAndSees(t *testing.T) {
	d := githubapp.NewMemoryDeliveries()
	ctx := context.Background()

	if seen, err := d.Seen(ctx, "d-1"); err != nil || seen {
		t.Fatalf("Seen before record = (%t, %v), want (false, nil)", seen, err)
	}
	if err := d.Record(ctx, "d-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if seen, err := d.Seen(ctx, "d-1"); err != nil || !seen {
		t.Fatalf("Seen after record = (%t, %v), want (true, nil)", seen, err)
	}
	if seen, err := d.Seen(ctx, "d-2"); err != nil || seen {
		t.Fatalf("Seen other delivery id = (%t, %v), want (false, nil)", seen, err)
	}
	if err := d.Record(ctx, "d-2"); err != nil {
		t.Fatalf("Record second id: %v", err)
	}
	if seen, err := d.Seen(ctx, "d-2"); err != nil || !seen {
		t.Fatalf("Seen after second record = (%t, %v), want (true, nil)", seen, err)
	}
}

// MemoryDeliveries backs webhook ingestion that processes deliveries
// concurrently, so its methods must be safe under parallel use.
func TestMemoryDeliveries_Concurrent(t *testing.T) {
	d := githubapp.NewMemoryDeliveries()
	ctx := context.Background()
	const id = "d-concurrent"

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := d.Record(ctx, id); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := d.Seen(ctx, id); err != nil {
				t.Errorf("Seen: %v", err)
			}
		}()
	}
	wg.Wait()

	if seen, err := d.Seen(ctx, id); err != nil || !seen {
		t.Fatalf("Seen after concurrent records = (%t, %v), want (true, nil)", seen, err)
	}
}
