package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type choice struct {
	value     string
	label     string
	isChannel bool
}

type modelChoices struct {
	focusIndex int
	choices    []choice
	selected   map[int]interface{}
}

const (
	downloadAvatarsIndex = 4
	downloadFilesIndex   = 5
	includeArchivedIndex = 6
	skipDownloadedIndex  = 7
)

func initialModelChoices(downloadAvatars, downloadFiles, includeArchived, skipDownloaded bool) modelChoices {
	selected := make(map[int]interface{})
	if downloadAvatars {
		selected[downloadAvatarsIndex] = nil
	}
	if downloadFiles {
		selected[downloadFilesIndex] = nil
	}
	if includeArchived {
		selected[includeArchivedIndex] = nil
	}
	if skipDownloaded {
		selected[skipDownloadedIndex] = nil
	}

	return modelChoices{
		choices: []choice{
			// {"public_channel", "Public channels", true},
			{"private_channel", "Private channels", true},
			{"im", "DM", true},
			{"mpim", "Group DM", true},
			{"downloadAvatars", "Download avatars", false},
			{"downloadFiles", "Download files", false},
			{"includeArchived", "Include archived channels", false},
			{"skipDownloaded", "Skip downloaded", false},
		},
		selected: selected,
	}
}

func (mc modelChoices) Init() tea.Cmd {
	return tea.SetWindowTitle("Select channels to export")
}

func (mc modelChoices) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			for i := range mc.selected {
				delete(mc.selected, i)
			}
			return mc, tea.Quit
		case "tab", "shift+tab", "enter", "up", "down":
			s := msg.String()

			// Did the user press enter while the submit button was focused?
			// If so, exit.
			if s == "enter" && mc.focusIndex == len(mc.choices) {
				return mc, tea.Quit
			}

			// Cycle indexes
			if s == "up" || s == "shift+tab" {
				mc.focusIndex--
			} else {
				mc.focusIndex++
			}

			if mc.focusIndex > len(mc.choices) {
				mc.focusIndex = 0
			} else if mc.focusIndex < 0 {
				mc.focusIndex = len(mc.choices)
			}

		case " ":
			_, ok := mc.selected[mc.focusIndex]
			if ok {
				delete(mc.selected, mc.focusIndex)
			} else {
				mc.selected[mc.focusIndex] = struct{}{}
			}
		}
	}

	return mc, nil
}

func (mc modelChoices) View() string {
	var b strings.Builder
	b.WriteString("What channels do you want to export?\n\n")

	for i, choice := range mc.choices {
		focusIndex := " "
		if mc.focusIndex == i {
			focusIndex = "▶︎"
		}

		checked := " "
		if _, ok := mc.selected[i]; ok {
			checked = "×"
		}

		if choice.value == "downloadAvatars" {
			b.WriteRune('\n')
		}

		style := noStyle
		if mc.focusIndex == i {
			style = focusedStyle
		}

		b.WriteString(style.Render(fmt.Sprintf("%s [%s] %s", focusIndex, checked, choice.label)) + "\n")
	}

	button := &blurredButton
	if mc.focusIndex == len(mc.choices) {
		button = &focusedButton
	}
	fmt.Fprintf(&b, "\n%s\n\n", *button)
	fmt.Fprintf(&b, "Use arrow keys ↑ and ↓ to move between inputs. Press enter to press the submit button.\n")

	return b.String()
}
