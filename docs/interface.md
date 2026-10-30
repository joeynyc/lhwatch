# Interface notes

The key table is in the README. This page covers the mouse and layout.

## Mouse

- Click a service to select it, and click it again for details.
- Click a host to filter by it.
- Use the wheel to move or scroll.
- Click the footer hints.
- Click the header to close a panel.
- A click never sends a signal. Confirming a stop always needs `y`.

### Selecting text

While lhwatch has the mouse, select text by holding Option in iTerm2, Shift in most other terminals, or Fn in macOS Terminal (where Command-R also toggles mouse reporting). Or run with `--no-mouse`.

### Pointer

In kitty, Ghostty, foot, and recent iTerm2, lhwatch shows an arrow pointer instead of the text I-beam (via the OSC 22 escape sequence) and restores the terminal's pointer on exit. Other terminals ignore the request.

## Layout

The layout adapts to the terminal. A wide window gives you the most context. Details remain accessible in narrower windows.

External names, commands, and errors are sanitized before rendering.
