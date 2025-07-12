package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
)

// CleanableItem represents a directory or file that can be cleaned
type CleanableItem struct {
	Path     string
	Type     string
	Size     int64
	Info     string
	Selected bool
}

func (i CleanableItem) Title() string {
	if i.Selected {
		return selectedStyle.Render("✓ " + i.Path)
	}
	return i.Path
}

func (i CleanableItem) Description() string {
	desc := fmt.Sprintf("%s - %s", i.Type, formatSize(i.Size))
	if i.Selected {
		return selectedStyle.Render(desc)
	}
	return desc
}

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
	item  string
	done  int
	total int
}

// Model represents the application state
type Model struct {
	state         state
	list          list.Model
	items         []CleanableItem
	spinner       spinner.Model
	progress      progress.Model
	cleaning      bool
	totalSize     int64
	cleanedSize   int64
	currentDir    string
	useGitignore  bool
	scanStartTime time.Time
	scanDuration  time.Duration
	scannedItems  int
	err           error
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
			Foreground(lipgloss.Color("42")).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42")).
			Bold(true)
)

func initialModel(targetDir string, useGitignore bool) Model {
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
		state:         stateScanning,
		list:          l,
		items:         []CleanableItem{},
		spinner:       s,
		progress:      prog,
		currentDir:    targetDir,
		useGitignore:  useGitignore,
		scanStartTime: time.Now(),
		scannedItems:  0,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		scanForCleanableItems(m.currentDir, m.useGitignore),
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
				if !m.cleaning {
					return m.toggleSelection(), nil
				}
			case key.Matches(msg, keys.clean):
				if !m.cleaning {
					return m.startCleaning()
				}
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
		m.scannedItems = len(m.items)
		m.scanDuration = time.Since(m.scanStartTime)
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

	case cleanSingleItem:
		if msg.index >= len(msg.items) {
			return m, func() tea.Msg { return cleanCompleteMsg{} }
		}

		item := msg.items[msg.index]

		// Clean the item and update cleaned size
		if err := os.RemoveAll(item.Path); err == nil {
			m.cleanedSize += item.Size

			// Remove the cleaned item from the model's items list
			for i, modelItem := range m.items {
				if modelItem.Path == item.Path {
					m.items = append(m.items[:i], m.items[i+1:]...)
					break
				}
			}

			// Update the list display
			listItems := make([]list.Item, len(m.items))
			for i, modelItem := range m.items {
				listItems[i] = modelItem
			}
			m.list.SetItems(listItems)
		}

		// Send progress update
		progressCmd := func() tea.Msg {
			return cleanProgressMsg{
				item:  item.Path,
				done:  msg.index + 1,
				total: msg.total,
			}
		}

		// Continue with next item or complete
		var nextCmd tea.Cmd
		if msg.index+1 < len(msg.items) {
			nextCmd = tea.Tick(time.Millisecond*100, func(time.Time) tea.Msg {
				return cleanSingleItem{
					items: msg.items,
					index: msg.index + 1,
					total: msg.total,
				}
			})
		} else {
			nextCmd = func() tea.Msg { return cleanCompleteMsg{} }
		}

		return m, tea.Batch(progressCmd, nextCmd)

	case cleanCompleteMsg:
		m.state = stateSelecting
		m.cleaning = false
		m.scannedItems = len(m.items) // Update total items count
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
		elapsed := time.Since(m.scanStartTime)
		return docStyle.Render(fmt.Sprintf(
			"%s Scanning for cleanable items...\n\nDirectory: %s\nElapsed: %v\nItems found: %d",
			m.spinner.View(),
			m.currentDir,
			elapsed.Round(time.Millisecond),
			m.scannedItems,
		))

	case stateSelecting:
		help := "\nControls:\n" +
			"  space: toggle selection (✓ = selected)\n" +
			"  c: clean selected items\n" +
			"  q: quit\n" +
			"  /: filter items"

		totalSize := m.calculateTotalSelectedSize()
		selectedCount := m.countSelectedItems()

		status := fmt.Sprintf(
			"\nScan time: %v (%d items) | Selected: %d items (%s)",
			m.scanDuration.Round(time.Millisecond),
			m.scannedItems,
			selectedCount,
			formatSize(totalSize),
		)

		content := m.list.View() + status

		// Show progress bar if cleaning
		if m.cleaning {
			content += "\n\nCleaning in progress...\n" + m.progress.View()
		}

		content += help

		return docStyle.Render(content)

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
func scanForCleanableItems(dir string, useGitignore bool) tea.Cmd {
	return func() tea.Msg {
		var items []CleanableItem

		if useGitignore {
			// Scan .gitignore file for patterns
			gitignoreItems := scanGitignoreItems(dir)
			items = append(items, gitignoreItems...)
		} else {
			// Define patterns to look for
			patterns := map[string]string{
				"node_modules":        "Node.js dependencies",
				"target":              "Rust build artifacts",
				"build":               "Build artifacts",
				"dist":                "Distribution files",
				"__pycache__":         "Python cache",
				".pytest_cache":       "Pytest cache",
				"venv":                "Python virtual environment",
				"env":                 "Python virtual environment",
				".venv":               "Python virtual environment",
				"vendor":              "Vendor dependencies",
				"deps":                "Elixir dependencies",
				"_build":              "Elixir build artifacts",
				".gradle":             "Gradle cache",
				"cmake-build-debug":   "CMake build artifacts",
				"cmake-build-release": "CMake build artifacts",
				"DerivedData":         "Xcode derived data",
				"*.log":               "Log files",
				"*.tmp":               "Temporary files",
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
		}

		return scanCompleteMsg(items)
	}
}

func cleanSelectedItems(items []CleanableItem) tea.Cmd {
	return tea.Batch(startCleaningProcess(items))
}

func startCleaningProcess(items []CleanableItem) tea.Cmd {
	return func() tea.Msg {
		selectedItems := []CleanableItem{}
		for _, item := range items {
			if item.Selected {
				selectedItems = append(selectedItems, item)
			}
		}

		if len(selectedItems) == 0 {
			return cleanCompleteMsg{}
		}

		// Start with first item
		return cleanSingleItem{
			items: selectedItems,
			index: 0,
			total: len(selectedItems),
		}
	}
}

// New message type for cleaning single items
type cleanSingleItem struct {
	items []CleanableItem
	index int
	total int
}

func scanGitignoreItems(dir string) []CleanableItem {
	var items []CleanableItem
	gitignorePath := filepath.Join(dir, ".gitignore")

	// Check if .gitignore exists
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		return items
	}

	// Read .gitignore file
	file, err := os.Open(gitignorePath)
	if err != nil {
		return items
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Skip negation patterns for now
		if strings.HasPrefix(line, "!") {
			continue
		}

		// Walk through directory to find matches
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			// Skip the .git directory itself
			if strings.Contains(path, "/.git/") || strings.HasSuffix(path, "/.git") {
				return filepath.SkipDir
			}

			// Get relative path from directory
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return nil
			}

			if matchesGitignorePattern(line, relPath) {
				for _, existing := range items {
					if existing.Path == path {
						return nil
					}
				}

				size := getDirectorySize(path)
				items = append(items, CleanableItem{
					Path:     path,
					Type:     "Gitignore pattern: " + line,
					Size:     size,
					Info:     "Matches .gitignore pattern",
					Selected: false,
				})

				// If it's a directory, skip walking into it
				if info.IsDir() {
					return filepath.SkipDir
				}
			}

			return nil
		})

		if err != nil {
			continue
		}
	}

	return items
}

func matchesGitignorePattern(pattern, path string) bool {
	// Handle directory patterns
	if strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
		return strings.HasPrefix(path, pattern+"/") || path == pattern
	}

	// Handle glob patterns
	if strings.Contains(pattern, "*") {
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			return true
		}
		// Also try matching the full relative path
		matched, _ = filepath.Match(pattern, path)
		return matched
	}

	// Exact match or path contains pattern
	return path == pattern || strings.Contains(path, pattern) || strings.HasSuffix(path, "/"+pattern)
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

const version = "v1.0.3"

func showVersion() {
	fmt.Printf("devtidy %s\n", version)
	fmt.Printf("Built with Go %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func showHelp() {
	fmt.Printf("devtidy %s - Clean development artifacts from your projects\n\n", version)
	fmt.Println("USAGE:")
	fmt.Println("  devtidy [options] [directory]")
	fmt.Println()
	fmt.Println("OPTIONS:")
	fmt.Println("  -h, --help      Show this help message")
	fmt.Println("  -v, --version   Show version information")
	fmt.Println("  --gitignore     Scan files matching .gitignore patterns")
	fmt.Println()
	fmt.Println("ARGUMENTS:")
	fmt.Println("  directory       Target directory to scan (default: current directory)")
	fmt.Println()
	fmt.Println("DESCRIPTION:")
	fmt.Println("  DevTidy helps you clean up common development artifacts like:")
	fmt.Println("  • node_modules (Node.js dependencies)")
	fmt.Println("  • target (Rust build artifacts)")
	fmt.Println("  • __pycache__ (Python cache files)")
	fmt.Println("  • build/dist (Build and distribution files)")
	fmt.Println("  • .gradle (Gradle cache)")
	fmt.Println("  • And many more...")
	fmt.Println()
	fmt.Println("EXAMPLES:")
	fmt.Println("  devtidy                    # Scan current directory")
	fmt.Println("  devtidy /path/to/project   # Scan specific directory")
	fmt.Println("  devtidy --gitignore        # Scan using .gitignore patterns")
	fmt.Println()
}

func main() {
	// Define command line flags
	var gitignoreFlag = flag.Bool("gitignore", false, "scan files matching .gitignore patterns")
	var helpFlag = flag.Bool("h", false, "show help")
	var help2Flag = flag.Bool("help", false, "show help")
	var versionFlag = flag.Bool("v", false, "show version")
	var version2Flag = flag.Bool("version", false, "show version")
	flag.Parse()

	// Handle help flag
	if *helpFlag || *help2Flag {
		showHelp()
		return
	}

	// Handle version flag
	if *versionFlag || *version2Flag {
		showVersion()
		return
	}

	// Get target directory from remaining args or use current directory
	targetDir := "."
	args := flag.Args()
	if len(args) > 0 {
		targetDir = args[0]

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

	// Check if gitignore flag is used but .gitignore doesn't exist
	if *gitignoreFlag {
		gitignorePath := filepath.Join(targetDir, ".gitignore")
		if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
			log.Fatalf("Error: .gitignore file not found in directory '%s'", targetDir)
		}
	}

	model := initialModel(targetDir, *gitignoreFlag)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
