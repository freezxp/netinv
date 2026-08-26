package reports

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryRangeDecodesSeriesAndSkipsNonNumbers(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotBody = r.Form.Encode()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[
			{"metric":{"dir":"in","if_index":"3"},
			 "values":[[1700000000,"10"],[1700000060,"NaN"],[1700000120,"30"]]}
		]}}`))
	}))
	defer srv.Close()

	r := NewVMReader(srv.URL)
	from := time.Unix(1700000000, 0)
	series, err := r.QueryRange(context.Background(), "up", from, from.Add(time.Hour), 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	// The NaN is dropped rather than plotted as zero: a gap in collection is
	// not a circuit that went quiet.
	if len(series[0].Points) != 2 {
		t.Fatalf("points = %+v, want the NaN dropped", series[0].Points)
	}
	if series[0].Points[1].T != 1700000120 || series[0].Points[1].V != 30 {
		t.Errorf("point = %+v", series[0].Points[1])
	}
	// The expression is POSTed as a form: a grouped report's query runs to
	// tens of kilobytes and would be truncated as a query string.
	for _, want := range []string{"query=up", "start=1700000000", "step=60"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body %q is missing %q", gotBody, want)
		}
	}
}

// The store's own words carry back. "missing operand" tells you what is wrong
// where a status code does not.
func TestQueryRangeSurfacesTheStoresError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":"error","error":"missing operand"}`))
	}))
	defer srv.Close()
	_, err := NewVMReader(srv.URL).QueryRange(context.Background(), "*", time.Now(),
		time.Now().Add(time.Hour), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "missing operand") {
		t.Fatalf("err = %v, want the store's reason", err)
	}
}

func TestQueryRangeReportsATransientFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := NewVMReader(srv.URL).QueryRange(context.Background(), "up",
		time.Now(), time.Now().Add(time.Hour), 0); err == nil {
		t.Fatal("a 502 was not reported")
	}
}
