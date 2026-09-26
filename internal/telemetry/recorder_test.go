package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ayush/cos-lite/internal/clientctx"
)

func TestAuditAndTraceParent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info := clientctx.Info{Key: "client-a", Name: "ChatGPT", TraceParent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}
	ctx := clientctx.With(context.Background(), info)
	ctx, span := r.Begin(ctx)
	if span.TraceID != "0123456789abcdef0123456789abcdef" || span.ParentSpanID != "0123456789abcdef" {
		t.Fatalf("span=%+v", span)
	}
	r.End(ctx, span, "tools/call", "read", "ok")
	recent := r.Recent("client-a", 10)
	if len(recent) != 1 || recent[0].Target != "read" || recent[0].ClientName != "ChatGPT" {
		t.Fatalf("recent=%#v", recent)
	}
}

func TestOTLPJSONExport(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/traces" || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("path=%s content-type=%s", req.URL.Path, req.Header.Get("Content-Type"))
		}
		var v map[string]any
		_ = json.NewDecoder(req.Body).Decode(&v)
		received <- v
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := clientctx.With(context.Background(), clientctx.Info{Key: "c", Name: "client"})
	ctx, span := r.Begin(ctx)
	r.End(ctx, span, "tools/call", "find", "ok")
	select {
	case v := <-received:
		b, _ := json.Marshal(v)
		if !strings.Contains(string(b), "cos-lite") || !strings.Contains(string(b), "tools/call find") {
			t.Fatalf("payload=%s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no OTLP export")
	}
}

func TestActivityPreviewIsLocalOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := clientctx.With(context.Background(), clientctx.Info{Key: "chat-a", Name: "ChatGPT"})
	ctx, span := r.Begin(ctx)
	r.EndWithPreview(ctx, span, "tools/call", "read", "ok", "hello output")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	activity, err := ReadActivity(10)
	if err != nil || len(activity) != 1 {
		t.Fatalf("activity=%#v err=%v", activity, err)
	}
	if activity[0].OutputPreview != "hello output" || activity[0].Target != "read" {
		t.Fatalf("activity=%#v", activity[0])
	}
	audit, err := ReadAudit(10)
	if err != nil || len(audit) != 1 {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
	b, _ := json.Marshal(audit[0])
	if strings.Contains(string(b), "hello output") || strings.Contains(string(b), "output_preview") {
		t.Fatalf("output preview leaked into audit: %s", b)
	}
}
