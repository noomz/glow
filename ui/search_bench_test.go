package ui

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkIncSearch(b *testing.B) {
	var md strings.Builder
	md.WriteString("# Doc\n\n")
	for i := range 16000 {
		fmt.Fprintf(&md, "line %d filler text jjj kkk ggg", i)
		if i%4 == 0 {
			md.WriteString(" needle here")
		}
		md.WriteString("\n\n")
	}
	common := &commonModel{
		cfg: Config{
			GlamourEnabled:  true,
			GlamourStyle:    "dark",
			GlamourMaxWidth: 80,
		},
		styles: newStyles(true),
		width:  100,
		height: 30,
	}
	m := newPagerModel(common)
	m.currentDocument = markdown{Note: "big.md"}
	m.setSize(100, 30)
	content, err := glamourRender(m, md.String())
	if err != nil {
		b.Fatal(err)
	}
	m.setContent(content)

	for _, pattern := range []string{".", "needle"} {
		b.Run(pattern, func(b *testing.B) {
			m.searchInput.SetValue(pattern)
			for b.Loop() {
				m.incSearch()
			}
			b.ReportMetric(float64(len(m.matches)), "matches")
		})
	}
}

// BenchmarkPagerView draws a frame of long lines, with every character
// matched and, as the floor, with no search. The highlighting cost must
// follow the visible area, not the line length.
func BenchmarkPagerView(b *testing.B) {
	for _, lineLen := range []int{2000, 10000} {
		for _, xOffset := range []int{0, 5000} {
			for _, pattern := range []string{".", ""} {
				b.Run(fmt.Sprintf("len=%d/x=%d/pattern=%q", lineLen, xOffset, pattern), func(b *testing.B) {
					common := &commonModel{styles: newStyles(true), width: 100, height: 41}
					m := newPagerModel(common)
					m.currentDocument = markdown{Note: "long.md"}
					m.setSize(100, 41)
					line := strings.Repeat("abcdefghij", lineLen/10)
					m.setContent(strings.Repeat(line+"\n", 40) + line)
					m.search(pattern)
					m.viewport.SetXOffset(xOffset)
					for b.Loop() {
						m.View()
					}
					b.ReportMetric(float64(m.viewport.XOffset()), "xoffset")
				})
			}
		}
	}
}
