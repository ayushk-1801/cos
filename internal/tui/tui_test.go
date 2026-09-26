//go:build linux

package tui

import "testing"

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
