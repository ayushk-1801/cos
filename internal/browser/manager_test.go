package browser

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestChromiumCDPSmoke(t *testing.T) {
	if _, err := exec.LookPath("chromium"); err != nil {
		t.Skip("chromium unavailable")
	}
	ctx := context.Background()
	m := NewManager(true)
	defer m.Shutdown()
	if err := m.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	tab, err := m.NewTab(ctx, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Navigate(ctx, tab.ID, "about:blank"); err != nil {
		t.Fatal(err)
	}
	_, err = m.Evaluate(ctx, tab.ID, `(()=>{document.body.innerHTML='<button id="b">Go</button><input id="i">';document.querySelector('#b').onclick=()=>{document.body.dataset.clicked='yes';console.log('clicked')};return true})()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Action(ctx, tab.ID, "fill", "#i", "hello", ""); err != nil {
		t.Fatal(err)
	}
	v, err := m.Evaluate(ctx, tab.ID, "document.querySelector('#i').value")
	if err != nil {
		t.Fatal(err)
	}
	if v != "hello" {
		t.Fatalf("value=%v", v)
	}
	if _, err := m.Action(ctx, tab.ID, "click", "#b", "", ""); err != nil {
		t.Fatal(err)
	}
	v, err = m.Evaluate(ctx, tab.ID, "document.body.dataset.clicked")
	if err != nil || v != "yes" {
		t.Fatalf("clicked=%v err=%v", v, err)
	}
	png, err := m.Screenshot(ctx, tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 100 {
		t.Fatalf("short screenshot %d", len(png))
	}
	events, err := m.Console(ctx, tab.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	methods := ""
	for _, e := range events {
		methods += e["method"].(string) + " "
	}
	if !strings.Contains(methods, "consoleAPICalled") {
		t.Fatalf("console events=%v", events)
	}
}
