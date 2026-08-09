package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/config"
	"github.com/local-device-bridge/local-device-bridge/internal/security"
	"golang.org/x/term"
)

// menuOption is deliberately small so the setup wizard can stay readable in
// both a real terminal and a pipe/CI log.
type menuOption struct {
	Label       string
	Description string
}

type cliTheme struct {
	Enabled bool
}

func currentTheme() cliTheme {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return cliTheme{}
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return cliTheme{}
	}
	return cliTheme{Enabled: true}
}

func terminalWidth() int {
	width := 80
	if columns, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && columns >= 40 {
		width = columns
	}
	if raw := strings.TrimSpace(os.Getenv("COLUMNS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 40 {
			width = parsed
		}
	}
	return width
}

func (theme cliTheme) paint(code, value string) string {
	if !theme.Enabled || value == "" {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func (theme cliTheme) cyan(value string) string  { return theme.paint("36", value) }
func (theme cliTheme) blue(value string) string  { return theme.paint("94", value) }
func (theme cliTheme) green(value string) string { return theme.paint("32", value) }
func (theme cliTheme) yellow(value string) string {
	return theme.paint("33", value)
}
func (theme cliTheme) red(value string) string  { return theme.paint("31", value) }
func (theme cliTheme) bold(value string) string { return theme.paint("1", value) }
func (theme cliTheme) dim(value string) string  { return theme.paint("2", value) }

func selectMenu(reader *bufio.Reader, prompt string, options []menuOption, defaultIndex int) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("menu has no options")
	}
	if defaultIndex < 0 || defaultIndex >= len(options) {
		defaultIndex = 0
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return selectNumberedMenu(reader, prompt, options, defaultIndex)
	}

	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return selectNumberedMenu(reader, prompt, options, defaultIndex)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	hideTerminalCursor()
	defer showTerminalCursor()

	theme := currentTheme()
	fmt.Fprintln(os.Stdout, theme.bold(prompt))
	selected := defaultIndex
	rendered := false
	for {
		if rendered {
			// The cursor is left immediately below the previous menu block.
			fmt.Fprintf(os.Stdout, "\x1b[%dA", len(options))
		}
		for index, option := range options {
			marker := "○ "
			if index == selected {
				marker = "● "
			}
			line := marker + option.Label
			if option.Description != "" {
				line += " — " + option.Description
			}
			line = truncateText(line, terminalWidth()-2)
			if index == selected {
				line = theme.cyan(line)
			} else {
				line = theme.dim(line)
			}
			fmt.Fprintf(os.Stdout, "\r\x1b[2K%s\n", line)
		}
		rendered = true

		var first [1]byte
		if _, err := os.Stdin.Read(first[:]); err != nil {
			return 0, err
		}
		switch first[0] {
		case 3, 27:
			if first[0] == 3 {
				return 0, errors.New("setup cancelled")
			}
			var sequence [2]byte
			if _, err := io.ReadFull(os.Stdin, sequence[:]); err != nil {
				return 0, err
			}
			if sequence[0] != '[' && sequence[0] != 'O' {
				continue
			}
			switch sequence[1] {
			case 'A':
				selected = (selected + len(options) - 1) % len(options)
			case 'B':
				selected = (selected + 1) % len(options)
			}
		case 'k', 'K':
			selected = (selected + len(options) - 1) % len(options)
		case 'j', 'J':
			selected = (selected + 1) % len(options)
		case 13, 10:
			clearMenu(len(options))
			fmt.Fprintf(os.Stdout, "\r\x1b[2K%s %s\n", theme.green("(✓)"), theme.bold(options[selected].Label))
			return selected, nil
		case 'q', 'Q':
			return 0, errors.New("setup cancelled")
		}
	}
}

func selectNumberedMenu(reader *bufio.Reader, prompt string, options []menuOption, defaultIndex int) (int, error) {
	theme := currentTheme()
	fmt.Println()
	fmt.Println(theme.bold(prompt))
	for index, option := range options {
		key := strconv.Itoa(index + 1)
		line := fmt.Sprintf("  [%s] %s", key, option.Label)
		if option.Description != "" {
			line += " — " + option.Description
		}
		fmt.Println(truncateText(line, terminalWidth()-2))
	}
	fmt.Printf("Choose [%d]: ", defaultIndex+1)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return defaultIndex, nil
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 1 || choice > len(options) {
		fmt.Println(theme.yellow("Please choose one of the displayed options."))
		return selectNumberedMenu(reader, prompt, options, defaultIndex)
	}
	fmt.Printf("%s %s\n", theme.green("(✓)"), theme.bold(options[choice-1].Label))
	return choice - 1, nil
}

func selectMultiMenu(reader *bufio.Reader, prompt string, options []menuOption, selected []bool) ([]bool, error) {
	if len(options) == 0 {
		return nil, errors.New("multi-select menu has no options")
	}
	if len(selected) != len(options) {
		selected = make([]bool, len(options))
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return selectNumberedMultiMenu(reader, prompt, options, selected)
	}

	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return selectNumberedMultiMenu(reader, prompt, options, selected)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	hideTerminalCursor()
	defer showTerminalCursor()

	theme := currentTheme()
	fmt.Fprintln(os.Stdout, theme.bold(prompt))
	fmt.Fprintln(os.Stdout, theme.dim("Space toggles a service · Enter continues"))
	cursor := 0
	rendered := false
	for {
		if rendered {
			fmt.Fprintf(os.Stdout, "\x1b[%dA", len(options))
		}
		for index, option := range options {
			marker := "○ "
			if selected[index] {
				marker = "● "
			}
			line := marker + option.Label
			if option.Description != "" {
				line += " — " + option.Description
			}
			line = truncateText(line, terminalWidth()-2)
			if index == cursor {
				line = theme.cyan(line)
			} else {
				line = theme.dim(line)
			}
			fmt.Fprintf(os.Stdout, "\r\x1b[2K%s\n", line)
		}
		rendered = true

		var first [1]byte
		if _, err := os.Stdin.Read(first[:]); err != nil {
			return nil, err
		}
		switch first[0] {
		case 3, 27:
			if first[0] == 3 {
				return nil, errors.New("setup cancelled")
			}
			var sequence [2]byte
			if _, err := io.ReadFull(os.Stdin, sequence[:]); err != nil {
				return nil, err
			}
			if sequence[0] != '[' && sequence[0] != 'O' {
				continue
			}
			switch sequence[1] {
			case 'A':
				cursor = (cursor + len(options) - 1) % len(options)
			case 'B':
				cursor = (cursor + 1) % len(options)
			}
		case ' ', 't', 'T':
			selected[cursor] = !selected[cursor]
		case 'j', 'J':
			cursor = (cursor + 1) % len(options)
		case 'k', 'K':
			cursor = (cursor + len(options) - 1) % len(options)
		case 13, 10:
			clearMenu(len(options))
			for index, option := range options {
				if selected[index] {
					fmt.Fprintf(os.Stdout, "\r\x1b[2K%s %s\n", theme.green("(✓)"), theme.bold(option.Label))
				}
			}
			return selected, nil
		case 'q', 'Q':
			return nil, errors.New("setup cancelled")
		}
	}
}

func selectNumberedMultiMenu(reader *bufio.Reader, prompt string, options []menuOption, selected []bool) ([]bool, error) {
	theme := currentTheme()
	fmt.Println()
	fmt.Println(theme.bold(prompt))
	for index, option := range options {
		mark := "○"
		if selected[index] {
			mark = "●"
		}
		line := fmt.Sprintf("  [%d] %s %s", index+1, mark, option.Label)
		if option.Description != "" {
			line += " — " + option.Description
		}
		fmt.Println(truncateText(line, terminalWidth()-2))
	}
	fmt.Print("Choose numbers separated by commas, or Enter for none: ")
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	for index := range selected {
		selected[index] = false
	}
	for _, value := range strings.Split(answer, ",") {
		choice, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil || choice < 1 || choice > len(options) {
			if strings.TrimSpace(value) != "" {
				fmt.Println(theme.yellow("Please choose valid option numbers."))
				return selectNumberedMultiMenu(reader, prompt, options, selected)
			}
			continue
		}
		selected[choice-1] = true
	}
	for index, option := range options {
		if selected[index] {
			fmt.Printf("%s %s\n", theme.green("(✓)"), theme.bold(option.Label))
		}
	}
	return selected, nil
}

func clearMenu(lines int) {
	if lines <= 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "\x1b[%dA", lines)
	for index := 0; index < lines; index++ {
		fmt.Fprint(os.Stdout, "\r\x1b[2K")
		if index < lines-1 {
			fmt.Fprint(os.Stdout, "\n")
		}
	}
	if lines > 1 {
		fmt.Fprintf(os.Stdout, "\x1b[%dA", lines-1)
	}
}

// Interactive menus own the terminal cursor while they redraw. Hiding it
// prevents a stray block/caret from appearing inside the setup layout (and
// makes the circle-style option markers the only selection indicator).
func hideTerminalCursor() { fmt.Fprint(os.Stdout, "\x1b[?25l") }
func showTerminalCursor() { fmt.Fprint(os.Stdout, "\x1b[?25h") }

func truncateText(value string, width int) string {
	if width < 4 || len([]rune(value)) <= width {
		return value
	}
	runes := []rune(value)
	return string(runes[:width-1]) + "…"
}

func progressLabel(method, path string) string {
	switch {
	case strings.HasSuffix(path, "/discovery/scan"):
		return "Scanning the local network"
	case strings.HasSuffix(path, "/pair"):
		return "Waiting for device approval"
	case strings.HasSuffix(path, "/unpair"):
		return "Removing saved pairing"
	case method != "GET":
		return "Sending command"
	default:
		return "Working"
	}
}

type requestResult struct {
	output any
	err    error
}

func requestWithProgress(cfg config.Config, secrets *security.SecretStore, method, path string, body any) (any, error) {
	if !currentTheme().Enabled || (!strings.HasSuffix(path, "/discovery/scan") && !strings.HasSuffix(path, "/pair") && !strings.HasSuffix(path, "/unpair") && method == "GET") {
		return request(cfg, secrets, method, path, body)
	}

	done := make(chan requestResult, 1)
	go func() {
		output, err := request(cfg, secrets, method, path, body)
		done <- requestResult{output: output, err: err}
	}()

	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	label := progressLabel(method, path)
	for {
		select {
		case result := <-done:
			if result.err != nil {
				fmt.Fprintf(os.Stdout, "\r\x1b[2K%s %s\n", currentTheme().red("✗"), result.err)
			} else {
				fmt.Fprintf(os.Stdout, "\r\x1b[2K%s %s\n", currentTheme().green("✓"), label)
			}
			return result.output, result.err
		case <-ticker.C:
			fmt.Fprintf(os.Stdout, "\r\x1b[2K%s %s", currentTheme().cyan(frames[frame%len(frames)]), currentTheme().dim(label))
			frame++
		}
	}
}
