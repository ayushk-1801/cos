package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ayush/cos-lite/internal/clientctx"
)

const (
	maxRecentEvents  = 500
	auditRotateAt    = 5 * 1024 * 1024
	activityRotateAt = 2 * 1024 * 1024
)

type Event struct {
	Timestamp  string  `json:"timestamp"`
	DurationMS float64 `json:"duration_ms"`
	ClientKey  string  `json:"client_key"`
	ClientName string  `json:"client_name"`
	Method     string  `json:"method"`
	Target     string  `json:"target,omitempty"`
	Status     string  `json:"status"`
	TraceID    string  `json:"trace_id"`
	SpanID     string  `json:"span_id"`
}

// ActivityEvent is the local TUI activity record. OutputPreview is deliberately
// kept out of Event/OTLP so tool output is never exported to an observability
// backend by default. The activity file is local-only, mode 0600 and bounded.
type ActivityEvent struct {
	Event
	OutputPreview string `json:"output_preview,omitempty"`
}

type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	TraceFlags   string
	Started      time.Time
}

type Recorder struct {
	mu            sync.RWMutex
	recent        []Event
	audit         *os.File
	auditPath     string
	auditBytes    int64
	activity      *os.File
	activityPath  string
	activityBytes int64
	writeMu       sync.RWMutex
	writeCh       chan localRecord
	writerDone    chan struct{}
	closed        bool
	otlpURL       string
	headers       http.Header
	client        *http.Client
	exportCh      chan otlpSpan
}

type otlpSpan struct {
	event Event
	span  Span
	end   time.Time
}

type localRecord struct {
	event    Event
	activity ActivityEvent
}

func New() (*Recorder, error) {
	p, err := auditPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if st, err := os.Stat(p); err == nil && st.Size() >= auditRotateAt {
		_ = os.Remove(p + ".1")
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(p, 0o600)
	var auditBytes int64
	if st, statErr := f.Stat(); statErr == nil {
		auditBytes = st.Size()
	}
	ap, err := activityPath()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if st, err := os.Stat(ap); err == nil && st.Size() >= activityRotateAt {
		_ = os.Remove(ap + ".1")
		_ = os.Rename(ap, ap+".1")
	}
	af, err := os.OpenFile(ap, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	_ = os.Chmod(ap, 0o600)
	var activityBytes int64
	if st, statErr := af.Stat(); statErr == nil {
		activityBytes = st.Size()
	}
	r := &Recorder{
		audit: f, auditPath: p, auditBytes: auditBytes,
		activity: af, activityPath: ap, activityBytes: activityBytes,
		writeCh: make(chan localRecord, 4096), writerDone: make(chan struct{}),
		client: &http.Client{Timeout: 3 * time.Second}, exportCh: make(chan otlpSpan, 256),
	}
	r.otlpURL = normalizeOTLPEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	r.headers = parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	if r.otlpURL != "" {
		go r.exportLoop()
	}
	go r.localWriterLoop()
	return r, nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.writeMu.Lock()
	if !r.closed {
		r.closed = true
		close(r.writeCh)
	}
	r.writeMu.Unlock()
	if r.writerDone != nil {
		<-r.writerDone
	}
	var first error
	if r.audit != nil {
		first = r.audit.Close()
	}
	if r.activity != nil {
		if err := r.activity.Close(); first == nil {
			first = err
		}
	}
	return first
}

func (r *Recorder) Begin(ctx context.Context) (context.Context, Span) {
	info := clientctx.From(ctx)
	traceID, parentSpan, flags := parseTraceParent(info.TraceParent)
	if traceID == "" {
		traceID = randomHex(16)
		flags = "01"
	}
	span := Span{TraceID: traceID, SpanID: randomHex(8), ParentSpanID: parentSpan, TraceFlags: flags, Started: time.Now()}
	return context.WithValue(ctx, spanKey{}, span), span
}

func (r *Recorder) End(ctx context.Context, span Span, method, target, status string) Event {
	return r.EndWithPreview(ctx, span, method, target, status, "")
}

func (r *Recorder) EndWithPreview(ctx context.Context, span Span, method, target, status, outputPreview string) Event {
	if r == nil {
		return Event{}
	}
	if span.Started.IsZero() {
		if v, ok := ctx.Value(spanKey{}).(Span); ok {
			span = v
		}
	}
	end := time.Now()
	info := clientctx.From(ctx)
	if status == "" {
		status = "ok"
	}
	e := Event{
		Timestamp: end.UTC().Format(time.RFC3339Nano), DurationMS: float64(end.Sub(span.Started).Microseconds()) / 1000,
		ClientKey: info.Key, ClientName: info.Name, Method: method, Target: target, Status: status,
		TraceID: span.TraceID, SpanID: span.SpanID,
	}
	r.mu.Lock()
	r.recent = append(r.recent, e)
	if len(r.recent) > maxRecentEvents {
		copy(r.recent, r.recent[len(r.recent)-maxRecentEvents:])
		r.recent = r.recent[:maxRecentEvents]
	}
	r.mu.Unlock()
	r.enqueueLocal(localRecord{event: e, activity: ActivityEvent{Event: e, OutputPreview: boundPreview(outputPreview, 1200)}})
	if r.otlpURL != "" {
		select {
		case r.exportCh <- otlpSpan{event: e, span: span, end: end}:
		default:
		}
	}
	return e
}

func (r *Recorder) Recent(clientKey string, limit int) []Event {
	if r == nil {
		return nil
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Event, 0, limit)
	for i := len(r.recent) - 1; i >= 0 && len(out) < limit; i-- {
		e := r.recent[i]
		if clientKey != "" && e.ClientKey != clientKey {
			continue
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type spanKey struct{}

func parseTraceParent(v string) (traceID, parentSpanID, flags string) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) != 4 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", "", ""
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", "", ""
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", "", ""
	}
	return strings.ToLower(parts[1]), strings.ToLower(parts[2]), strings.ToLower(parts[3])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

func auditPath() (string, error) {
	if d := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); d != "" {
		return filepath.Join(d, "cos-lite", "audit.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "cos-lite", "audit.jsonl"), nil
}

func AuditPath() (string, error) { return auditPath() }

func activityPath() (string, error) {
	if d := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); d != "" {
		return filepath.Join(d, "cos-lite", "activity.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "cos-lite", "activity.jsonl"), nil
}

func ActivityPath() (string, error) { return activityPath() }

func ReadAudit(limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	p, err := auditPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func ReadActivity(limit int) ([]ActivityEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	p, err := activityPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]ActivityEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e ActivityEvent
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func boundPreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 || s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (r *Recorder) writeActivity(line []byte) error {
	if r.activity == nil {
		return nil
	}
	if r.activityBytes+int64(len(line)) > activityRotateAt && r.activityPath != "" {
		_ = r.activity.Close()
		_ = os.Remove(r.activityPath + ".1")
		_ = os.Rename(r.activityPath, r.activityPath+".1")
		f, err := os.OpenFile(r.activityPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			r.activity = nil
			return err
		}
		_ = os.Chmod(r.activityPath, 0o600)
		r.activity = f
		r.activityBytes = 0
	}
	n, err := r.activity.Write(line)
	r.activityBytes += int64(n)
	return err
}

func (r *Recorder) writeAudit(line []byte) error {
	if r.audit == nil {
		return nil
	}
	if r.auditBytes+int64(len(line)) > auditRotateAt && r.auditPath != "" {
		_ = r.audit.Close()
		_ = os.Remove(r.auditPath + ".1")
		_ = os.Rename(r.auditPath, r.auditPath+".1")
		f, err := os.OpenFile(r.auditPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			r.audit = nil
			return err
		}
		_ = os.Chmod(r.auditPath, 0o600)
		r.audit = f
		r.auditBytes = 0
	}
	n, err := r.audit.Write(line)
	r.auditBytes += int64(n)
	return err
}

func (r *Recorder) enqueueLocal(record localRecord) {
	r.writeMu.RLock()
	defer r.writeMu.RUnlock()
	if r.closed || r.writeCh == nil {
		return
	}
	r.writeCh <- record
}

func (r *Recorder) localWriterLoop() {
	defer close(r.writerDone)
	batch := make([]localRecord, 0, 64)
	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}
	armTimer := func() {
		if timer == nil {
			timer = time.NewTimer(50 * time.Millisecond)
		} else {
			timer.Reset(50 * time.Millisecond)
		}
		timerC = timer.C
	}
	flush := func() {
		if len(batch) == 0 {
			stopTimer()
			return
		}
		var auditBuf, activityBuf bytes.Buffer
		for _, record := range batch {
			if b, err := json.Marshal(record.event); err == nil {
				auditBuf.Write(b)
				auditBuf.WriteByte('\n')
			}
			if b, err := json.Marshal(record.activity); err == nil {
				activityBuf.Write(b)
				activityBuf.WriteByte('\n')
			}
		}
		if auditBuf.Len() > 0 {
			_ = r.writeAudit(auditBuf.Bytes())
		}
		if activityBuf.Len() > 0 {
			_ = r.writeActivity(activityBuf.Bytes())
		}
		batch = batch[:0]
		stopTimer()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case record, ok := <-r.writeCh:
			if !ok {
				flush()
				return
			}
			if len(batch) == 0 {
				armTimer()
			}
			batch = append(batch, record)
			if len(batch) >= cap(batch) {
				flush()
			}
		case <-timerC:
			flush()
		}
	}
}

func normalizeOTLPEndpoint(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	if !strings.HasSuffix(u.Path, "/v1/traces") {
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/traces"
	}
	return u.String()
}

func parseHeaders(v string) http.Header {
	h := http.Header{}
	for _, part := range strings.Split(v, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] != "" {
			h.Set(kv[0], kv[1])
		}
	}
	return h
}

func (r *Recorder) exportLoop() {
	for item := range r.exportCh {
		_ = r.export(item)
	}
}

func (r *Recorder) export(item otlpSpan) error {
	service := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if service == "" {
		service = "cos-lite"
	}
	statusCode := 1
	if item.event.Status != "ok" {
		statusCode = 2
	}
	attrs := []any{
		attribute("mcp.method", item.event.Method), attribute("mcp.target", item.event.Target),
		attribute("mcp.client.key", item.event.ClientKey), attribute("mcp.client.name", item.event.ClientName),
	}
	span := map[string]any{
		"traceId": item.span.TraceID, "spanId": item.span.SpanID, "name": spanName(item.event), "kind": 2,
		"startTimeUnixNano": strconv.FormatInt(item.span.Started.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(item.end.UnixNano(), 10),
		"attributes": attrs, "status": map[string]any{"code": statusCode},
	}
	if item.span.ParentSpanID != "" {
		span["parentSpanId"] = item.span.ParentSpanID
	}
	payload := map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{attribute("service.name", service)}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "cos-lite"}, "spans": []any{span}}},
	}}}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, r.otlpURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vals := range r.headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("OTLP HTTP %d", resp.StatusCode)
	}
	return nil
}

func attribute(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

func spanName(e Event) string {
	if e.Target != "" {
		return e.Method + " " + e.Target
	}
	return e.Method
}
