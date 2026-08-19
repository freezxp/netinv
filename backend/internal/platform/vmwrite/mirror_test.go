package vmwrite

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	bodies []string
	status int
	delay  time.Duration
}

func (r *recorder) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.delay > 0 {
			time.Sleep(r.delay)
		}
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(b))
		r.mu.Unlock()
		if r.status != 0 {
			w.WriteHeader(r.status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// The mirror has to receive the same bytes as the primary. An io.Reader is
// consumed by the first POST, so mirroring by re-reading a spent reader would
// silently write an empty body — a backup that appears to be working and
// contains nothing.
func TestImportSendsTheSameBodyToPrimaryAndMirror(t *testing.T) {
	primary, mirror := &recorder{}, &recorder{}
	ps, ms := primary.server(), mirror.server()
	defer ps.Close()
	defer ms.Close()

	tgt := New(ps.URL, []string{ms.URL}, quiet())
	body := []byte(`{"metric":{"__name__":"x"},"values":[1],"timestamps":[2]}` + "\n")
	if err := tgt.Import(context.Background(), body); err != nil {
		t.Fatalf("import: %v", err)
	}
	p, m := primary.got(), mirror.got()
	if len(p) != 1 || len(m) != 1 {
		t.Fatalf("primary got %d bodies, mirror got %d", len(p), len(m))
	}
	if p[0] != string(body) || m[0] != p[0] {
		t.Fatalf("bodies differ:\n primary %q\n mirror  %q", p[0], m[0])
	}
}

// A backup target that can fail production ingest is a liability rather than a
// backup. Whatever the mirror does — reject, hang, not exist — the primary
// write must stand and the caller must see success, or the batch is redelivered
// and the fault spreads to collection.
func TestMirrorFailureNeverFailsThePrimary(t *testing.T) {
	primary := &recorder{}
	ps := primary.server()
	defer ps.Close()
	broken := &recorder{status: http.StatusInternalServerError}
	bs := broken.server()
	defer bs.Close()

	tgt := New(ps.URL, []string{bs.URL, "http://127.0.0.1:1"}, quiet())
	if err := tgt.Import(context.Background(), []byte("line\n")); err != nil {
		t.Fatalf("a failing mirror failed the import: %v", err)
	}
	if len(primary.got()) != 1 {
		t.Fatalf("primary did not receive the batch")
	}
}

// The primary's failure is the one that must propagate: the ingester relies on
// it to nack and retry, and swallowing it would drop metrics silently.
func TestPrimaryFailurePropagates(t *testing.T) {
	broken := &recorder{status: http.StatusBadRequest}
	bs := broken.server()
	defer bs.Close()
	mirror := &recorder{}
	ms := mirror.server()
	defer ms.Close()

	tgt := New(bs.URL, []string{ms.URL}, quiet())
	if err := tgt.Import(context.Background(), []byte("line\n")); err == nil {
		t.Fatal("a failing primary reported success")
	}
	// And the mirror is not written when the primary refused: the copy should
	// not contain samples the system itself rejected.
	if len(mirror.got()) != 0 {
		t.Fatalf("mirror received %d bodies after a primary failure", len(mirror.got()))
	}
}

// The caller's context is usually tied to the message being processed. If the
// mirror inherited it, an ack immediately after the primary write would cancel
// the mirror mid-flight and turn a healthy backup into truncated requests.
func TestMirrorSurvivesACancelledCallerContext(t *testing.T) {
	primary := &recorder{}
	ps := primary.server()
	defer ps.Close()
	mirror := &recorder{delay: 40 * time.Millisecond}
	ms := mirror.server()
	defer ms.Close()

	ctx, cancel := context.WithCancel(context.Background())
	tgt := New(ps.URL, []string{ms.URL}, quiet())
	// Cancel as soon as the primary write returns, which is what an ack does.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := tgt.Import(ctx, []byte("line\n")); err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(mirror.got()) != 1 {
		t.Fatalf("mirror got %d bodies — a cancelled caller context killed it", len(mirror.got()))
	}
}

func TestParseMirrorsSplitsAndTrims(t *testing.T) {
	got := ParseMirrors(" http://a:8428 , http://b:8428 ,, ")
	if len(got) != 2 || got[0] != "http://a:8428" || got[1] != "http://b:8428" {
		t.Fatalf("ParseMirrors gave %#v", got)
	}
	if len(ParseMirrors("")) != 0 {
		t.Fatal("an unset value must configure no mirrors")
	}
}
