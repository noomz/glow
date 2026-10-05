package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestGlamourRenderTextSizing(t *testing.T) {
	t.Setenv("GLOW_TEXT_SIZING", "on")

	common := &commonModel{
		cfg: Config{
			GlamourEnabled:  true,
			GlamourStyle:    "dark",
			GlamourMaxWidth: 80,
		},
		styles: newStyles(true),
		width:  80,
		height: 24,
	}
	m := newPagerModel(common)
	m.currentDocument = markdown{Note: "test.md"}
	m.setSize(80, 24)

	out, err := glamourRender(m, "# Hello\n\n## World\n")
	if err != nil {
		t.Fatal(err)
	}
	// The dark style's H1 has a background color, which renders broken when
	// scaled, so it is left at normal size.
	if strings.Contains(out, "\x1b]66;s=3;") {
		t.Errorf("expected H1 with background to not be scaled, got %q", out)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("expected H1 text in pager output, got %q", out)
	}
	if !strings.Contains(out, "\x1b]66;s=2;World\x1b\\") {
		t.Errorf("expected H2 at 2x scale in pager output, got %q", out)
	}
	if strings.Contains(out, "\x1b]6666;") {
		t.Errorf("expected no leftover markers in pager output, got %q", out)
	}
}

func TestFindMatchesSmartcase(t *testing.T) {
	content := "Foo foo FOO"
	if got := findMatches(content, "foo"); len(got) != 3 {
		t.Errorf("expected lowercase pattern to match case-insensitively, got %v", got)
	}
	if got := findMatches(content, "Foo"); !reflect.DeepEqual(got, [][]int{{0, 3}}) {
		t.Errorf("expected pattern with uppercase to match case-sensitively, got %v", got)
	}
	if got := findMatches(content, ""); got != nil {
		t.Errorf("expected empty pattern to not match, got %v", got)
	}
}

func TestFindMatchesRegex(t *testing.T) {
	if got := findMatches("fo foo fooo", "fo{2,}"); !reflect.DeepEqual(got, [][]int{{3, 6}, {7, 11}}) {
		t.Errorf("expected regex matches, got %v", got)
	}
	// Invalid regular expressions are matched literally
	if got := findMatches("x a(b y", "a(b"); !reflect.DeepEqual(got, [][]int{{2, 5}}) {
		t.Errorf("expected literal match for invalid regex, got %v", got)
	}
	// Empty matches are dropped
	if got := findMatches("bbb", "a*"); len(got) != 0 {
		t.Errorf("expected no empty matches, got %v", got)
	}
}

func TestFindMatchesGutter(t *testing.T) {
	styles := newStyles(true)
	content := styles.lineNumberStyle("   1") + "one 1\n" +
		styles.lineNumberStyle("   2") + "two\n" +
		styles.lineNumberStyle("  11") + "1"
	// Offsets into "   1one 1\n   2two\n  111"
	want := [][]int{{8, 9}, {22, 23}}
	if got := findMatches(blankGutter(searchText(content), lineNumberWidth), "1"); !reflect.DeepEqual(got, want) {
		t.Errorf("expected gutter to be excluded, got %v, want %v", got, want)
	}
	if got := findMatches(searchText(content), "1"); len(got) != 5 {
		t.Errorf("expected gutter matches without a gutter, got %v", got)
	}
}

func TestHighlightMatches(t *testing.T) {
	styles := newStyles(true)
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render
	content := red("aaaa") + "\n" + red("bb foo") + "\ncc foo"

	out := highlightAll(t, content, "foo", 1)
	if xansi.Strip(out) != xansi.Strip(content) {
		t.Errorf("expected highlighting to keep the text, got %q", xansi.Strip(out))
	}
	lines := strings.Split(out, "\n")
	if lines[0] != red("aaaa") {
		t.Errorf("expected first line to be untouched, got %q", lines[0])
	}
	if want := styles.searchMatchStyle.Render("foo"); !strings.Contains(lines[1], want) {
		t.Errorf("expected match on second line, got %q", lines[1])
	}
	if want := styles.searchSelectedMatchStyle.Render("foo"); !strings.HasSuffix(lines[2], want) {
		t.Errorf("expected selected match on third line, got %q", lines[2])
	}
}

func newTestPager(t *testing.T, content string) pagerModel {
	t.Helper()
	common := &commonModel{
		styles: newStyles(true),
		width:  80,
		height: 24,
	}
	m := newPagerModel(common)
	m.currentDocument = markdown{Note: "test.md"}
	m.setSize(80, 24)
	m, _ = m.update(contentRenderedMsg(content))
	return m
}

// highlightAll returns the pager's view of content with pattern searched and
// the given match selected, one row per line of content.
func highlightAll(t *testing.T, content, pattern string, selected int) string {
	t.Helper()
	m := newTestPager(t, content)
	m.search(pattern)
	m.matchIndex = selected
	return strings.Join(viewRows(m, strings.Count(content, "\n")+1), "\n")
}

// viewRows returns the first n rows of the pager's view without the padding
// the viewport adds.
func viewRows(m pagerModel, n int) []string {
	rows := strings.Split(m.View(), "\n")[:n]
	for i, row := range rows {
		rows[i] = strings.TrimRight(row, " ")
	}
	return rows
}

func typeKeys(m pagerModel, keys ...tea.KeyPressMsg) pagerModel {
	for _, k := range keys {
		m, _ = m.update(k)
	}
	return m
}

func runeKeys(s string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, r := range s {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}

func TestPagerSearch(t *testing.T) {
	m := newTestPager(t, strings.Repeat("line\n", 50)+"alpha\nbeta alpha\ngamma")

	// Keys typed into the prompt aren't handled as pager commands
	m = typeKeys(m, runeKeys("/jjdG")...)
	if !m.searching || m.searchInput.Value() != "jjdG" {
		t.Fatalf("expected keys to go to the prompt, got %q", m.searchInput.Value())
	}
	if m.viewport.YOffset() != 0 {
		t.Errorf("expected viewport not to scroll while typing, got offset %d", m.viewport.YOffset())
	}

	// Cancelling restores the previous state
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.searching || len(m.matches) != 0 {
		t.Fatalf("expected search to be cancelled, got %d matches", len(m.matches))
	}

	m = typeKeys(m, runeKeys("/alpha")...)
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.searching || m.searchQuery != "alpha" || len(m.matches) != 2 || m.matchIndex != 0 {
		t.Fatalf("expected 2 matches for committed search, got %d (index %d)", len(m.matches), m.matchIndex)
	}
	if m.viewport.YOffset() == 0 {
		t.Errorf("expected viewport to scroll to the match")
	}
	if want := m.common.styles.searchSelectedMatchStyle.Render("alpha"); !strings.Contains(m.View(), want) {
		t.Errorf("expected the view to highlight the selected match, got %q", m.View())
	}

	m = typeKeys(m, runeKeys("n")...)
	if m.matchIndex != 1 {
		t.Errorf("expected next match, got %d", m.matchIndex)
	}
	m = typeKeys(m, runeKeys("n")...)
	if m.matchIndex != 0 {
		t.Errorf("expected next match to wrap, got %d", m.matchIndex)
	}
	m = typeKeys(m, runeKeys("N")...)
	if m.matchIndex != 1 {
		t.Errorf("expected previous match to wrap, got %d", m.matchIndex)
	}

	// Esc clears the matches
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.searchQuery != "" || len(m.matches) != 0 {
		t.Errorf("expected esc to clear the search, got %d matches", len(m.matches))
	}

	// Committing a search without matches reports it
	m = typeKeys(m, runeKeys("/nope")...)
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.state != pagerStateStatusMessage || m.statusMessage != "Pattern not found" {
		t.Errorf("expected pattern not found message, got %q", m.statusMessage)
	}
	m.statusMessageTimer.Stop()
}

func TestPagerSearchRerender(t *testing.T) {
	m := newTestPager(t, "alpha\nbeta alpha\ngamma alpha")
	m = typeKeys(m, runeKeys("/alpha")...)
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeKeys(m, runeKeys("nn")...)
	if len(m.matches) != 3 || m.matchIndex != 2 {
		t.Fatalf("expected third of 3 matches, got %d/%d", m.matchIndex+1, len(m.matches))
	}

	// The search is re-run on reload, clamping the current match
	m, _ = m.update(contentRenderedMsg("alpha\nALPHA"))
	if len(m.matches) != 2 || m.matchIndex != 1 {
		t.Errorf("expected second of 2 matches after reload, got %d/%d", m.matchIndex+1, len(m.matches))
	}
	if want := m.common.styles.searchSelectedMatchStyle.Render("ALPHA"); !strings.Contains(m.View(), want) {
		t.Errorf("expected reloaded content to be highlighted, got %q", m.View())
	}
}

func TestFindMatchesMultiline(t *testing.T) {
	// ^ and $ match at line boundaries
	if got := findMatches("foo\nxfoo\nfoo", "^foo$"); !reflect.DeepEqual(got, [][]int{{0, 3}, {9, 12}}) {
		t.Errorf("expected per-line anchors, got %v", got)
	}
	// Escapes like \S don't count as uppercase for smartcase
	if got := findMatches("FOO bar", `\S+o`); !reflect.DeepEqual(got, [][]int{{0, 3}}) {
		t.Errorf("expected escaped uppercase to be ignored for smartcase, got %v", got)
	}
}

func TestHighlightMatchesWide(t *testing.T) {
	sel := newStyles(true).searchSelectedMatchStyle
	content := "日本語 foo"
	out := highlightAll(t, content, "foo", 0)
	if want := "日本語 " + sel.Render("foo"); out != want {
		t.Errorf("expected match after wide characters to be highlighted, got %q, want %q", out, want)
	}
}

func TestHighlightTabs(t *testing.T) {
	sel := newStyles(true).searchSelectedMatchStyle
	for _, content := range []string{"a\tfoo\nz", "\x1b]66;s=2;a\tfoo\x1b\\\nz"} {
		m := newTestPager(t, content)
		m.search("foo")
		first := strings.TrimRight(strings.SplitN(m.View(), "\n", 2)[0], " ")
		want := "a    " + sel.Render("foo")
		if strings.HasPrefix(content, "\x1b]66;") {
			want = "\x1b]66;s=2;a    \x1b\\" + sel.Render("\x1b]66;s=2;foo\x1b\\")
		}
		if first != want {
			t.Errorf("expected the highlight to follow the tab shown as spaces, got %q, want %q", first, want)
		}
	}
}

func TestHighlightScrolledHorizontally(t *testing.T) {
	sel := newStyles(true).searchSelectedMatchStyle
	osc := func(s string) string { return "\x1b]66;s=2;" + s + "\x1b\\" }
	long := "\n" + strings.Repeat("x", 40) // lets the viewport scroll
	for _, tt := range []struct {
		name, content  string
		width, xOffset int
		pattern, want  string
	}{
		{"plain", "abcdefghij foo xyz", 10, 8, "foo", "ij " + sel.Render("foo") + " xyz"},
		// The viewport keeps the wide character the left edge falls in
		{"wide character at the left edge", "日本語日本語 foo" + long, 14, 3, "foo", "本語日本語 " + sel.Render("foo")},
		// Sized text is zero-width to the viewport, so only the plain text scrolls
		{"sized heading", "    " + osc("Title") + long, 20, 3, "itl", " " + osc("T") + sel.Render(osc("itl")) + osc("e")},
		{"match scrolled off", "foo " + strings.Repeat("x", 30), 10, 20, "foo", "xxxxxxxxxx"},
	} {
		m := newTestPager(t, tt.content)
		m.setSize(tt.width, 5)
		m.search(tt.pattern)
		m.viewport.SetXOffset(tt.xOffset)
		if got := viewRows(m, 1)[0]; got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHighlightZeroWidthLine(t *testing.T) {
	// The viewport shows nothing for a lone line it measures as zero width
	m := newTestPager(t, "\x1b]66;s=2;Title\x1b\\")
	m.search("itl")
	if row := viewRows(m, 1)[0]; row != "" || m.viewport.TotalLineCount() != 0 {
		t.Errorf("expected nothing to highlight on an empty view, got row %q with %d lines", row, m.viewport.TotalLineCount())
	}
}

func TestHighlightMatchesSpanningLines(t *testing.T) {
	sel := newStyles(true).searchSelectedMatchStyle
	content := "ab\ncd"
	out := highlightAll(t, content, `b\nc`, 0)
	if want := "a" + sel.Render("b") + "\n" + sel.Render("c") + "d"; out != want {
		t.Errorf("expected match to be highlighted on both lines, got %q, want %q", out, want)
	}
}

func TestHighlightMatchesTextSizing(t *testing.T) {
	styles := newStyles(true)
	sel := styles.searchSelectedMatchStyle
	osc := func(meta, s string) string { return "\x1b]66;" + meta + ";" + s + "\x1b\\" }
	bold := "\x1b[1m"
	content := bold + osc("s=2", "Title") + "\x1b[m\n" +
		osc("s=2:n=3:d=4:w=3", "Abcd") + "\n" +
		osc("s=3", "Other")

	if got := searchText(content); got != "Title\nAbcd\nOther" {
		t.Fatalf("expected sized text to be searchable, got %q", got)
	}

	matches := findMatches(searchText(content), "itl|bc")
	if !reflect.DeepEqual(matches, [][]int{{1, 4}, {7, 9}}) {
		t.Fatalf("expected matches in sized text, got %v", matches)
	}

	lines := strings.Split(highlightAll(t, content, "itl|bc", 0), "\n")
	// Sized text is split around the match, restoring the style after it
	if want := bold + osc("s=2", "T") + sel.Render(osc("s=2", "itl")) + bold + osc("s=2", "e") + "\x1b[m"; lines[0] != want {
		t.Errorf("expected sized heading to be highlighted in place, got %q, want %q", lines[0], want)
	}
	// Fractionally scaled text is highlighted a whole sequence at a time
	if want := styles.searchMatchStyle.Render(osc("s=2:n=3:d=4:w=3", "Abcd")); lines[1] != want {
		t.Errorf("expected fractionally sized text to be highlighted whole, got %q, want %q", lines[1], want)
	}
	if want := osc("s=3", "Other"); lines[2] != want {
		t.Errorf("expected line without matches to be untouched, got %q", lines[2])
	}
}

func TestHighlightSkipsEmptyRanges(t *testing.T) {
	// A match spanning an empty line has nothing to style there
	if lines := strings.Split(highlightAll(t, "a\n\nb", `a\n\nb`, 0), "\n"); lines[1] != "" {
		t.Errorf("expected the empty line untouched, got %q", lines[1])
	}

	// Nor on a line scrolled entirely out of view
	m := newTestPager(t, "foo\nbar\n"+strings.Repeat("x", 60))
	m.setSize(10, 8)
	m.search(`foo\nbar`)
	m.viewport.SetXOffset(20)
	if row := viewRows(m, 1)[0]; row != "" {
		t.Errorf("expected the line left of the view untouched, got %q", row)
	}
}

func TestPagerSearchRerenderWithoutMatches(t *testing.T) {
	m := newTestPager(t, "alpha\nbeta")
	m = typeKeys(m, runeKeys("/alpha")...)
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.update(contentRenderedMsg("beta"))
	if m.searchQuery != "" || len(m.matches) != 0 {
		t.Errorf("expected search to be cleared when reload removes all matches, got %q", m.searchQuery)
	}
}

func TestPagerSearchRerenderWhileSearching(t *testing.T) {
	m := newTestPager(t, "alpha")
	m = typeKeys(m, runeKeys("/omega")...)
	if len(m.matches) != 0 {
		t.Fatalf("expected no matches, got %d", len(m.matches))
	}

	m, _ = m.update(contentRenderedMsg(strings.Repeat("line\n", 50) + "omega"))
	if !m.searching || len(m.matches) != 1 {
		t.Fatalf("expected reload to re-run the search, got %d matches", len(m.matches))
	}
	if m.viewport.YOffset() == 0 {
		t.Errorf("expected reload to scroll to the match")
	}
}

func TestHighlightSizedLine(t *testing.T) {
	styles := newStyles(true)
	sel := styles.searchSelectedMatchStyle
	osc := func(meta, s string) string { return "\x1b]66;" + meta + ";" + s + "\x1b\\" }
	highlight := func(content, pattern string) string {
		// A lone sized line is zero-width to the viewport, which drops it
		return strings.TrimSuffix(highlightAll(t, content+"\nz", pattern, 0), "\nz")
	}

	tests := []struct {
		name, content, pattern, want string
	}{
		{
			"selected match wins in fractionally sized text",
			osc("s=2:n=3:d=4:w=3", "abab"), "ab",
			sel.Render(osc("s=2:n=3:d=4:w=3", "abab")),
		},
		{
			"wide characters in sized text",
			osc("s=2", "日本語"), "本",
			osc("s=2", "日") + sel.Render(osc("s=2", "本")) + osc("s=2", "語"),
		},
		{
			"match crossing from sized into plain text",
			osc("s=2", "Ti") + "tle x", "itl",
			osc("s=2", "T") + sel.Render(osc("s=2", "i")) + sel.Render("tl") + "e x",
		},
	}
	for _, tt := range tests {
		if got := highlight(tt.content, tt.pattern); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestParseOSC66(t *testing.T) {
	for _, tt := range []struct {
		s          string
		text, rest string
	}{
		{"\x1b]66;s=2;ab\x1b\\cd", "ab", "cd"},
		{"\x1b]66;s=2;ab\acd", "ab", "cd"},
		// A lone ESC ends the sequence and starts the next one
		{"\x1b]66;s=2;ab\x1b[m", "ab", "\x1b[m"},
		{"\x1b]66;s=2;ab", "ab", ""},
	} {
		meta, text, n := parseOSC66(tt.s)
		if meta != "s=2" || text != tt.text || tt.s[n:] != tt.rest {
			t.Errorf("parseOSC66(%q) = %q, %q, rest %q", tt.s, meta, text, tt.s[n:])
		}
	}
}

func TestPagerSearchCancelAfterReload(t *testing.T) {
	m := newTestPager(t, "alpha")
	m = typeKeys(m, runeKeys("/alpha")...)
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// The previous search has no matches in the reloaded content
	m = typeKeys(m, runeKeys("/")...)
	m, _ = m.update(contentRenderedMsg("beta"))
	m = typeKeys(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.searching || m.searchQuery != "" || len(m.matches) != 0 {
		t.Errorf("expected cancel to clear a search without matches, got %q", m.searchQuery)
	}
}
