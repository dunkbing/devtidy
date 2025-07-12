package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
)

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

type state int

const (
	stateScanning state = iota
	stateSelecting
	stateCleaning
	stateComplete
)

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
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	prog := progress.New(progress.WithDefaultGradient())

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

type scanJob struct {
	root   string
	info   os.FileInfo
}

func boundedWalk(root string, maxWorkers int) <-chan scanJob {
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU()
	}

	out := make(chan scanJob, maxWorkers*2)
	go func() {
		defer close(out)

		// work queue
		work := []string{root}
		var mu sync.Mutex
		var wg sync.WaitGroup

		// worker function
		worker := func() {
			defer wg.Done()
			for {
				mu.Lock()
				if len(work) == 0 {
					mu.Unlock()
					return
				}
				dir := work[len(work)-1]
				work = work[:len(work)-1]
				mu.Unlock()

				entries, err := os.ReadDir(dir)
				if err != nil {
					continue
				}
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					name := e.Name()
					if strings.HasPrefix(name, ".") && name != "." {
						if name == ".git" {
							continue
						}
					}
					path := filepath.Join(dir, name)
					info, _ := e.Info()
					out <- scanJob{root: path, info: info}

					mu.Lock()
					work = append(work, path)
					mu.Unlock()
				}
			}
		}

		// start workers
		wg.Add(maxWorkers)
		for i := 0; i < maxWorkers; i++ {
			go worker()
		}
		wg.Wait()
	}()
	return out
}

// Commands
func scanForCleanableItems(dir string, useGitignore bool) tea.Cmd {
	return func() tea.Msg {
		var items []CleanableItem
		mx := sync.Mutex{}

		if useGitignore {
			gitignoreItems := scanGitignoreItems(dir)
			items = append(items, gitignoreItems...)
			return scanCompleteMsg(items)
		}

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

		sizeCache := make(map[string]int64)
		var cacheMu sync.Mutex
		var wg sync.WaitGroup

		for j := range boundedWalk(dir, runtime.NumCPU()) {
			j := j
			wg.Add(1)
			go func() {
				defer wg.Done()
				name := filepath.Base(j.root)
				for pat, desc := range patterns {
					var match bool
					if strings.Contains(pat, "*") {
						match, _ = filepath.Match(pat, name)
					} else {
						match = name == pat
					}
					if match {
						cacheMu.Lock()
						if _, ok := sizeCache[j.root]; !ok {
							sizeCache[j.root] = getDirectorySize(j.root)
						}
						sz := sizeCache[j.root]
						cacheMu.Unlock()

						mx.Lock()
						items = append(items, CleanableItem{
							Path:     j.root,
							Type:     desc,
							Size:     sz,
							Info:     desc,
							Selected: false,
						})
						mx.Unlock()
						return
					}
				}
			}()
		}
		wg.Wait()
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
	gitignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		return nil
	}

	file, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Read all non-comment patterns once
	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "!") {
			patterns = append(patterns, line)
		}
	}

	var (
		items []CleanableItem
		mu    sync.Mutex
	)

	for job := range boundedWalk(dir, runtime.NumCPU()) {
		path := job.root
		rel, _ := filepath.Rel(dir, path)
		for _, pat := range patterns {
			if matchesGitignorePattern(pat, rel) {
				mu.Lock()
				// de-dup
				found := false
				for _, it := range items {
					if it.Path == path {
						found = true
						break
					}
				}
				if !found {
					items = append(items, CleanableItem{
						Path:     path,
						Type:     "Gitignore pattern: " + pat,
						Size:     getDirectorySize(path),
						Info:     "Matches .gitignore pattern",
						Selected: false,
					})
				}
				mu.Unlock()
				break // one match is enough
			}
		}
	}
	return items
}

func matchesGitignorePattern(pattern, path string) bool {
	if strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
		return strings.HasPrefix(path, pattern+"/") || path == pattern
	}

	if strings.Contains(pattern, "*") {
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			return true
		}
		matched, _ = filepath.Match(pattern, path)
		return matched
	}

	// Exact match or path contains pattern
	return path == pattern || strings.Contains(path, pattern) || strings.HasSuffix(path, "/"+pattern)
}

func getDirectorySize(path string) int64 {
	var size int64
	var wg sync.WaitGroup
	limit := make(chan struct{}, runtime.NumCPU())

	filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		limit <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			if fi, err := d.Info(); err == nil {
				atomic.AddInt64(&size, fi.Size())
			}
		}()
		return nil
	})
	wg.Wait()
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

const version = "v1.0.4"

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

	if *helpFlag || *help2Flag {
		showHelp()
		return
	}

	if *versionFlag || *version2Flag {
		showVersion()
		return
	}

	targetDir := "."
	args := flag.Args()
	if len(args) > 0 {
		targetDir = args[0]

		if info, err := os.Stat(targetDir); err != nil {
			log.Fatalf("Error: Directory '%s' does not exist or is not accessible", targetDir)
		} else if !info.IsDir() {
			log.Fatalf("Error: '%s' is not a directory", targetDir)
		}

		if absPath, err := filepath.Abs(targetDir); err == nil {
			targetDir = absPath
		}
	} else {
		if currentDir, err := os.Getwd(); err == nil {
			targetDir = currentDir
		}
	}

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
