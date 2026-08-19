package settings

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// A destination that cannot work must be refused while someone is looking at
// the form, not accepted and then discovered an hour later as a counter nobody
// is watching. "vm-backup:8428" reads as correct in a text box and produces a
// request to a relative path that fails on every batch.
func TestPutMirrorRejectsAddressesThatCannotWork(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	s := &Store{Pool: pool}

	for _, bad := range []string{
		"vm-backup:8428",                     // no scheme
		"ftp://vm-backup:8428",               // wrong scheme
		"http://",                            // no host
		"http://vm:8428/api/v1/import",       // the writer appends this itself
		"http://vm:8428/select/0/prometheus", // a path, for the same reason
	} {
		if _, err := s.PutMirror(ctx, Mirror{Enabled: true, URLs: []string{bad}}, ""); err == nil {
			t.Errorf("accepted %q", bad)
		} else if errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("%q gave kind %v, want invalid", bad, errx.KindOf(err))
		}
	}
}

func TestPutMirrorNormalisesAndDeduplicates(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	s := &Store{Pool: pool}

	out, err := s.PutMirror(ctx, Mirror{Enabled: true, URLs: []string{
		" http://vm-a:8428/ ", "http://vm-a:8428", "", "http://vm-b:8428",
	}}, "")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// The same instance twice is not a second copy — it is the same copy and
	// twice the work.
	if len(out.URLs) != 2 || out.URLs[0] != "http://vm-a:8428" || out.URLs[1] != "http://vm-b:8428" {
		t.Fatalf("stored %#v", out.URLs)
	}

	got, err := s.GetMirror(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || len(got.URLs) != 2 {
		t.Fatalf("read back %#v", got)
	}
}

// Enabling with nowhere to send is a setting that silently does nothing.
func TestPutMirrorRejectsEnabledWithNoDestination(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	if _, err := s.PutMirror(ctx, Mirror{Enabled: true}, ""); err == nil {
		t.Fatal("accepted copying enabled with no destination")
	}
}

// Disabling keeps the addresses, so switching copying off for maintenance does
// not make an operator retype them to switch it back on.
func TestDisablingKeepsTheDestinations(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	s := &Store{Pool: pool}

	if _, err := s.PutMirror(ctx, Mirror{Enabled: true,
		URLs: []string{"http://vm-a:8428"}}, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	off, err := s.PutMirror(ctx, Mirror{Enabled: false,
		URLs: []string{"http://vm-a:8428"}}, "")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(off.URLs) != 1 {
		t.Fatalf("disabling dropped the destinations: %#v", off)
	}
	if len(off.Targets()) != 0 {
		t.Fatal("Targets() returned destinations while copying is disabled")
	}
}

// Never configured is off, not an error. A deployment that has never set this
// must not log a failure a minute forever.
func TestUnconfiguredMirrorIsSilentlyOff(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	m, err := (&Store{Pool: pool}).GetMirror(context.Background())
	if err != nil {
		t.Fatalf("get on a fresh database: %v", err)
	}
	if m.Enabled || len(m.Targets()) != 0 {
		t.Fatalf("unconfigured mirror reads as %#v", m)
	}
	_ = ctx
}
