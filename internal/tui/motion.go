package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type background uint8

const (
	backgroundOff background = iota
	glyphRain
	scanWave
	plasma
	fire
	raindrops
	backgroundCount
)

func (bg background) String() string {
	return [backgroundCount]string{"off", "glyph rain", "scan wave", "plasma", "fire", "raindrops"}[bg]
}

// A zero countdown shows the normal title or sprite.
type motionState struct {
	background           background
	phase, title, avatar int
}

type motionMsg struct{}

func (m *Model) motionTick() tea.Cmd {
	if m.motionOn || m.ui.Reduced || m.ui.Paused || m.w < MinW || m.h < MinH {
		return nil
	}
	delay := frameEvery
	if m.motion.title == 0 && m.motion.avatar == 0 {
		if m.motion.background == backgroundOff {
			return nil
		}
		delay = 75 * time.Millisecond
	}
	m.motionOn = true
	return tea.Tick(delay, func(time.Time) tea.Msg { return motionMsg{} })
}
