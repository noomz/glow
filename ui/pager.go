package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glow/v3/utils"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/log"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"
	runewidth "github.com/mattn/go-runewidth"
	"github.com/muesli/reflow/ansi"
	"github.com/muesli/reflow/truncate"
	"github.com/muesli/termenv"
	"github.com/rivo/uniseg"
)

const (
	statusBarHeight = 1
	lineNumberWidth = 4

	// Prefix of kitty's text sizing sequences, used for headings.
	osc66Prefix = "\x1b]66;"
)

var pagerHelpHeight int

type (
	contentRenderedMsg string
	reloadMsg          struct{}
)

type pagerState int

const (
	pagerStateBrowse pagerState = iota
	pagerStateStatusMessage
)

type pagerModel struct {
	common   *commonModel
	viewport viewport.Model
	state    pagerState
	showHelp bool

	statusMessage      string
	statusMessageTimer *time.Timer

	// Current document being rendered, sans-glamour rendering. We cache
	// it here so we can re-render it on resize.
	currentDocument markdown

	// The rendered content's lines, its searchText with the line number
	// gutter blanked, and the offset of each line in that text. Searches and
	// highlights work on the stripped text.
	lines      []string
	stripped   string
	lineStarts []int

	// Search state. While the prompt is open, the prompt's value is the
	// active pattern; otherwise it's searchQuery.
	searchInput textinput.Model
	searching   bool
	searchQuery string
	matches     [][]int
	matchLines  []int
	matchIndex  int

	// Search state to restore when the prompt is cancelled.
	prevSearchQuery string
	prevMatchOffset int
	prevYOffset     int

	watcher *fsnotify.Watcher
}

func newPagerModel(common *commonModel) pagerModel {
	// Init viewport
	vp := viewport.New()

	si := textinput.New()
	si.Prompt = "/"
	// The prompt is rendered inline into the status bar, so draw the cursor
	// as part of the string rather than moving the real one.
	si.SetVirtualCursor(true)

	m := pagerModel{
		common:      common,
		state:       pagerStateBrowse,
		viewport:    vp,
		searchInput: si,
	}
	m.setContent("")
	m.initWatcher()
	return m
}

func (m *pagerModel) setSize(w, h int) {
	m.viewport.SetWidth(w)
	m.viewport.SetHeight(h - statusBarHeight)
	m.searchInput.SetWidth(w - 2) // leave room for the prompt and cursor

	if m.showHelp {
		if pagerHelpHeight == 0 {
			pagerHelpHeight = strings.Count(m.helpView(), "\n")
		}
		m.viewport.SetHeight(m.viewport.Height() - (statusBarHeight + pagerHelpHeight))
	}
}

func (m *pagerModel) setContent(s string) {
	// The viewport would draw tabs as spaces anyway. Doing it here keeps the
	// search offsets in step with what's drawn.
	s = strings.ReplaceAll(s, "\t", "    ")
	m.viewport.SetContent(s)
	if m.viewport.TotalLineCount() == 0 {
		// The viewport shows nothing for a lone line it measures as zero width
		s = ""
	}
	m.lines = strings.Split(s, "\n")
	m.stripped = blankGutter(searchText(s), m.gutterWidth())
	m.lineStarts = []int{0}
	for i, c := range []byte(m.stripped) {
		if c == '\n' {
			m.lineStarts = append(m.lineStarts, i+1)
		}
	}
}

func (m *pagerModel) toggleHelp() {
	m.showHelp = !m.showHelp
	m.setSize(m.common.width, m.common.height)
	if m.viewport.PastBottom() {
		m.viewport.GotoBottom()
	}
}

type pagerStatusMessage struct {
	message string
	isError bool
}

// Perform stuff that needs to happen after a successful markdown stash. Note
// that the returned command should be sent back the through the pager
// update function.
func (m *pagerModel) showStatusMessage(msg pagerStatusMessage) tea.Cmd {
	// Show a success message to the user
	m.state = pagerStateStatusMessage
	m.statusMessage = msg.message
	if m.statusMessageTimer != nil {
		m.statusMessageTimer.Stop()
	}
	m.statusMessageTimer = time.NewTimer(statusMessageTimeout)

	return waitForStatusMessageTimeout(pagerContext, m.statusMessageTimer)
}

func (m *pagerModel) unload() {
	log.Debug("unload")
	if m.showHelp {
		m.toggleHelp()
	}
	if m.statusMessageTimer != nil {
		m.statusMessageTimer.Stop()
	}
	m.state = pagerStateBrowse
	m.stopSearching()
	m.clearSearch()
	m.setContent("")
	m.viewport.SetYOffset(0)
	m.unwatchFile()
}

func (m pagerModel) update(msg tea.Msg) (pagerModel, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// While the search prompt is open, keys go to the prompt only
		if m.searching {
			return m.handleSearchKey(msg)
		}

		switch msg.String() {
		case "q", keyEsc:
			if msg.String() == keyEsc && len(m.matches) > 0 {
				m.clearSearch()
				return m, nil
			}
			if m.state != pagerStateBrowse {
				m.state = pagerStateBrowse
				return m, nil
			}
		case "home", "g":
			m.viewport.GotoTop()
		case "end", "G":
			m.viewport.GotoBottom()

		case "d":
			m.viewport.HalfPageDown()

		case "u":
			m.viewport.HalfPageUp()

		case "e":
			lineno := int(math.RoundToEven(float64(m.viewport.TotalLineCount()) * m.viewport.ScrollPercent()))
			if m.viewport.AtTop() {
				lineno = 0
			}
			log.Info(
				"opening editor",
				"file", m.currentDocument.localPath,
				"line", fmt.Sprintf("%d/%d", lineno, m.viewport.TotalLineCount()),
			)
			return m, openEditor(m.currentDocument.localPath, lineno)

		case "c":
			// Copy using OSC 52
			termenv.Copy(m.currentDocument.Body)
			// Copy using native system clipboard
			_ = clipboard.WriteAll(m.currentDocument.Body)
			cmds = append(cmds, m.showStatusMessage(pagerStatusMessage{"Copied contents", false}))

		case "r":
			return m, loadLocalMarkdown(&m.currentDocument)

		case "/":
			m.prevSearchQuery = m.searchQuery
			if len(m.matches) > 0 {
				m.prevMatchOffset = m.matches[m.matchIndex][0]
			}
			m.prevYOffset = m.viewport.YOffset()
			m.searching = true
			m.searchInput.Reset()
			return m, m.searchInput.Focus()

		case "n":
			if len(m.matches) > 0 {
				m.selectMatch((m.matchIndex + 1) % len(m.matches))
			}

		case "N":
			if len(m.matches) > 0 {
				m.selectMatch((m.matchIndex - 1 + len(m.matches)) % len(m.matches))
			}

		case "?":
			m.toggleHelp()
		}

	// Glow has rendered the content
	case contentRenderedMsg:
		log.Info("content rendered", "state", m.state)

		old, oldStarts := m.stripped, m.lineStarts
		m.setContent(string(msg))
		cmds = append(cmds, m.watchFile)

		switch {
		case m.searching:
			// Search from where the prompt was opened, clamped to the new
			// content, as while typing.
			m.prevMatchOffset = shiftOffset(old, m.stripped, m.prevMatchOffset)
			top := shiftOffset(old, m.stripped, oldStarts[m.prevYOffset])
			m.prevYOffset = sort.Search(len(m.lineStarts), func(i int) bool {
				return m.lineStarts[i] > top
			}) - 1
			m.viewport.SetYOffset(m.prevYOffset)
			m.prevYOffset = m.viewport.YOffset()
			m.incSearch()
		case m.searchQuery != "":
			off := shiftOffset(old, m.stripped, m.matches[m.matchIndex][0])
			m.search(m.searchQuery)
			if len(m.matches) == 0 {
				m.clearSearch()
				break
			}
			m.matchIndex = matchFrom(m.matches, off)
		}

	// The file was changed on disk and we're reloading it
	case reloadMsg:
		return m, loadLocalMarkdown(&m.currentDocument)

	// We've finished editing the document, potentially making changes. Let's
	// retrieve the latest version of the document so that we display
	// up-to-date contents.
	case editorFinishedMsg:
		return m, loadLocalMarkdown(&m.currentDocument)

	// We've received terminal dimensions, either for the first time or
	// after a resize
	case tea.WindowSizeMsg:
		return m, renderWithGlamour(m, m.currentDocument.Body)

	case statusMessageTimeoutMsg:
		m.state = pagerStateBrowse
	}

	// Non-key messages, like cursor blinks and pastes, for the search prompt
	if m.searching {
		cmds = append(cmds, m.updateSearchInput(msg))
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// handleSearchKey handles key presses while the search prompt is open.
func (m pagerModel) handleSearchKey(msg tea.KeyPressMsg) (pagerModel, tea.Cmd) {
	switch msg.String() {
	case keyEnter:
		if m.searchInput.Value() == "" {
			m.cancelSearch()
			return m, nil
		}
		m.stopSearching()
		if len(m.matches) == 0 {
			m.clearSearch()
			m.viewport.SetYOffset(m.prevYOffset)
			return m, m.showStatusMessage(pagerStatusMessage{"Pattern not found", true})
		}
		m.searchQuery = m.searchInput.Value()
		return m, nil

	case keyEsc:
		m.cancelSearch()
		return m, nil
	}

	return m, m.updateSearchInput(msg)
}

// updateSearchInput updates the search prompt and, if the pattern changed,
// highlights its matches as you type, like vim's incsearch.
func (m *pagerModel) updateSearchInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	pattern := m.searchInput.Value()
	m.searchInput, cmd = m.searchInput.Update(msg)
	if m.searchInput.Value() != pattern {
		m.incSearch()
	}
	return cmd
}

// incSearch searches for the prompt's pattern from where the prompt was
// opened, not from the last match, and selects the nearest match.
func (m *pagerModel) incSearch() {
	m.viewport.SetYOffset(m.prevYOffset)
	m.search(m.searchInput.Value())
	if len(m.matches) > 0 {
		m.selectMatch(nearestMatch(m.matchLines, m.prevYOffset))
	}
}

// cancelSearch closes the search prompt, restoring the previous search and
// scroll position.
func (m *pagerModel) cancelSearch() {
	m.stopSearching()
	m.searchQuery = m.prevSearchQuery
	m.search(m.searchQuery)
	if len(m.matches) == 0 {
		// The content may have changed since the prompt was opened
		m.clearSearch()
	} else {
		m.matchIndex = matchFrom(m.matches, m.prevMatchOffset)
	}
	m.viewport.SetYOffset(m.prevYOffset)
}

func (m *pagerModel) stopSearching() {
	m.searching = false
	m.searchInput.Blur()
}

// clearSearch removes the active search and its highlights.
func (m *pagerModel) clearSearch() {
	m.searchQuery = ""
	m.matches = nil
	m.matchLines = nil
	m.matchIndex = 0
}

func (m *pagerModel) search(pattern string) {
	m.matches = findMatches(m.stripped, pattern)
	m.matchLines = make([]int, len(m.matches))
	m.matchIndex = 0

	line := 0
	for i, match := range m.matches {
		for line+1 < len(m.lineStarts) && m.lineStarts[line+1] <= match[0] {
			line++
		}
		m.matchLines[i] = line
	}
}

// selectMatch makes the given match the current one and scrolls to it.
func (m *pagerModel) selectMatch(i int) {
	m.matchIndex = i
	m.viewport.EnsureVisible(m.matchLines[i], 0, 0)
}

// gutterWidth returns the width of the line number gutter, if shown.
func (m pagerModel) gutterWidth() int {
	isCode := !utils.IsMarkdownFile(m.currentDocument.Note)
	if m.common.cfg.GlamourEnabled && (isCode || m.common.cfg.ShowLineNumbers) {
		return lineNumberWidth
	}
	return 0
}

// blankGutter replaces the line numbers in text, the first gutter columns of
// each line, with spaces, so they're neither matched nor compared across
// reloads. Line numbers are ASCII, so byte offsets stay aligned.
func blankGutter(text string, gutter int) string {
	if gutter == 0 {
		return text
	}
	buf := []byte(text)
	start := 0
	for i, line := range strings.Split(text, "\n") {
		w := min(len(line), max(gutter, len(strconv.Itoa(i+1))))
		for j := start; j < start+w; j++ {
			buf[j] = ' '
		}
		start += len(line) + 1
	}
	return string(buf)
}

// findMatches returns the byte offsets of all matches for pattern in text.
// ^ and $ match at line boundaries. Patterns without uppercase letters, not
// counting escapes like \S, match case-insensitively (smartcase) and invalid
// regular expressions match literally.
func findMatches(text, pattern string) [][]int {
	if pattern == "" {
		return nil
	}

	flags := "(?mi)"
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case unicode.IsUpper(r):
			flags = "(?m)"
		}
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		re = regexp.MustCompile(flags + regexp.QuoteMeta(pattern))
	}

	// Drop empty matches, e.g. from "a*", as there's nothing to highlight
	matches := re.FindAllStringIndex(text, -1)
	n := 0
	for _, match := range matches {
		if match[1] > match[0] {
			matches[n] = match
			n++
		}
	}
	return matches[:n]
}

// nearestMatch returns the index of the first match at or below the given
// line, wrapping around to the first match.
func nearestMatch(matchLines []int, line int) int {
	for i, l := range matchLines {
		if l >= line {
			return i
		}
	}
	return 0
}

// matchFrom returns the index of the first match at or after byte offset off,
// wrapping around to the first match.
func matchFrom(matches [][]int, off int) int {
	return sort.Search(len(matches), func(i int) bool {
		return matches[i][0] >= off
	}) % len(matches)
}

// shiftOffset returns where the text at byte offset off of before is in after.
// Lines are the unit of change: an offset in a line that was edited moves to
// the start of that line.
func shiftOffset(before, after string, off int) int {
	n := min(len(before), len(after))
	prefix := 0
	for prefix < n && before[prefix] == after[prefix] {
		prefix++
	}
	if !lineEndsAt(before, prefix) || !lineEndsAt(after, prefix) {
		prefix = strings.LastIndexByte(before[:prefix], '\n') + 1
	}
	if off < prefix {
		return off
	}
	suffix := 0
	for suffix < n-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	for suffix > 0 && (!lineStartsAt(before, len(before)-suffix) || !lineStartsAt(after, len(after)-suffix)) {
		suffix--
	}
	if suffix > 0 && off >= len(before)-suffix {
		return off + len(after) - len(before)
	}
	return prefix
}

// lineEndsAt reports whether a line of s ends at byte i.
func lineEndsAt(s string, i int) bool {
	return i == len(s) || s[i] == '\n'
}

// lineStartsAt reports whether a line of s starts at byte i.
func lineStartsAt(s string, i int) bool {
	return i == 0 || s[i-1] == '\n'
}

// highlightLines styles the matches on rows, the viewport's view of the
// content lines from first on. Rows map to lines one to one, as glow sets
// neither SoftWrap nor a gutter. We don't use the viewport's highlighting, as
// it misplaces highlights in content containing ANSI sequences.
func (m pagerModel) highlightLines(rows []string, first int) []string {
	if len(m.matches) == 0 {
		return rows
	}

	left, width := m.viewport.XOffset(), m.viewport.Width()
	for i := range rows {
		n := first + i
		if n >= len(m.lines) {
			break
		}
		start, end := m.lineStarts[n], len(m.stripped)
		if n+1 < len(m.lineStarts) {
			end = m.lineStarts[n+1] - 1
		}

		if strings.Contains(m.lines[n], osc66Prefix) {
			// Sized text is zero-width to the viewport, which cuts around it,
			// so highlight the whole line and cut like the viewport does.
			ranges, sel := m.lineRanges(start, start, end)
			rows[i] = xansi.Cut(highlightSizedLine(m.lines[n], ranges, sel), left, left+width)
			continue
		}

		// StyleRanges takes columns of the row, which starts at from
		line := m.stripped[start:end]
		from, to := visibleWindow(line, left, width)
		ranges, _ := m.lineRanges(start, start+from, start+to)
		pos, col := from, 0
		for k, r := range ranges {
			col += xansi.StringWidth(line[pos:r.Start])
			ranges[k].Start = col
			col += xansi.StringWidth(line[r.Start:r.End])
			ranges[k].End = col
			pos = r.End
		}
		rows[i] = lipgloss.StyleRanges(rows[i], ranges...)
	}

	return rows
}

// lineRanges returns the parts of the matches within [from, to), byte offsets
// into the stripped text, relative to start, the offset of their line, and
// the index of the selected match among them, or -1.
func (m pagerModel) lineRanges(start, from, to int) ([]lipgloss.Range, int) {
	var ranges []lipgloss.Range
	sel := -1
	j := sort.Search(len(m.matches), func(i int) bool { return m.matches[i][1] > from })
	for ; j < len(m.matches) && m.matches[j][0] < to; j++ {
		lo, hi := max(m.matches[j][0], from), min(m.matches[j][1], to)
		if lo >= hi {
			continue
		}
		st := m.common.styles.searchMatchStyle
		if j == m.matchIndex {
			st = m.common.styles.searchSelectedMatchStyle
			sel = len(ranges)
		}
		ranges = append(ranges, lipgloss.NewRange(lo-start, hi-start, st))
	}
	return ranges, sel
}

// visibleWindow returns the bytes of line, a line of stripped text, that the
// viewport shows at horizontal offset left with the given width. The edges
// follow ansi.Cut, which keeps the character the left edge falls in and drops
// the one the right edge falls in.
func visibleWindow(line string, left, width int) (from, to int) {
	from = len(line) - len(xansi.TruncateLeft(line, left, ""))
	col := xansi.StringWidth(line[:from])
	for to = from; to < len(line); {
		cluster, w := xansi.FirstGraphemeCluster(line[to:], xansi.GraphemeWidth)
		if col+w > left+width {
			break
		}
		to += len(cluster)
		col += w
	}
	return from, to
}

func (m pagerModel) View() string {
	var b strings.Builder
	lines := m.highlightLines(strings.Split(m.viewport.View(), "\n"), m.viewport.YOffset())
	fmt.Fprint(&b, strings.Join(lines, "\n")+"\n")

	// Footer
	m.statusBarView(&b)

	if m.showHelp {
		fmt.Fprint(&b, "\n"+m.helpView())
	}

	return b.String()
}

func (m pagerModel) statusBarView(b *strings.Builder) {
	const (
		minPercent               float64 = 0.0
		maxPercent               float64 = 1.0
		percentToStringMagnitude float64 = 100.0
	)

	showStatusMessage := m.state == pagerStateStatusMessage
	styles := m.common.styles

	// Logo
	logo := glowLogoView(m.common.styles)

	// Scroll percent
	// The search prompt replaces the status bar
	if m.searching {
		prompt := m.searchInput.View()
		fmt.Fprint(b, prompt+strings.Repeat(" ", max(0, m.common.width-ansi.PrintableRuneWidth(prompt))))
		return
	}

	percent := math.Max(minPercent, math.Min(maxPercent, m.viewport.ScrollPercent()))
	scrollPercent := fmt.Sprintf(" %3.f%% ", percent*percentToStringMagnitude)
	if len(m.matches) > 0 {
		scrollPercent = fmt.Sprintf(" [%d/%d]", m.matchIndex+1, len(m.matches)) + scrollPercent
	}
	if showStatusMessage {
		scrollPercent = styles.statusBarMessageScrollPosStyle(scrollPercent)
	} else {
		scrollPercent = styles.statusBarScrollPosStyle(scrollPercent)
	}

	// "Help" note
	var helpNote string
	if showStatusMessage {
		helpNote = styles.statusBarMessageHelpStyle(" ? Help ")
	} else {
		helpNote = styles.statusBarHelpStyle(" ? Help ")
	}

	// Note
	var note string
	if showStatusMessage {
		note = m.statusMessage
	} else {
		note = m.currentDocument.Note
	}
	note = truncate.StringWithTail(" "+note+" ", uint(max(0, //nolint:gosec
		m.common.width-
			ansi.PrintableRuneWidth(logo)-
			ansi.PrintableRuneWidth(scrollPercent)-
			ansi.PrintableRuneWidth(helpNote),
	)), ellipsis)
	if showStatusMessage {
		note = styles.statusBarMessageStyle(note)
	} else {
		note = styles.statusBarNoteStyle(note)
	}

	// Empty space
	padding := max(0,
		m.common.width-
			ansi.PrintableRuneWidth(logo)-
			ansi.PrintableRuneWidth(note)-
			ansi.PrintableRuneWidth(scrollPercent)-
			ansi.PrintableRuneWidth(helpNote),
	)
	emptySpace := strings.Repeat(" ", padding)
	if showStatusMessage {
		emptySpace = styles.statusBarMessageStyle(emptySpace)
	} else {
		emptySpace = styles.statusBarNoteStyle(emptySpace)
	}

	fmt.Fprintf(b, "%s%s%s%s%s",
		logo,
		note,
		emptySpace,
		scrollPercent,
		helpNote,
	)
}

func (m pagerModel) helpView() (s string) {
	col1 := []string{
		"g/home  go to top",
		"G/end   go to bottom",
		"c       copy contents",
		"e       edit this document",
		"r       reload this document",
		"esc     back to files",
		"q       quit",
	}

	s += "\n"
	s += "k/↑      up                  " + col1[0] + "\n"
	s += "j/↓      down                " + col1[1] + "\n"
	s += "b/pgup   page up             " + col1[2] + "\n"
	s += "f/pgdn   page down           " + col1[3] + "\n"
	s += "u        ½ page up           " + col1[4] + "\n"
	s += "d        ½ page down         " + col1[5] + "\n"
	s += "/        search              " + col1[6] + "\n"
	s += "n/N      next/prev match     "

	s = indent(s, 2)

	// Fill up empty cells with spaces for background coloring
	if m.common.width > 0 {
		lines := strings.Split(s, "\n")
		for i := 0; i < len(lines); i++ {
			l := runewidth.StringWidth(lines[i])
			n := max(m.common.width-l, 0)
			lines[i] += strings.Repeat(" ", n)
		}

		s = strings.Join(lines, "\n")
	}

	return m.common.styles.helpViewStyle(s)
}

// COMMANDS

func renderWithGlamour(m pagerModel, md string) tea.Cmd {
	return func() tea.Msg {
		s, err := glamourRender(m, md)
		if err != nil {
			log.Error("error rendering with Glamour", "error", err)
			return errMsg{err}
		}
		return contentRenderedMsg(s)
	}
}

// This is where the magic happens.
func glamourRender(m pagerModel, markdown string) (string, error) {
	trunc := lipgloss.NewStyle().MaxWidth(m.viewport.Width() - lineNumberWidth).Render

	if !m.common.cfg.GlamourEnabled {
		return markdown, nil
	}

	isCode := !utils.IsMarkdownFile(m.currentDocument.Note)
	width := max(0, min(int(m.common.cfg.GlamourMaxWidth), m.viewport.Width())) //nolint:gosec
	if isCode {
		width = 0
	}

	options := []glamour.TermRendererOption{
		utils.GlamourStyle(m.common.cfg.GlamourStyle, isCode),
		glamour.WithWordWrap(width),
	}

	if m.common.cfg.PreserveNewLines {
		options = append(options, glamour.WithPreservedNewLines())
	}
	r, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return "", fmt.Errorf("error creating glamour renderer: %w", err)
	}

	if isCode {
		markdown = utils.WrapCodeBlock(markdown, filepath.Ext(m.currentDocument.Note))
	}

	out, err := r.Render(markdown)
	if err != nil {
		return "", fmt.Errorf("error rendering markdown: %w", err)
	}

	if isCode {
		out = strings.TrimSpace(out)
	} else {
		out = utils.ApplyTextSizing(out)
	}

	// trim lines
	lines := strings.Split(out, "\n")

	var content strings.Builder
	for i, s := range lines {
		if isCode || m.common.cfg.ShowLineNumbers {
			content.WriteString(m.common.styles.lineNumberStyle(fmt.Sprintf("%"+fmt.Sprint(lineNumberWidth)+"d", i+1)))
			content.WriteString(trunc(s))
		} else {
			content.WriteString(s)
		}

		// don't add an artificial newline after the last split
		if i+1 < len(lines) {
			content.WriteRune('\n')
		}
	}

	return content.String(), nil
}

func (m *pagerModel) initWatcher() {
	var err error
	m.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		log.Error("error creating fsnotify watcher", "error", err)
	}
}

func (m *pagerModel) watchFile() tea.Msg {
	dir := m.localDir()

	if err := m.watcher.Add(dir); err != nil {
		log.Error("error adding dir to fsnotify watcher", "error", err)
		return nil
	}

	log.Info("fsnotify watching dir", "dir", dir)

	for {
		select {
		case event, ok := <-m.watcher.Events:
			if !ok || event.Name != m.currentDocument.localPath {
				continue
			}

			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			log.Debug("fsnotify event", "file", event.Name, "event", event.Op)
			return reloadMsg{}
		case err, ok := <-m.watcher.Errors:
			if !ok {
				continue
			}
			log.Debug("fsnotify error", "dir", dir, "error", err)
		}
	}
}

func (m *pagerModel) unwatchFile() {
	dir := m.localDir()

	err := m.watcher.Remove(dir)
	if err == nil {
		log.Debug("fsnotify dir unwatched", "dir", dir)
	} else {
		log.Error("fsnotify fail to unwatch dir", "dir", dir, "error", err)
	}
}

func (m *pagerModel) localDir() string {
	return filepath.Dir(m.currentDocument.localPath)
}

// highlightSizedLine styles the given ranges, byte offsets into the
// searchText of line, in a line containing OSC 66 text sizing sequences.
// lipgloss.StyleRanges would move those sequences around, so we style the text
// ourselves, splitting sized text into separate sequences as needed.
// Fractionally scaled text is styled a whole sequence at a time, as each of
// its sequences carries its own width, so a highlight may cover up to a few
// cells around the match. Grapheme clusters are styled whole too, as a split
// one isn't drawn as one glyph.
func highlightSizedLine(line string, ranges []lipgloss.Range, selected int) string {
	var b strings.Builder
	var sgr strings.Builder // SGR sequences in effect, restored after highlights
	pos := 0                // byte offset into the line's searchText

	emit := func(s string, st lipgloss.Style, styled bool) {
		if styled {
			s = st.Render(s) + sgr.String()
		}
		b.WriteString(s)
	}

	// write writes text found at pos, styling the parts within ranges. wrap
	// turns each part back into its raw form.
	write := func(text string, whole bool, wrap func(string) string) {
		snapToClusters(text, pos, ranges)
		for len(text) > 0 {
			n, st, styled := len(text), lipgloss.Style{}, false
			for k, r := range ranges {
				switch {
				case whole && r.Start < pos+len(text) && r.End > pos:
					if !styled || k == selected {
						st, styled = r.Style, true
					}
				case whole:
				case r.Start <= pos && pos < r.End:
					n = min(n, r.End-pos)
					if !styled || k == selected {
						st, styled = r.Style, true
					}
				case r.Start > pos:
					n = min(n, r.Start-pos)
				}
			}
			emit(wrap(text[:n]), st, styled)
			text = text[n:]
			pos += n
		}
	}

	for len(line) > 0 {
		i := strings.IndexByte(line, '\x1b')
		switch {
		case i < 0:
			i = len(line)
			fallthrough
		case i > 0:
			write(line[:i], false, func(s string) string { return s })
			line = line[i:]
		case strings.HasPrefix(line, osc66Prefix):
			meta, text, n := parseOSC66(line)
			write(text, strings.Contains(meta, "w="), func(s string) string {
				return osc66Prefix + meta + ";" + s + "\x1b\\"
			})
			line = line[n:]
		default:
			seq, _, n, _ := xansi.DecodeSequence(line, xansi.NormalState, nil)
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				if seq == "\x1b[m" || seq == "\x1b[0m" {
					sgr.Reset()
				} else {
					sgr.WriteString(seq)
				}
			}
			b.WriteString(line[:n])
			line = line[n:]
		}
	}

	return b.String()
}

// snapToClusters widens the ranges that start or end inside a grapheme cluster
// of text, whose first byte is at pos, to the cluster's bounds.
func snapToClusters(text string, pos int, ranges []lipgloss.Range) {
	start, state := pos, -1
	for rest := text; rest != ""; {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		end := start + len(cluster)
		for k, r := range ranges {
			if r.Start > start && r.Start < end {
				ranges[k].Start = start
			}
			if r.End > start && r.End < end {
				ranges[k].End = end
			}
		}
		start = end
	}
}

// searchText strips ANSI sequences from s like ansi.Strip, but keeps the text
// of OSC 66 text sizing sequences, used for headings, so it can be searched.
func searchText(s string) string {
	if !strings.Contains(s, osc66Prefix) {
		return xansi.Strip(s)
	}

	var b strings.Builder
	for len(s) > 0 {
		i := strings.Index(s, osc66Prefix)
		if i < 0 {
			b.WriteString(xansi.Strip(s))
			break
		}
		b.WriteString(xansi.Strip(s[:i]))
		_, text, n := parseOSC66(s[i:])
		b.WriteString(text)
		s = s[i+n:]
	}
	return b.String()
}

// parseOSC66 parses the OSC 66 sequence at the start of s, returning its
// metadata, its text, and its length in bytes.
func parseOSC66(s string) (meta, text string, n int) {
	body := s[len(osc66Prefix):]
	end, n := len(body), len(s)
	if i := strings.IndexAny(body, "\a\x1b"); i >= 0 {
		// Terminated by BEL or ST (ESC \). A lone ESC ends the sequence
		// without being consumed, as it starts the next one.
		end, n = i, len(osc66Prefix)+i+1
		if body[i] == '\x1b' {
			if i+1 < len(body) && body[i+1] == '\\' {
				n++
			} else {
				n--
			}
		}
	}
	meta, text, _ = strings.Cut(body[:end], ";")
	return meta, text, n
}
