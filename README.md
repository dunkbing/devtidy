# DevTidy

A terminal UI app to clean up development dependencies and build artifacts.

## What it cleans

### Default mode
- `node_modules` (Node.js)
- `target` (Rust)
- `__pycache__`, `venv` (Python)
- `build`, `dist` (Build artifacts)
- `.gradle` (Java)
- `deps`, `_build` (Elixir)
- Log files, temp files, and more

### Gitignore mode (`--gitignore`)
- Files and directories matching patterns in `.gitignore`
- Requires a `.gitignore` file in the target directory

## Install

### Homebrew (macOS/Linux)
```bash
brew install --cask dunkbing/brews/devtidy
```

### Go install
```bash
go install github.com/dunkbing/devtidy@latest
```

## Usage

```bash
# Scan current directory
devtidy

# Scan specific directory
devtidy /path/to/dir
```

## Controls

- `↑/↓ or k/j` - Navigate items
- `space` - Toggle selection (✓ = selected)
- `c` - Clean selected items
- `/` - Filter items
- `q` - Quit

## Safety

Only cleans items you explicitly select. Shows size before cleaning.
