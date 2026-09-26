//go:build linux

package tui

import (
	"strings"
	"testing"
)

func TestInputAcceptsQAndBackspace(t *testing.T) {
	m := Model{screen: addProject, input: "/tmp/pro"}
	if m.handleInput(key{kind: "rune", r: 'q'}) {
		t.Fatal("unexpected quit")
	}
	if m.input != "/tmp/proq" {
		t.Fatalf("input=%q", m.input)
	}
	m.handleInput(key{kind: "backspace"})
	if m.input != "/tmp/pro" {
		t.Fatalf("input=%q", m.input)
	}
}

func TestBackspaceRemovesWholeRune(t *testing.T) {
	m := Model{screen: addProject, input: "/tmp/प्र"}
	m.handleInput(key{kind: "backspace"})
	if m.input != "/tmp/प्" {
		t.Fatalf("input=%q", m.input)
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("http://127.0.0.1:8765/mcp/supersecret")
	if got != "http://127.0.0.1:8765/mcp/********" {
		t.Fatalf("got %q", got)
	}
}

func TestSecretInputDoesNotRenderSecret(t *testing.T) {
	var b strings.Builder
	renderSecretInput(&b, "OpenAI", "Runtime API key", "sk-do-not-render", "help")
	if strings.Contains(b.String(), "sk-do-not-render") {
		t.Fatalf("secret leaked in render: %q", b.String())
	}
	if !strings.Contains(b.String(), "••") {
		t.Fatalf("expected masked input: %q", b.String())
	}
}

func TestLogsViewRTogglesRawMode(t *testing.T) {
	m := Model{screen: logsView}
	m.handle(key{kind: "rune", r: 'r'})
	if !m.logsRaw {
		t.Fatal("expected raw log mode")
	}
	m.handle(key{kind: "rune", r: 'r'})
	if m.logsRaw {
		t.Fatal("expected compact log mode")
	}
}

func TestClipText(t *testing.T) {
	if got := clipText("abcdefgh", 5); got != "abcd…" {
		t.Fatalf("got %q", got)
	}
	if got := clipText("abc", 5); got != "abc" {
		t.Fatalf("got %q", got)
	}
}
