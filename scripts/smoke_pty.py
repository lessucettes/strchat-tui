#!/usr/bin/env python3
"""Real-terminal smoke test for the built client (POSIX controlling PTY).

The screen model below handles the escape sequences this client emits, but it
can lag a repaint of a single pane (verified against `tmux capture-pane`): treat
a passing marker as evidence that the client rendered it, and use tmux when a
pane's exact contents matter.
Unlike the simulation-screen tests, this drives the actual binary in a real
terminal: startup, typed input, slash commands and a clean exit. It is
deliberately hermetic: the isolated configuration lists only a dead loopback
relay (ws://127.0.0.1:1), so no public relay is contacted and nothing can be
published. A joined topic channel exercises the real code path that selects
relays, fails to connect and retries.

Output is interpreted with a small screen model (cursor addressing, erases and
carriage returns), because a full-screen TUI repaints cells and emits runs of
text without the spaces between them; matching the raw byte stream would miss
most on-screen text.

Usage:
    python3 scripts/smoke_pty.py ./strchat-tui
    python3 scripts/smoke_pty.py ./strchat-tui --cols 58 --rows 10
"""
import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time

# Cursor/erase/style sequences the client's terminal output uses. Intermediate
# bytes (for example the space in the cursor-shape sequence ESC[0 SP q) are
# matched and dropped so they never reach the screen model.
CSI = re.compile(rb"\x1b\[([0-9;:?<>=!]*)([ -/]*)([@-~])")
CHARSET = re.compile(rb"\x1b\([0-9A-Za-z]")
DEAD_RELAY = "ws://127.0.0.1:1"
PRIVATE_KEY = "0000000000000000000000000000000000000000000000000000000000000001"


class Screen:
    """Minimal terminal screen: handles CUP/CHA/VPA, cursor moves, erases."""

    def __init__(self, cols, rows):
        self.cols, self.rows = cols, rows
        self.cells = [[" "] * cols for _ in range(rows)]
        self.row, self.col = 0, 0

    def _put(self, ch):
        if 0 <= self.row < self.rows and 0 <= self.col < self.cols:
            self.cells[self.row][self.col] = ch
        self.col += 1

    def _csi(self, params, final):
        args = [int(p) for p in params.split(b";") if p.isdigit()]
        first = args[0] if args else 0
        match final:
            case "H" | "f":
                self.row = max(0, (args[0] if args else 1) - 1)
                self.col = max(0, (args[1] if len(args) > 1 else 1) - 1)
                self.row, self.col = min(self.row, self.rows - 1), min(self.col, self.cols - 1)
            case "G":
                self.col = min(max(0, (first or 1) - 1), self.cols - 1)
            case "d":
                self.row = min(max(0, (first or 1) - 1), self.rows - 1)
            case "A":
                self.row = max(0, self.row - (first or 1))
            case "B":
                self.row = min(self.rows - 1, self.row + (first or 1))
            case "C":
                self.col = min(self.cols - 1, self.col + (first or 1))
            case "D":
                self.col = max(0, self.col - (first or 1))
            case "J":
                if first == 2:
                    self.cells = [[" "] * self.cols for _ in range(self.rows)]
                elif first == 0:  # erase below
                    for c in range(self.col, self.cols):
                        self.cells[self.row][c] = " "
                    for r in range(self.row + 1, self.rows):
                        self.cells[r] = [" "] * self.cols
            case "K":
                for c in range(self.col, self.cols):
                    self.cells[self.row][c] = " "

    def feed(self, data):
        data = CHARSET.sub(b"", data)
        pos = 0
        while pos < len(data):
            byte = data[pos]
            if byte == 0x1B:
                match = CSI.match(data, pos)
                if match:
                    self._csi(match.group(1), match.group(3).decode())
                    pos = match.end()
                    continue
                pos += 1  # unknown escape: skip it
                continue
            if byte == 0x0A:
                self.row = min(self.rows - 1, self.row + 1)
                pos += 1
                continue
            if byte == 0x0D:
                self.col = 0
                pos += 1
                continue
            if byte in (0x08, 0x07):
                self.col = max(0, self.col - 1) if byte == 0x08 else self.col
                pos += 1
                continue
            # Multi-byte UTF-8: decode the next rune.
            length = 1
            if byte & 0xE0 == 0xC0:
                length = 2
            elif byte & 0xF0 == 0xE0:
                length = 3
            elif byte & 0xF8 == 0xF0:
                length = 4
            chunk = data[pos:pos + length].decode("utf-8", "replace")
            self._put(chunk)
            pos += length

    def text(self):
        return "\n".join("".join(row).rstrip() for row in self.cells)


class Terminal:
    def __init__(self, binary, cols, rows, home):
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        env = dict(os.environ, TERM="xterm-256color", XDG_CONFIG_HOME=home, HOME=home)

        def controlling_terminal():
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

        self.process = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=slave,
                                        env=env, preexec_fn=controlling_terminal)
        os.close(slave)
        self.screen = Screen(cols, rows)
        self.bytes_read = 0
        self.steps = []

    def pump(self, seconds):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                break
            readable, _, _ = select.select([self.master], [], [], 0.05)
            if not readable:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError as exc:
                if exc.errno == errno.EIO:
                    break
                raise
            self.bytes_read += len(data)
            self.screen.feed(data)

    def wait_for(self, needle, timeout, what):
        deadline = time.monotonic() + timeout
        while True:
            if needle in self.screen.text():
                self.steps.append({"step": what, "ok": True})
                return True
            if time.monotonic() >= deadline or self.process.poll() is not None:
                self.steps.append({"step": what, "ok": False})
                return False
            self.pump(0.2)

    def send(self, text):
        os.write(self.master, text.encode())

    def finish(self, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline and self.process.poll() is None:
            self.pump(0.2)
        if self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGKILL)
            self.process.wait()
            return None
        return self.process.returncode

    def close(self):
        os.close(self.master)
        if self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGKILL)
            self.process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--chat", default="smoketest",
                        help="chat to join; a geohash exercises the bundled relay catalogs")
    parser.add_argument("--timeout", type=float, default=15.0)
    parser.add_argument("--cols", type=int, default=120)
    parser.add_argument("--rows", type=int, default=32)
    parser.add_argument("--help-expect", default="COMMANDS:",
                        help="text expected on screen after /help; only the last pane-full of lines stays "
                             "visible, so small terminals need the tail of the help text")
    parser.add_argument("--send-expect", default="not queued",
                        help="text expected after sending while the only relay is dead; leave empty on "
                             "layouts where that line does not fit on screen, which then checks instead "
                             "that no phantom success is shown")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve(strict=True))

    with tempfile.TemporaryDirectory(prefix="strchat-smoke-") as home:
        config_dir = Path(home) / "strchat-tui"
        config_dir.mkdir(parents=True)
        # anchor_relays keeps every connection attempt on the dead loopback relay:
        # no public relay is contacted, so this test touches no real network.
        (config_dir / "config.json").write_text(json.dumps({
            "private_key": PRIVATE_KEY,
            "nick": "smoke",
            "views": [],
            "active_view_name": "",
            "anchor_relays": [DEAD_RELAY],
        }, indent=2))

        term = Terminal(binary, args.cols, args.rows, home)
        result = {"binary": binary, "cols": args.cols, "rows": args.rows}
        try:
            if not term.wait_for("Alt+I", args.timeout, "startup_ready"):
                raise AssertionError("UI never became ready")
            term.send("/help\r")
            if not term.wait_for(args.help_expect, args.timeout, "help_rendered"):
                raise AssertionError("/help output not rendered")
            for name in ("blue-gray", "red-gold", "monochrome", "default"):
                term.send("/theme %s\r" % name)
                if not term.wait_for("Theme: " + name, args.timeout, "theme_" + name):
                    raise AssertionError("theme switch not rendered: " + name)
            term.send("/join %s\r" % args.chat)
            # Asking the client which chat is active proves the join reached the
            # client; the answer lands in the message pane, which every layout
            # shows (the chat list is hidden on tiny terminals).
            term.send("/set\r")
            if not term.wait_for("Current active chat/group is: %s" % args.chat, args.timeout, "channel_joined"):
                raise AssertionError("joined channel is not the active chat")
            if args.send_expect:
                # The only configured relay is dead: the client must refuse the
                # send visibly instead of pretending the message went out.
                term.send("hello from a real terminal\r")
                if not term.wait_for(args.send_expect, args.timeout, "offline_send_refused"):
                    raise AssertionError("offline send was not refused visibly")
            else:
                # Short or tiny layouts cannot show the whole log line, so check
                # the contract that matters: no phantom send success.
                term.send("hello from a real terminal\r")
                term.pump(2.0)
                if "hello from a real terminal" in term.screen.text():
                    raise AssertionError("message was echoed although no relay accepted it")
                if term.process.poll() is not None:
                    raise AssertionError("client exited while sending to a dead relay")
                term.steps.append({"step": "no_phantom_send_success", "ok": True})
            term.send("history draft marker")
            if not term.wait_for("history draft marker", args.timeout, "history_draft_typed"):
                raise AssertionError("history draft not rendered")
            term.send("\x10")  # Ctrl+P: recall the last attempted message.
            term.pump(0.2)
            if not term.wait_for("hello from a real terminal", args.timeout, "history_previous"):
                raise AssertionError("Ctrl+P did not recall the full input")
            term.send("\x0e")  # Ctrl+N: restore the draft, without sending it.
            term.pump(0.2)
            if not term.wait_for("history draft marker", args.timeout, "history_draft_restored"):
                raise AssertionError("Ctrl+N did not restore the draft")
            term.send("\x15/quit\r")  # Ctrl+U clears the restored draft.
            code = term.finish(args.timeout)
            result["exit_code"] = code
            if code != 0:
                raise AssertionError(f"exit code {code!r} after /quit")
        except AssertionError as exc:
            result["error"] = str(exc)
            result["screen"] = term.screen.text()
        finally:
            result["steps"] = term.steps
            result["bytes_read"] = term.bytes_read
            term.close()

    print(json.dumps(result, indent=2))
    return 1 if result.get("error") else 0


if __name__ == "__main__":
    sys.exit(main())
