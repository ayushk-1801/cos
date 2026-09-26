package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type cdpSession struct {
	ws      *wsConn
	next    atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan cdpResponse
	closed  chan struct{}
	console []map[string]any
	network []map[string]any
}

type cdpResponse struct {
	Result map[string]any
	Err    error
}

type wireMessage struct {
	ID     int64          `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Result map[string]any `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newCDPSession(raw string) (*cdpSession, error) {
	ws, err := dialWS(raw)
	if err != nil {
		return nil, err
	}
	s := &cdpSession{ws: ws, pending: make(map[int64]chan cdpResponse), closed: make(chan struct{})}
	go s.readLoop()
	for _, m := range []string{"Runtime.enable", "Log.enable", "Network.enable", "Page.enable"} {
		_, _ = s.Call(m, nil, 3*time.Second)
	}
	return s, nil
}

func (s *cdpSession) Close() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)
		s.ws.Close()
	}
}

func (s *cdpSession) Call(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	id := s.next.Add(1)
	ch := make(chan cdpResponse, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := s.ws.WriteText(payload); err != nil {
		s.remove(id)
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.Result, r.Err
	case <-t.C:
		s.remove(id)
		return nil, fmt.Errorf("CDP %s timed out", method)
	case <-s.closed:
		return nil, errors.New("CDP session closed")
	}
}

func (s *cdpSession) remove(id int64) { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }

func (s *cdpSession) readLoop() {
	defer func() {
		s.mu.Lock()
		for id, ch := range s.pending {
			ch <- cdpResponse{Err: errors.New("CDP connection closed")}
			delete(s.pending, id)
		}
		s.mu.Unlock()
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
	}()
	for {
		b, err := s.ws.ReadText()
		if err != nil {
			return
		}
		var msg wireMessage
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			s.mu.Lock()
			ch := s.pending[msg.ID]
			delete(s.pending, msg.ID)
			s.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- cdpResponse{Err: fmt.Errorf("CDP error %d: %s", msg.Error.Code, msg.Error.Message)}
				} else {
					ch <- cdpResponse{Result: msg.Result}
				}
			}
			continue
		}
		s.recordEvent(msg.Method, msg.Params)
	}
}

func (s *cdpSession) recordEvent(method string, p map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "Runtime.consoleAPICalled", "Log.entryAdded", "Runtime.exceptionThrown":
		s.console = appendBounded(s.console, map[string]any{"method": method, "params": p}, 200)
	case "Network.requestWillBeSent", "Network.responseReceived", "Network.loadingFailed":
		s.network = appendBounded(s.network, map[string]any{"method": method, "params": p}, 300)
	}
}

func appendBounded(in []map[string]any, v map[string]any, max int) []map[string]any {
	in = append(in, v)
	if len(in) > max {
		copy(in, in[len(in)-max:])
		in = in[:max]
	}
	return in
}

func (s *cdpSession) consoleEvents(clear bool) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]map[string]any{}, s.console...)
	if clear {
		s.console = nil
	}
	return out
}
func (s *cdpSession) networkEvents(clear bool) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]map[string]any{}, s.network...)
	if clear {
		s.network = nil
	}
	return out
}
