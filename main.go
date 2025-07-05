package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CleanableItem represents a directory or file that can be cleaned
type CleanableItem struct {
	Path     string
	Type     string
	Size     int64
	Info     string
	Selected bool
}

func (i CleanableItem) Title() string       { return i.Path }
func (i CleanableItem) Description() string { return fmt.Sprintf("%s - %s", i.Type, formatSize(i.Size)) }
func (i CleanableItem) FilterValue() string { return i.Path }

// Define the different states of the app
type state int

const (
	stateScanning state = iota
	stateSelecting
	stateCleaning
	stateComplete
)

// Messages for the tea program
type scanCompleteMsg []CleanableItem
type cleanCompleteMsg struct{}
type cleanProgressMsg struct {
	item string
	done int
	total int
}

// Model represents the application state
type Model struct {
	state       state
	list        list.Model
	items       []CleanableItem
	spinner     spinner.Model
	progress    progress.Model
	cleaning    bool
	totalSize   int64
	cleanedSize int64
	currentDir  string
	err         error
}

// Key mappings
var keys = struct {
	toggle key.Binding
	clean  key.Binding
	quit   key.Binding
	help   key.Binding
}{
	toggle: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle selection"),
	),
	clean: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "clean selected"),
	),
	quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
}

// Styles
var (
	docStyle = lipgloss.NewStyle().Margin(1, 2)

	titleStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("170")).
		Bold(true)

	errorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")).
		Bold(true)

	successStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true)
)

func initialModel(targetDir string) Model {
	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	// Initialize progress bar
	prog := progress.New(progress.WithDefaultGradient())

	// Initialize list
	l := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Cleanable Items"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = titleStyle

	return Model{
		state:      stateScanning,
		list:       l,
		items:      []CleanableItem{},
		spinner:    s,
		progress:   prog,
		currentDir: targetDir,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		scanForCleanableItems(m.currentDir),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v-3)
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateScanning:
			if key.Matches(msg, keys.quit) {
				return m, tea.Quit
			}
		case stateSelecting:
			switch {
			case key.Matches(msg, keys.quit):
				return m, tea.Quit
			case key.Matches(msg, keys.toggle):
				return m.toggleSelection(), nil
			case key.Matches(msg, keys.clean):
				return m.startCleaning()
			}
		case stateCleaning:
			if key.Matches(msg, keys.quit) {
				return m, tea.Quit
			}
		case stateComplete:
			if key.Matches(msg, keys.quit) {
				return m, tea.Quit
			}
		}

	case scanCompleteMsg:
		m.items = []CleanableItem(msg)
		m.state = stateSelecting

		// Convert items to list items
		listItems := make([]list.Item, len(m.items))
		for i, item := range m.items {
			listItems[i] = item
		}

		m.list.SetItems(listItems)
		return m, nil

	case cleanProgressMsg:
		cmd := m.progress.SetPercent(float64(msg.done) / float64(msg.total))
		return m, cmd

	case cleanCompleteMsg:
		m.state = stateComplete
		m.cleaning = false
		return m, nil

	case spinner.TickMsg:
		if m.state == stateScanning {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	// Update list
	if m.state == stateSelecting {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	switch m.state {
	case stateScanning:
		return docStyle.Render(fmt.Sprintf(
			"%s Scanning for cleanable items...\n\nDirectory: %s",
			m.spinner.View(),
			m.currentDir,
		))

	case stateSelecting:
		help := "\nControls:\n" +
			"  space: toggle selection\n" +
			"  c: clean selected items\n" +
			"  q: quit\n" +
			"  /: filter items"

		totalSize := m.calculateTotalSelectedSize()
		selectedCount := m.countSelectedItems()

		status := fmt.Sprintf(
			"\nSelected: %d items (%s)",
			selectedCount,
			formatSize(totalSize),
		)

		return docStyle.Render(m.list.View() + status + help)

	case stateCleaning:
		return docStyle.Render(fmt.Sprintf(
			"Cleaning selected items...\n\n%s\n\nPress q to quit",
			m.progress.View(),
		))

	case stateComplete:
		return docStyle.Render(successStyle.Render(
			fmt.Sprintf(
				"✓ Cleaning complete!\n\nCleaned: %s\n\nPress q to quit",
				formatSize(m.cleanedSize),
			),
		))
	}

	return ""
}

func (m Model) toggleSelection() Model {
	if selectedItem, ok := m.list.SelectedItem().(CleanableItem); ok {
		// Find the item in our slice and toggle it
		for i, item := range m.items {
			if item.Path == selectedItem.Path {
				m.items[i].Selected = !m.items[i].Selected

				// Update the list item
				listItems := make([]list.Item, len(m.items))
				for j, item := range m.items {
					listItems[j] = item
				}
				m.list.SetItems(listItems)
				break
			}
		}
	}
	return m
}

func (m Model) startCleaning() (Model, tea.Cmd) {
	if m.countSelectedItems() == 0 {
		return m, nil
	}

	m.state = stateCleaning
	m.cleaning = true

	return m, cleanSelectedItems(m.items)
}

func (m Model) calculateTotalSelectedSize() int64 {
	var total int64
	for _, item := range m.items {
		if item.Selected {
			total += item.Size
		}
	}
	return total
}

func (m Model) countSelectedItems() int {
	count := 0
	for _, item := range m.items {
		if item.Selected {
			count++
		}
	}
	return count
}

// Commands
func scanForCleanableItems(dir string) tea.Cmd {
	return func() tea.Msg {
		var items []CleanableItem

		// Define patterns to look for
		patterns := map[string]string{
			"node_modules":     "Node.js dependencies",
			"target":           "Rust build artifacts",
			"build":            "Build artifacts",
			"dist":             "Distribution files",
			"__pycache__":      "Python cache",
			".pytest_cache":    "Pytest cache",
			"venv":             "Python virtual environment",
			"env":              "Python virtual environment",
			".venv":            "Python virtual environment",
			"vendor":           "Vendor dependencies",
			"deps":             "Elixir dependencies",
			"_build":           "Elixir build artifacts",
			".gradle":          "Gradle cache",
			"cmake-build-debug": "CMake build artifacts",
			"cmake-build-release": "CMake build artifacts",
			"DerivedData":      "Xcode derived data",
			"*.log":            "Log files",
			"*.tmp":            "Temporary files",
		}

		// Walk through directory tree
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip errors, continue walking
			}

			// Skip hidden directories and files at root level
			if strings.HasPrefix(filepath.Base(path), ".") && path != dir {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			// Check if this matches any of our patterns
			basename := filepath.Base(path)
			for pattern, description := range patterns {
				if strings.Contains(pattern, "*") {
					// Handle glob patterns
					if matched, _ := filepath.Match(pattern, basename); matched {
						size := getDirectorySize(path)
						items = append(items, CleanableItem{
							Path:     path,
							Type:     description,
							Size:     size,
							Info:     description,
							Selected: false,
						})
					}
				} else if basename == pattern {
					size := getDirectorySize(path)
					items = append(items, CleanableItem{
						Path:     path,
						Type:     description,
						Size:     size,
						Info:     description,
						Selected: false,
					})

					// If it's a directory, skip walking into it
					if info.IsDir() {
						return filepath.SkipDir
					}
				}
			}

			return nil
		})

		if err != nil {
			return scanCompleteMsg([]CleanableItem{})
		}

		return scanCompleteMsg(items)
	}
}

func cleanSelectedItems(items []CleanableItem) tea.Cmd {
	return func() tea.Msg {
		var totalCleaned int64
		selectedItems := []CleanableItem{}

		// Get selected items
		for _, item := range items {
			if item.Selected {
				selectedItems = append(selectedItems, item)
			}
		}

		// Clean each selected item
		for i, item := range selectedItems {
			if err := os.RemoveAll(item.Path); err == nil {
				totalCleaned += item.Size
			}

			// Send progress update
			tea.NewProgram(nil).Send(cleanProgressMsg{
				item:  item.Path,
				done:  i + 1,
				total: len(selectedItems),
			})
		}

		return cleanCompleteMsg{}
	}
}

// Helper functions
func getDirectorySize(path string) int64 {
	var size int64

	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})

	if err != nil {
		return 0
	}

	return size
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func main() {
	// Check if bubbletea dependencies are available
	if _, err := exec.LookPath("go"); err != nil {
		log.Fatal("Go is required to run this application")
	}

	// Get target directory from command line args or use current directory
	targetDir := "."
	if len(os.Args) > 1 {
		targetDir = os.Args[1]

		// Verify the directory exists
		if info, err := os.Stat(targetDir); err != nil {
			log.Fatalf("Error: Directory '%s' does not exist or is not accessible", targetDir)
		} else if !info.IsDir() {
			log.Fatalf("Error: '%s' is not a directory", targetDir)
		}

		// Convert to absolute path
		if absPath, err := filepath.Abs(targetDir); err == nil {
			targetDir = absPath
		}
	} else {
		// Get current directory
		if currentDir, err := os.Getwd(); err == nil {
			targetDir = currentDir
		}
	}

	model := initialModel(targetDir)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
