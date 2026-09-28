package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNewModelLoadsDocumentBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte("# Hello\n\nSome text here.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(Config{Path: path}, "").(model)
	if m.state != stateShowDocument {
		t.Fatalf("expected stateShowDocument, got %s", m.state)
	}
	if !strings.Contains(m.pager.currentDocument.Body, "Some text") {
		t.Errorf("expected document body to be loaded, got %q", m.pager.currentDocument.Body)
	}
}

func TestSearchKeyRouting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	var tm tea.Model = newModel(Config{Path: path}, "")
	send := func(msgs ...tea.Msg) tea.Cmd {
		var cmd tea.Cmd
		for _, msg := range msgs {
			tm, cmd = tm.Update(msg)
		}
		return cmd
	}
	key := func(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }
	send(tea.WindowSizeMsg{Width: 80, Height: 24}, contentRenderedMsg("alpha\nbeta alpha"))

	// Keys that would quit or leave the document go to the prompt instead
	send(key("/"), key("q"), key("h"), tea.KeyPressMsg{Code: tea.KeyLeft}, tea.KeyPressMsg{Code: tea.KeyDelete})
	m := tm.(model)
	if m.state != stateShowDocument || !m.pager.searching || m.pager.searchInput.Value() != "q" {
		t.Fatalf("expected keys to go to the prompt, got state %s, value %q", m.state, m.pager.searchInput.Value())
	}

	// Esc cancels the prompt, not the document
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m := tm.(model); m.state != stateShowDocument || m.pager.searching {
		t.Fatalf("expected esc to cancel the prompt, got state %s", m.state)
	}

	// Esc clears matches first, then leaves the document
	send(key("/"), key("a"), key("l"), tea.KeyPressMsg{Code: tea.KeyEnter})
	if m := tm.(model); len(m.pager.matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(m.pager.matches))
	}
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m := tm.(model); m.state != stateShowDocument || len(m.pager.matches) != 0 {
		t.Fatalf("expected esc to clear matches, got state %s, %d matches", m.state, len(m.pager.matches))
	}
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m := tm.(model); m.state != stateShowStash {
		t.Fatalf("expected esc to leave the document, got state %s", m.state)
	}
}

func TestSearchCtrlCAndCtrlZ(t *testing.T) {
	var tm tea.Model = newModel(Config{}, "")
	m := tm.(model)
	m.state = stateShowDocument
	m.pager.searching = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected ctrl+c to quit while searching")
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(tea.SuspendMsg); !ok {
		t.Errorf("expected ctrl+z to suspend while searching")
	}
}
