# keywrap

`keywrap` is a tiny Unix utility that lets you **run any command inside a pseudo-terminal (PTY) while remapping keys on-the-fly**.  
It is especially handy when you want to:

- Attach custom shortcuts to long-running TUI programs (e.g. `fzf`, `bat`, `fx`, etc.).
- Replace the current process with another one on a key-press (`become`).
- Spawn auxiliary commands without leaving the current view (`execute`).
- Keep the screen visible after the child exits (`--hold`).

---

## Quick start

```bash
# Install
go install github.com/urie96/keywrap@latest

# Example: run nvim when you press Ctrl-E
keywrap --bind "ctrl-e:become(nvim README.md)" -- bat README.md
```

---

## Usage

```
keywrap [OPTIONS] -- <command> [args...]
```

| Option                    | Meaning                                                         |
| ------------------------- | --------------------------------------------------------------- |
| `--bind "<key>:<action>"` | Map a key to an action. May be repeated.                        |
| `--hold`, `-h`            | Do **not** quit after the child process ends; wait for any key. |
| `--input "<text>"`        | Feed literal text into the child’s stdin right after start.     |

### Supported keys

| Key literal | Example                      |
| ----------- | ---------------------------- |
| Single char | `q`, `Q`, `1`                |
| Ctrl combos | `ctrl-c`, `ctrl-f`, `ctrl-e` |
| Named keys  | `enter`, `tab`               |

### Supported actions

| Action      | Syntax                 | Effect                                                                              |
| ----------- | ---------------------- | ----------------------------------------------------------------------------------- |
| **exit**    | `exit`                 | Gracefully stop the child and quit `keywrap`.                                       |
| **become**  | `become(<shell-cmd>)`  | Stop the child and **replace** the current process with `<shell-cmd>` via `execve`. |
| **execute** | `execute(<shell-cmd>)` | Run `<shell-cmd>` in the background; the child keeps running.                       |

---

## How it works

1. A PTY is allocated (`creack/pty`) and your command is started inside it.
2. The controlling terminal (`/dev/tty`) is switched to raw mode so we can read single keystrokes.
3. Keystrokes are matched against the user-supplied keymap:
   - If a mapping exists, the corresponding action is triggered.
   - Otherwise the key is forwarded transparently to the child.
4. When the child exits, `keywrap` either quits or waits (`--hold`) depending on flags.

---

## License

MIT
