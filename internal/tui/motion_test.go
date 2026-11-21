package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agent-view/internal/cards"
)

func TestWheelMovesViewportIndependently(t *testing.T) {
	f := newModelFix(t, true, 120, 36)
	for i := 0; i < 14; i++ {
		id := fmt.Sprintf("agent-%02d", i)
		f.batch(agentEnv(id, "", f.clock), startEnv(id, "call-"+id, "read", "/repo/source.go", f.clock))
	}
	f.press("g")
	f.press("enter")
	position := func() int { return f.m.ui.Top }
	wheel := func(button tea.MouseButton, y int) { f.send(tea.MouseWheelMsg{Button: button, X: 8, Y: y}) }

	wheel(tea.MouseWheelDown, boardTop+1)
	if position() != 3 {
		t.Fatalf("top after one notch = %d", position())
	}
	f.send(clockMsg{})
	f.batch(agentEnv("new", "", f.clock))
	if position() != 3 || f.m.ui.Focus != dk("agent-00") || f.m.ui.Selected != f.m.ui.Focus {
		t.Fatal("refresh changed scroll, focus, or selection")
	}
	originalLog := strings.Join(f.lines()[36-logH-1:], "\n")
	wheel(tea.MouseWheelDown, 36-logH-1)
	wheel(tea.MouseWheelDown, 0)
	if position() != 3 {
		t.Fatal("scroll outside board moved it")
	}
	for i := 0; i < 60; i++ {
		wheel(tea.MouseWheelDown, 4)
	}
	bottom := position()
	wheel(tea.MouseWheelDown, 4)
	if bottom <= 3 || position() != bottom {
		t.Fatal("bottom clamp")
	}
	if strings.Join(f.lines()[36-logH-1:], "\n") != originalLog {
		t.Fatal("wheel changed pinned log")
	}
	f.press("?")
	wheel(tea.MouseWheelUp, 4)
	if position() != bottom {
		t.Fatal("scrolled behind help")
	}
	f.press("esc")
	f.press("space")
	wheel(tea.MouseWheelUp, 4)
	if position() != bottom-3 {
		t.Fatal("wheel while paused")
	}
	f.press("g")
	if position() != 0 {
		t.Fatal("keyboard did not restore selection tracking")
	}
	wheel(tea.MouseWheelUp, 4)
	if position() != 0 {
		t.Fatal("top clamp")
	}
}

func TestMotionSnapsAndStops(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	if strings.Contains(f.lines()[0], "AGENT VIEW") {
		t.Fatal("expected launch scramble")
	}
	for range 19 {
		f.send(motionMsg{})
	}
	if !strings.Contains(f.lines()[0], "AGENT VIEW") || f.m.motionOn {
		t.Fatal("title did not settle")
	}
	f.batch(agentEnv("a", "", f.clock))
	f.frames(20)
	f.press("enter")
	f.frames(12) // Expand the previously collapsed card with the board animation clock.
	h := hitFor(t, f.m.hits, "a")
	sprite := func() []string {
		var rows []string
		for _, line := range f.lines()[h.Y+1 : h.Y+5] {
			rows = append(rows, string([]rune(line)[h.X+2:h.X+10]))
		}
		return rows
	}
	want := cards.Sprite("a")
	if slices.Equal(sprite(), want[:]) {
		t.Fatal("focused avatar did not scramble")
	}
	for range 10 {
		f.send(motionMsg{})
	}
	if !slices.Equal(sprite(), want[:]) || f.m.motionOn {
		t.Fatal("avatar did not snap back")
	}
	f.press("r")
	if !f.m.motionOn {
		t.Fatal("replay did not start")
	}
	f.press("space")
	f.send(motionMsg{})
	if f.m.motionOn || f.m.motion.title != 0 {
		t.Fatal("pause did not stop motion")
	}
}

func TestBackgroundProtectsCardsAndLog(t *testing.T) {
	f := newFixture(t)
	f.agent("a", "")
	in := f.input(120, 40)
	in.UI.Selected = dk("a")
	base, hits := Compose(in)
	if len(hits) == 0 {
		t.Fatal("no card")
	}
	for bg := glyphRain; bg < backgroundCount; bg++ {
		in.motion = motionState{background: bg, phase: 7}
		frame, _ := Compose(in)
		h := hits[0]
		for y := h.Y; y < h.Y+h.H; y++ {
			i := y*frame.W + h.X
			if !slices.Equal(base.C[i:i+h.W], frame.C[i:i+h.W]) {
				t.Fatal("background entered card")
			}
		}
		logStart := (frame.H - logH - 1) * frame.W
		if !slices.Equal(base.C[logStart:(frame.H-1)*frame.W], frame.C[logStart:(frame.H-1)*frame.W]) {
			t.Fatal("background entered log")
		}
		in.motion.phase++
		next, _ := Compose(in)
		if slices.Equal(frame.C, next.C) {
			t.Fatalf("%s did not move", bg)
		}
	}
	fix := newModelFix(t, true, 120, 40)
	for _, key := range []string{"b", "r", "enter"} {
		fix.press(key)
	}
	if fix.m.motionOn || fix.m.motion != (motionState{}) {
		t.Fatal("effects enabled with reduced motion")
	}
}
