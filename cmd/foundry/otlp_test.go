package main

import (
	"compress/gzip"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	colmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpSink is an OTLP/HTTP receiver that keeps every span and metric name it is sent.
type otlpSink struct {
	url     string
	mu      sync.Mutex
	got     []sinkSpan
	metrics []string
}

type sinkSpan struct {
	Name, Trace, ID, Parent string
	Attrs                   map[string]string
}

func startSink(t *testing.T) *otlpSink {
	s := &otlpSink{}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *otlpSink) serve(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body = gz
	}
	b, _ := io.ReadAll(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/v1/traces":
		var req coltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					attrs := map[string]string{}
					for _, a := range sp.Attributes {
						attrs[a.Key] = a.Value.String()
					}
					s.got = append(s.got, sinkSpan{Name: sp.Name, Trace: hex.EncodeToString(sp.TraceId),
						ID: hex.EncodeToString(sp.SpanId), Parent: hex.EncodeToString(sp.ParentSpanId), Attrs: attrs})
				}
			}
		}
		out, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
	case "/v1/metrics":
		var req colmetric.ExportMetricsServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					s.metrics = append(s.metrics, m.Name)
				}
			}
		}
		out, _ := proto.Marshal(&colmetric.ExportMetricsServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
	}
}

func (s *otlpSink) spans() []sinkSpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkSpan{}, s.got...)
}

func (s *otlpSink) metricNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.metrics...)
}

// named returns the spans called name.
func named(spans []sinkSpan, name string) []sinkSpan {
	var out []sinkSpan
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func (w *world) exportTo(s *otlpSink) {
	w.env = append(w.env, "OTEL_TRACES_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT="+s.url)
}

func TestShipSpansParentUnderTraceparent(t *testing.T) {
	sink := startSink(t)
	w := newWorld(t, "feat")
	w.exportTo(sink)
	const trace, parent = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	w.env = append(w.env, "TRACEPARENT=00-"+trace+"-"+parent+"-01")
	w.foundry("friction", "none", "--reason", "smooth run")
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	spans := sink.spans()
	ship := named(spans, "foundry.ship")
	if len(ship) != 1 || ship[0].Trace != trace || ship[0].Parent != parent {
		t.Fatalf("foundry.ship %+v, want trace %s parent %s; all %+v", ship, trace, parent, spans)
	}
	for _, step := range []string{"preflight", "checks", "review", "push_pr", "ci", "friction"} {
		got := named(spans, "foundry.ship."+step)
		if len(got) != 1 || got[0].Parent != ship[0].ID {
			t.Errorf("foundry.ship.%s %+v, want one child of %s", step, got, ship[0].ID)
		}
	}
}
