# bashcapture

Run any shell command and automatically save a PNG screenshot of the terminal output.

The command runs normally in your terminal (with full stdin/stdout passthrough), and when it finishes, a timestamped PNG is saved to `~/Pictures/Screenshots/`.

## Install

```
go install github.com/Jason-Hendry/bashcapture@latest
```

Or build from source:

```
git clone <repo>
cd bashcapture
go build
```

## Usage

```
bashcapture <command> [args...]
```

### Examples

```bash
bashcapture ls
bashcapture grep -r "TODO" src/
bashcapture ssh myserver uname -a
bashcapture 'cat /etc/os-release | head -5'
```

Images are saved to `~/Pictures/Screenshots/<timestamp>.png`.

## Features

- **Transparent passthrough** -- stdin, stdout, and terminal resize all work normally. The wrapped command behaves exactly as if you ran it directly.
- **ANSI color support** -- standard 8/16 colors, 256-color palette, and 24-bit RGB are rendered in the output image.
- **PTY execution** -- commands run in a real pseudo-terminal, so programs that auto-detect color (like `ls`, `grep`, `gcc`) produce colored output without `--color=always`.
- **Interactive shell** -- runs via `bash -ic`, so your aliases and `.bashrc` settings apply.
- **Embedded font** -- uses JetBrains Mono, bundled into the binary. No external font dependencies.
- **Long output handling** -- captures up to 1000 lines. If output exceeds that, the last 1000 lines are kept.
- **One Atom Dark theme** -- dark background with colors based on the One Dark palette.

## How it works

1. The command is launched in a PTY via an interactive bash shell.
2. PTY output is tee'd to both your real terminal and an internal capture buffer.
3. When the command exits, ANSI escape sequences are parsed for color information, and non-SGR control sequences are stripped.
4. The captured text is rendered line-by-line onto a PNG image using the embedded JetBrains Mono font.

## Limitations

- Does not handle ncurses/full-screen applications (`vim`, `htop`, `less`, etc.). These rely on cursor movement and alternate screen buffers that aren't captured.
- Background colors in ANSI output are not rendered -- only foreground colors are supported.

## License

JetBrains Mono is licensed under the [OFL-1.1](JetBrainsMono-2.304/OFL.txt).
