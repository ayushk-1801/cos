package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Tab struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type Manager struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	base       string
	profile    string
	sessions   map[string]*cdpSession
	headless   bool
	profileKey string
}

func NewManager(headless bool) *Manager {
	return NewManagerForClient(headless, "shared")
}

func NewManagerForClient(headless bool, key string) *Manager {
	if strings.TrimSpace(key) == "" {
		key = "anonymous"
	}
	sum := sha256.Sum256([]byte(key))
	return &Manager{sessions: make(map[string]*cdpSession), headless: headless, profileKey: fmt.Sprintf("%x", sum[:8])}
}

func (m *Manager) Ensure(ctx context.Context) error {
	m.mu.Lock()
	if m.base != "" {
		base := m.base
		m.mu.Unlock()
		if ping(base) == nil {
			return nil
		}
		m.mu.Lock()
		m.base = ""
	}
	defer m.mu.Unlock()
	bin := ""
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return errors.New("Chromium/Chrome not found; install with: sudo apt install chromium-browser (or chromium)")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	profile := filepath.Join(cache, "cos-lite", "chromium-profiles", m.profileKey)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	args := []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + strconv.Itoa(port), "--remote-allow-origins=*", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-sync", "--disable-extensions", "--disable-component-update", "about:blank"}
	if m.headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	if os.Geteuid() == 0 {
		args = append([]string{"--no-sandbox"}, args...)
	}
	cmd := exec.CommandContext(context.Background(), bin, args...)
	// Keep the dedicated browser in its own Linux process group so shutdown also
	// reaches Chromium renderer/GPU/helper children rather than only the browser parent.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ping(base) == nil {
			m.cmd = cmd
			m.base = base
			m.profile = profile
			go cmd.Wait()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return errors.New("Chromium remote debugging endpoint did not start")
}

func ping(base string) error {
	c := http.Client{Timeout: 500 * time.Millisecond}
	r, err := c.Get(base + "/json/version")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("status %d", r.StatusCode)
	}
	return nil
}

func (m *Manager) Tabs(ctx context.Context) ([]Tab, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	base := m.base
	m.mu.Unlock()
	var tabs []Tab
	if err := getJSON(base+"/json/list", &tabs); err != nil {
		return nil, err
	}
	out := tabs[:0]
	for _, t := range tabs {
		if t.Type == "page" {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *Manager) NewTab(ctx context.Context, target string) (Tab, error) {
	if err := m.Ensure(ctx); err != nil {
		return Tab{}, err
	}
	if target == "" {
		target = "about:blank"
	}
	m.mu.Lock()
	base := m.base
	m.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, base+"/json/new?"+url.QueryEscape(target), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Tab{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Tab{}, fmt.Errorf("new tab: HTTP %d", resp.StatusCode)
	}
	var t Tab
	err = json.NewDecoder(resp.Body).Decode(&t)
	return t, err
}

func (m *Manager) CloseTab(ctx context.Context, id string) error {
	if err := m.Ensure(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	base := m.base
	if s := m.sessions[id]; s != nil {
		s.Close()
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/close/"+url.PathEscape(id), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("close tab: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) Session(ctx context.Context, id string) (*cdpSession, error) {
	m.mu.Lock()
	if s := m.sessions[id]; s != nil {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	tabs, err := m.Tabs(ctx)
	if err != nil {
		return nil, err
	}
	var tab *Tab
	for i := range tabs {
		if tabs[i].ID == id {
			tab = &tabs[i]
			break
		}
	}
	if tab == nil {
		return nil, fmt.Errorf("unknown tab_id %q", id)
	}
	s, err := newCDPSession(tab.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if old := m.sessions[id]; old != nil {
		s.Close()
		s = old
	} else {
		m.sessions[id] = s
	}
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) PickTab(ctx context.Context, id string) (Tab, *cdpSession, error) {
	tabs, err := m.Tabs(ctx)
	if err != nil {
		return Tab{}, nil, err
	}
	if id == "" {
		if len(tabs) != 1 {
			return Tab{}, nil, fmt.Errorf("tab_id is required when %d tabs are open", len(tabs))
		}
		id = tabs[0].ID
	}
	var picked Tab
	found := false
	for _, t := range tabs {
		if t.ID == id {
			picked = t
			found = true
			break
		}
	}
	if !found {
		return Tab{}, nil, fmt.Errorf("unknown tab_id %q", id)
	}
	s, err := m.Session(ctx, id)
	return picked, s, err
}

func getJSON(raw string, v any) error {
	r, err := http.Get(raw)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", r.StatusCode)
	}
	return json.NewDecoder(r.Body).Decode(v)
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.Close()
	}
	m.sessions = map[string]*cdpSession{}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = syscall.Kill(-m.cmd.Process.Pid, syscall.SIGTERM)
	}
	m.base = ""
}

func (m *Manager) Evaluate(ctx context.Context, tabID, expr string) (any, error) {
	_, s, err := m.PickTab(ctx, tabID)
	if err != nil {
		return nil, err
	}
	res, err := s.Call("Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true, "userGesture": true}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	r, _ := res["result"].(map[string]any)
	if ex := res["exceptionDetails"]; ex != nil {
		return nil, fmt.Errorf("javascript exception: %v", ex)
	}
	return r["value"], nil
}

func (m *Manager) Navigate(ctx context.Context, tabID, target string) error {
	_, s, err := m.PickTab(ctx, tabID)
	if err != nil {
		return err
	}
	oldURL, _ := m.Evaluate(ctx, tabID, "location.href")
	if _, err := s.Call("Page.navigate", map[string]any{"url": target}, 15*time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := m.Evaluate(ctx, tabID, "document.readyState")
		href, hrefErr := m.Evaluate(ctx, tabID, "location.href")
		changed := fmt.Sprint(href) != fmt.Sprint(oldURL) || target == "about:blank"
		if stateErr == nil && hrefErr == nil && changed && (state == "complete" || state == "interactive") {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("navigation did not reach a ready document within 15s")
}

func (m *Manager) Snapshot(ctx context.Context, tabID string) (any, error) {
	script := `(()=>{function esc(s){return String(s||'').replace(/[^a-zA-Z0-9_-]/g,c=>'\\'+c.codePointAt(0).toString(16)+' ')}function sel(el){if(el.id)return '#'+esc(el.id);let a=[],x=el;while(x&&x.nodeType===1&&a.length<6){let p=x.parentElement;if(!p)break;let same=[...p.children].filter(c=>c.tagName===x.tagName);let q=x.tagName.toLowerCase()+(same.length>1?':nth-of-type('+(same.indexOf(x)+1)+')':'');a.unshift(q);x=p}return a.join(' > ')}let els=[...document.querySelectorAll('a,button,input,textarea,select,[role],[contenteditable="true"]')].slice(0,300).map(el=>({tag:el.tagName.toLowerCase(),selector:sel(el),text:(el.innerText||el.value||'').trim().slice(0,300),aria_label:el.getAttribute('aria-label')||'',role:el.getAttribute('role')||'',type:el.getAttribute('type')||'',name:el.getAttribute('name')||'',placeholder:el.getAttribute('placeholder')||'',disabled:!!el.disabled}));return {url:location.href,title:document.title,text:(document.body?.innerText||'').slice(0,24000),elements:els}})()`
	return m.Evaluate(ctx, tabID, script)
}

func (m *Manager) Screenshot(ctx context.Context, tabID string) (string, error) {
	_, s, err := m.PickTab(ctx, tabID)
	if err != nil {
		return "", err
	}
	res, err := s.Call("Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, 15*time.Second)
	if err != nil {
		return "", err
	}
	data, _ := res["data"].(string)
	if data == "" {
		return "", errors.New("Chromium returned empty screenshot")
	}
	return data, nil
}

func (m *Manager) Console(ctx context.Context, tabID string, clear bool) ([]map[string]any, error) {
	_, s, err := m.PickTab(ctx, tabID)
	if err != nil {
		return nil, err
	}
	return s.consoleEvents(clear), nil
}
func (m *Manager) Network(ctx context.Context, tabID string, clear bool) ([]map[string]any, error) {
	_, s, err := m.PickTab(ctx, tabID)
	if err != nil {
		return nil, err
	}
	return s.networkEvents(clear), nil
}

func (m *Manager) Action(ctx context.Context, tabID, action, selector, value, key string) (any, error) {
	q, _ := json.Marshal(selector)
	v, _ := json.Marshal(value)
	switch strings.ToLower(action) {
	case "click":
		return m.Evaluate(ctx, tabID, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)throw new Error('selector not found');e.click();return true})()`, q))
	case "fill":
		return m.Evaluate(ctx, tabID, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)throw new Error('selector not found');const s=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e),'value')?.set;s?s.call(e,%s):e.value=%s;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return e.value})()`, q, v, v))
	case "type":
		return m.Evaluate(ctx, tabID, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)throw new Error('selector not found');e.focus();e.value=(e.value||'')+%s;e.dispatchEvent(new Event('input',{bubbles:true}));return e.value})()`, q, v))
	case "focus":
		return m.Evaluate(ctx, tabID, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)throw new Error('selector not found');e.focus();return true})()`, q))
	case "press":
		_, s, err := m.PickTab(ctx, tabID)
		if err != nil {
			return nil, err
		}
		if key == "" {
			key = value
		}
		if key == "" {
			return nil, errors.New("key is required for press")
		}
		for _, typ := range []string{"keyDown", "keyUp"} {
			if _, err := s.Call("Input.dispatchKeyEvent", map[string]any{"type": typ, "key": key, "text": func() string {
				if len(key) == 1 {
					return key
				}
				return ""
			}()}, 5*time.Second); err != nil {
				return nil, err
			}
		}
		return true, nil
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}
