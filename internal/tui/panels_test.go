package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
)

// sampleCoverage returns a coverage that fills every line of the panel; tag
// makes the paths differ from host to host.
func sampleCoverage(tag string) schema.Coverage {
	return schema.Coverage{
		LogRoots:         []string{"/home/" + tag + "/.claude/projects", "/home/" + tag + "/.codex/sessions"},
		LogsOpen:         7,
		LogsDiscovered:   12,
		WatchRoots:       []string{"/home/" + tag + "/work"},
		MalformedRecords: 3,
		FSDropped:        1,
		Sources: map[string]schema.SourceState{
			"claude": {State: "ok"},
			"codex":  {State: "degraded", Detail: "no\x1b[31m logs"},
			"cursor": {State: "down", Detail: "not installed"},
		},
	}
}

// panelCase is one coverage panel of a golden.
type panelCase struct {
	name string
	covs []HostCoverage
	diag []string
	w, h int
}

// oneHost returns the coverage of a single host.
func oneHost(label string, c schema.Coverage) []HostCoverage { return []HostCoverage{{label, c}} }

// oneHostCases are the panels a single host draws: with and without
// diagnostics, too short for the whole content, and with no coverage.
func oneHostCases() []panelCase {
	diag := []string{"exec: bun: not found", "devbox collector down: exit status 1", "store: dropped 2 envelope(s)"}
	return []panelCase{
		{"diagnostics", oneHost("devbox", sampleCoverage("u")), diag, 72, 24},
		{"no diagnostics", oneHost("devbox", sampleCoverage("u")), nil, 72, 24},
		{"short box", oneHost("devbox", sampleCoverage("u")), diag, 72, 12},
		{"many diagnostics", oneHost("devbox", sampleCoverage("u")), numbered("diag", 9), 72, 20},
		{"no coverage", nil, diag, 72, 24},
		{"narrow", oneHost("devbox", sampleCoverage("u")), diag, 40, 24},
	}
}

// numbered returns n lines "<prefix> 1" … "<prefix> n".
func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", prefix, i+1)
	}
	return out
}

// caseGolden joins the panels of cases into one golden text.
func caseGolden(cases []panelCase, draw func(panelCase) string) string {
	var b strings.Builder
	for _, c := range cases {
		b.WriteString("=== " + c.name + " ===\n")
		b.WriteString(draw(c))
		b.WriteString("\n")
	}
	return b.String()
}

// panelGolden draws each case and returns the golden text: the plain text of
// the panel, then its style mask.
func panelGolden(cases []panelCase) string {
	return caseGolden(cases, func(c panelCase) string {
		g := CoveragePanel(c.covs, c.diag, c.w, c.h)
		return cells.Plain(g) + "\n" + cells.Mask(g)
	})
}

// panelLines returns the lines inside the border of a panel, without the
// border and without trailing spaces.
func panelLines(g *cells.Grid) []string {
	lines := plainLines(g)
	var out []string
	for _, l := range lines[1 : len(lines)-1] {
		r := []rune(l)
		out = append(out, strings.TrimRight(string(r[2:len(r)-2]), " "))
	}
	return out
}

// contentLines returns the lines of a panel that is tall enough for all its
// content, without the blank lines at the end.
func contentLines(covs []HostCoverage, diag []string, w int) []string {
	lines := panelLines(CoveragePanel(covs, diag, w, 400))
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hostCovs(labels ...string) []HostCoverage {
	var out []HostCoverage
	for _, l := range labels {
		out = append(out, HostCoverage{l, sampleCoverage(l)})
	}
	return out
}

// TestCoveragePanelOneHostUnchanged: a single host draws as the first-host
// panel did, with no host line. The cases are the golden recorded from that
// panel, except "short box": the panel now cuts the coverage with a "more
// lines" line to keep the diagnostics, so that case is recorded from the
// panel of P3c. Do not regenerate the others.
func TestCoveragePanelOneHostUnchanged(t *testing.T) {
	checkGolden(t, "coverage_one_host", panelGolden(oneHostCases()))
}

// twoHostCases are the panels of two hosts. The box is as tall as the
// content in the first two, shorter in the last, and the second host has no
// coverage in the third (its label has an escape sequence).
func twoHostCases() []panelCase {
	diag := []string{"exec: bun: not found", "box#2 collector down: exit status 1", "store: dropped 2 envelope(s)"}
	two := []HostCoverage{{"box", sampleCoverage("a")}, {"box#2", sampleCoverage("b")}}
	partial := []HostCoverage{{"box", sampleCoverage("a")}, {"dev\x1b[31mbox", schema.Coverage{}}}
	return []panelCase{
		{"diagnostics", two, diag, 72, 28},
		{"no diagnostics", two, nil, 72, 28},
		{"second host without coverage", partial, diag, 72, 28},
		{"short box", two, diag, 72, 14},
	}
}

func TestCoveragePanelTwoHosts(t *testing.T) {
	checkGolden(t, "coverage_two_hosts", panelGolden(twoHostCases()))
}

// joined joins lines for a failure message and for an exact comparison.
func joined(lines []string) string { return strings.Join(lines, "\n") }

// trimEnd returns lines without the blank lines at the end.
func trimEnd(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TestCoveragePanelKeepsNewestFourDiagnostics: the panel of the 80 × 24 box
// (20 × 18 inside) with five hosts and nine diagnostics reserves the section
// of the diagnostics. It cuts hosts, not diagnostics: the first block is whole,
// the marker counts the four hosts left out, and the newest diagnostics end the
// box. The second set of hosts has blocks so short that two of them would fit
// in the box with the marker, but only if the reserve did not exist.
func TestCoveragePanelKeepsNewestFourDiagnostics(t *testing.T) {
	var bare []HostCoverage // hosts that sent no coverage: blocks of 7 lines
	for _, l := range []string{"h1", "h2", "h3", "h4", "h5"} {
		bare = append(bare, HostCoverage{Label: l})
	}
	diag := numbered("diag", 9)
	for _, c := range []struct {
		name  string
		covs  []HostCoverage
		block int // the lines of the first block
	}{
		{"hosts with coverage", hostCovs("h1", "h2", "h3", "h4", "h5"), 10},
		{"hosts without coverage", bare, 7},
	} {
		full := contentLines(c.covs, diag, 72)
		lines := panelLines(CoveragePanel(c.covs, diag, 72, 20))
		if len(lines) != 18 {
			t.Fatalf("%s: %d inner lines, want 18", c.name, len(lines))
		}
		if joined(lines[:c.block]) != joined(full[:c.block]) {
			t.Errorf("%s: the first block is not whole:\n%s", c.name, joined(lines))
		}
		if lines[c.block] != "… 4 more hosts" {
			t.Errorf("%s: line after the first block is %q, want %q:\n%s", c.name, lines[c.block], "… 4 more hosts", joined(lines))
		}
		if lines[c.block+1] != "" || lines[c.block+2] != "diagnostics" {
			t.Errorf("%s: the section does not follow the marker: %q, %q", c.name, lines[c.block+1], lines[c.block+2])
		}
		at := -1
		for _, want := range []string{"diag 6", "diag 7", "diag 8", "diag 9"} {
			i := slices.Index(lines, want)
			if i <= at {
				t.Fatalf("%s: %q at line %d, after the line %d; want the newest four diagnostics in order:\n%s", c.name, want, i, at, joined(lines))
			}
			at = i
		}
		if lines[17] != "diag 9" {
			t.Errorf("%s: last line %q, want %q", c.name, lines[17], "diag 9")
		}
	}
}

// TestCoveragePanelOneHostCutsLinesNotDiagnostics: one host with a tall
// coverage and a short box. The coverage ends with a line that counts the
// lines left out, and the diagnostics still show below it.
func TestCoveragePanelOneHostCutsLinesNotDiagnostics(t *testing.T) {
	cov := sampleCoverage("a")
	cov.LogRoots = nil
	for i := 0; i < 30; i++ {
		cov.LogRoots = append(cov.LogRoots, fmt.Sprintf("/home/a/root%d", i))
	}
	covs := oneHost("devbox", cov)
	diag := numbered("diag", 3)
	total := len(coverageLines(cov))
	if total != 30+1+1+1+1+3 {
		t.Fatalf("the coverage has %d lines, want 37", total)
	}
	full := contentLines(covs, diag, 72)

	// h 12: 10 lines inside, 5 for the diagnostics, 5 for the coverage: its
	// first 4 lines and the marker.
	lines := panelLines(CoveragePanel(covs, diag, 72, 12))
	if len(lines) != 10 {
		t.Fatalf("%d inner lines, want 10", len(lines))
	}
	if joined(lines[:4]) != joined(full[:4]) {
		t.Errorf("the lines above the marker differ from the content:\n%s", joined(lines))
	}
	if want := fmt.Sprintf("… %d more lines", total-4); lines[4] != want {
		t.Errorf("marker %q, want %q", lines[4], want)
	}
	want := append([]string{"", "diagnostics"}, diag...)
	if joined(lines[5:]) != joined(want) {
		t.Errorf("the diagnostics are %q, want %q", lines[5:], want)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "host ") {
			t.Errorf("a single host has a host line: %q", l)
		}
	}
}

// TestCoveragePanelOneMoreHostIsSingular: with two hosts of which only one
// block fits, the marker is "… 1 more host".
func TestCoveragePanelOneMoreHostIsSingular(t *testing.T) {
	covs := hostCovs("h1", "h2")
	diag := numbered("diag", 3)
	full := contentLines(covs, diag, 72)
	// 5 lines for the diagnostics, then one block of 10 lines and the marker.
	for _, inner := range []int{5 + 11, 5 + 20} {
		lines := panelLines(CoveragePanel(covs, diag, 72, inner+2))
		if joined(lines[:10]) != joined(full[:10]) {
			t.Errorf("inner %d: the first block is not whole:\n%s", inner, joined(lines))
		}
		if lines[10] != "… 1 more host" {
			t.Errorf("inner %d: line after the block is %q, want %q", inner, lines[10], "… 1 more host")
		}
		if strings.Contains(joined(lines), "hosts") {
			t.Errorf("inner %d: the marker is plural:\n%s", inner, joined(lines))
		}
	}
}

// TestCoveragePanelDownHostReasonShows: the reason a host is down is the
// newest diagnostic line, and it shows even when the box has room for one
// block only.
func TestCoveragePanelDownHostReasonShows(t *testing.T) {
	covs := hostCovs("h1", "h2")
	const reason = "ghost collector down: exit status 255"
	diag := append(numbered("diag", 5), reason)
	lines := panelLines(CoveragePanel(covs, diag, 72, 20))
	if !slices.Contains(lines, reason) {
		t.Errorf("the reason %q is cut:\n%s", reason, joined(lines))
	}
	if lines[len(lines)-1] != reason {
		t.Errorf("last line %q, want %q", lines[len(lines)-1], reason)
	}
}

// TestCoveragePanelTinyBox: a box with less room than the section of the
// diagnostics draws the last lines of that section and nothing else.
func TestCoveragePanelTinyBox(t *testing.T) {
	covs := hostCovs("h1", "h2")
	for _, c := range []struct {
		h    int
		diag []string
		want []string
	}{
		{6, numbered("diag", 6), []string{"diag 3", "diag 4", "diag 5", "diag 6"}},
		{5, numbered("diag", 6), []string{"diag 4", "diag 5", "diag 6"}},
		{4, nil, []string{"diagnostics", "none"}},
		{3, nil, []string{"none"}},
	} {
		for _, cv := range [][]HostCoverage{covs, oneHost("h1", sampleCoverage("h1")), nil} {
			lines := panelLines(CoveragePanel(cv, c.diag, 72, c.h))
			if joined(lines) != joined(c.want) {
				t.Errorf("h %d, %d hosts, %d diagnostics: lines %q, want %q", c.h, len(cv), len(c.diag), lines, c.want)
			}
		}
	}
}

// TestCoveragePanelNoRoomForHosts: when the box has the room of the
// diagnostics section and no more, the panel shows the section and no host
// marker, because the marker would take a line of the section.
func TestCoveragePanelNoRoomForHosts(t *testing.T) {
	diag := numbered("diag", 3)
	lines := panelLines(CoveragePanel(hostCovs("h1", "h2", "h3"), diag, 72, 5+2))
	want := append([]string{"", "diagnostics"}, diag...)
	if joined(lines) != joined(want) {
		t.Errorf("lines %q, want %q", lines, want)
	}
}

// TestCoveragePanelFitsUnchanged: a box as tall as the content, or taller,
// shows the content as is, with no marker. A box one line short cuts what the
// rule says: whole hosts, lines of a single coverage, or the oldest
// diagnostic, and the newest diagnostics stay.
func TestCoveragePanelFitsUnchanged(t *testing.T) {
	for _, c := range []struct {
		name  string
		covs  []HostCoverage
		diag  []string
		total int
		short func(full []string) []string // the content in a box one line short
	}{
		{"five hosts", hostCovs("h1", "h2", "h3", "h4", "h5"), numbered("diag", 3), 5*10 + 4 + 1 + 1 + 3,
			// four blocks and their blank lines, the marker, then the section
			func(f []string) []string { return append(append(slices.Clone(f[:43]), "… 1 more host"), f[54:]...) }},
		{"one host", hostCovs("h1"), numbered("diag", 3), 9 + 1 + 1 + 3,
			// 7 lines of the coverage and the marker, then the section
			func(f []string) []string { return append(append(slices.Clone(f[:7]), "… 2 more lines"), f[9:]...) }},
		{"two hosts, nine diagnostics", hostCovs("h1", "h2"), numbered("diag", 9), 2*10 + 1 + 1 + 1 + 9,
			// the oldest diagnostic goes, with no marker
			func(f []string) []string { return append(slices.Clone(f[:23]), f[24:]...) }},
	} {
		full := contentLines(c.covs, c.diag, 72)
		if len(full) != c.total {
			t.Fatalf("%s: content has %d lines, want %d", c.name, len(full), c.total)
		}
		for _, inner := range []int{c.total, c.total + 1} {
			lines := panelLines(CoveragePanel(c.covs, c.diag, 72, inner+2))
			if len(lines) != inner {
				t.Fatalf("%s: %d inner lines, want %d", c.name, len(lines), inner)
			}
			if joined(trimEnd(lines)) != joined(full) || strings.Contains(joined(lines), "more") {
				t.Errorf("%s: inner %d: the content does not fit as is:\n%s", c.name, inner, joined(lines))
			}
		}
		lines := panelLines(CoveragePanel(c.covs, c.diag, 72, c.total-1+2))
		if want := c.short(full); joined(trimEnd(lines)) != joined(want) {
			t.Errorf("%s: one line short:\n%s\nwant:\n%s", c.name, joined(lines), joined(want))
		}
	}
}

// TestCoveragePanelHostLine: with several hosts each block starts with the
// cleaned label in the label style; a single host has no such line.
func TestCoveragePanelHostLine(t *testing.T) {
	g := CoveragePanel([]HostCoverage{{"dev\x1b[31mbox", sampleCoverage("a")}, {"box#2", sampleCoverage("b")}}, nil, 72, 30)
	lines := panelLines(g)
	if lines[0] != "host devbox" {
		t.Errorf("first line %q, want %q", lines[0], "host devbox")
	}
	if strings.Contains(cells.Plain(g), "\x1b") {
		t.Errorf("the panel keeps an escape byte:\n%s", cells.Plain(g))
	}
	var second int
	for i, l := range lines {
		if l == "host box#2" {
			second = i
		}
	}
	if second == 0 || lines[second-1] != "" {
		t.Fatalf("no blank line before the second block (host line at %d):\n%s", second, strings.Join(lines, "\n"))
	}
	for _, y := range []int{1, 1 + second} {
		if c := g.C[y*g.W+2]; c.S.Role != cells.RDim {
			t.Errorf("host line %d has role %v, want the label role", y, c.S.Role)
		}
	}

	single := panelLines(CoveragePanel(oneHost("devbox", sampleCoverage("a")), nil, 72, 30))
	if strings.HasPrefix(single[0], "host") {
		t.Errorf("a single host has a host line: %q", single[0])
	}
}
