package tui

import (
	"fmt"
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
// panel did. The golden was recorded from that panel before the change; do
// not regenerate it. No host line, and no "more lines" line when the box is
// too short.
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

// TestCoveragePanelTruncates: content taller than the box ends with a line
// that counts the lines left out, and a box that fits shows no such line.
func TestCoveragePanelTruncates(t *testing.T) {
	covs := hostCovs("h1", "h2", "h3", "h4", "h5")
	diag := numbered("diag", 3)
	full := contentLines(covs, diag, 72)
	total := len(full) // 5 blocks of 10 lines, 4 blank lines, a blank line, "diagnostics", 3 lines
	if want := 5*10 + 4 + 1 + 1 + 3; total != want {
		t.Fatalf("content has %d lines, want %d", total, want)
	}

	// The box of the brief: 5 hosts in 12 lines.
	lines := panelLines(CoveragePanel(covs, diag, 72, 12))
	if len(lines) != 10 {
		t.Fatalf("%d inner lines, want 10", len(lines))
	}
	if want := fmt.Sprintf("… %d more lines", total-9); lines[9] != want {
		t.Errorf("last line %q, want %q", lines[9], want)
	}
	if strings.Join(lines[:9], "\n") != strings.Join(full[:9], "\n") {
		t.Errorf("the lines above the last differ from the content:\n%s", strings.Join(lines, "\n"))
	}

	// The edge: a box as tall as the content, one line short, one tall.
	for _, c := range []struct {
		inner int
		last  string // "" for the last content line
	}{{total, ""}, {total - 1, "… 2 more lines"}, {total + 1, ""}} {
		lines := panelLines(CoveragePanel(covs, diag, 72, c.inner+2))
		if len(lines) != c.inner {
			t.Fatalf("%d inner lines, want %d", len(lines), c.inner)
		}
		if c.last == "" {
			if strings.Join(lines[:total], "\n") != strings.Join(full, "\n") || strings.Contains(strings.Join(lines, "\n"), "more lines") {
				t.Errorf("inner %d: the content does not fit as is:\n%s", c.inner, strings.Join(lines, "\n"))
			}
			continue
		}
		if lines[len(lines)-1] != c.last || lines[len(lines)-2] != full[total-3] {
			t.Errorf("inner %d: ends %q, %q; want %q after %q", c.inner, lines[len(lines)-2], lines[len(lines)-1], c.last, full[total-3])
		}
	}

	// Diagnostics are part of the content: they are cut after the blocks.
	covs = hostCovs("h1", "h2")
	diag = numbered("diag", 9)
	full = contentLines(covs, diag, 72)
	lines = panelLines(CoveragePanel(covs, diag, 72, 28))
	if want := fmt.Sprintf("… %d more lines", len(full)-25); lines[len(lines)-1] != want {
		t.Errorf("last line %q, want %q", lines[len(lines)-1], want)
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
