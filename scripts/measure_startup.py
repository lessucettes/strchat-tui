#!/usr/bin/env python3
"""Measure a real terminal startup and /quit using an isolated configuration.

POSIX-only measurement harness; the application itself also builds for Windows.
No relays are contacted: each run starts with an empty temporary configuration.
"""
import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import resource
import select
import signal
import statistics
import struct
import subprocess
import tempfile
import termios
import time


def measure(binary, idle_seconds):
    with tempfile.TemporaryDirectory(prefix="strchat-startup-") as home:
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        env = dict(os.environ, TERM="xterm-256color", XDG_CONFIG_HOME=home, HOME=home)
        before = resource.getrusage(resource.RUSAGE_CHILDREN)
        started = time.perf_counter()
        def controlling_terminal():
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

        process = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=slave,
                                   env=env, preexec_fn=controlling_terminal)
        os.close(slave)
        screen = bytearray()
        ready_ms = None
        quit_at = None
        idle_cpu_start = None
        idle_cpu_ticks = None
        peak_rss_kib = 0
        deadline = started + 15 + idle_seconds
        try:
            while process.poll() is None and time.perf_counter() < deadline:
                now = time.perf_counter()
                # Linux procfs observations are optional; startup timing is POSIX.
                try:
                    status = Path(f"/proc/{process.pid}/status").read_text()
                    for line in status.splitlines():
                        if line.startswith("VmHWM:"):
                            peak_rss_kib = max(peak_rss_kib, int(line.split()[1]))
                    fields = Path(f"/proc/{process.pid}/stat").read_text().split()
                    ticks = int(fields[13]) + int(fields[14])
                except (OSError, ValueError, IndexError):
                    ticks = None
                if ready_ms is not None and idle_cpu_start is None:
                    idle_cpu_start = ticks
                if quit_at is not None and now >= quit_at:
                    idle_cpu_ticks = None if ticks is None or idle_cpu_start is None else ticks - idle_cpu_start
                    os.write(master, b"/quit\r")
                    quit_at = None
                readable, _, _ = select.select([master], [], [], 0.02)
                if readable:
                    try:
                        data = os.read(master, 65536)
                    except OSError as exc:
                        if exc.errno == errno.EIO:
                            break
                        raise
                    screen.extend(data)
                    # tcell may emit one escape sequence per cell. Strip those to
                    # find actual initial UI text instead of timing terminal setup.
                    import re
                    visible = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", screen)
                    if ready_ms is None and (b"/join" in visible or b"Alt+I" in visible):
                        ready_ms = (time.perf_counter() - started) * 1000
                        quit_at = time.perf_counter() + idle_seconds
                    if len(screen) > 1_000_000:
                        del screen[:-100_000]
            if process.poll() is None:
                try:
                    process.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                    raise RuntimeError("client did not start and exit via /quit within deadline")
            if process.returncode != 0 or ready_ms is None:
                raise RuntimeError(f"client failed: exit={process.returncode}, UI ready={ready_ms is not None}")
        finally:
            os.close(master)
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        after = resource.getrusage(resource.RUSAGE_CHILDREN)
        return {
            "ready_ms": round(ready_ms, 3),
            "total_ms": round((time.perf_counter() - started) * 1000, 3),
            "cpu_ms": round(1000 * (after.ru_utime + after.ru_stime - before.ru_utime - before.ru_stime), 3),
            "peak_rss_kib": peak_rss_kib or None,
            "idle_cpu_ms": None if idle_cpu_ticks is None else round(idle_cpu_ticks * 1000 / os.sysconf("SC_CLK_TCK"), 3),
        }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--runs", type=int, default=10)
    parser.add_argument("--idle-seconds", type=float, default=0.25)
    args = parser.parse_args()
    if args.runs < 1 or args.idle_seconds < 0:
        parser.error("runs must be positive and idle-seconds nonnegative")
    binary = str(Path(args.binary).resolve(strict=True))
    measurements = [measure(binary, args.idle_seconds) for _ in range(args.runs)]
    medians = {key: statistics.median(row[key] for row in measurements if row[key] is not None)
               for key in measurements[0] if any(row[key] is not None for row in measurements)}
    print(json.dumps({"binary": binary, "runs": measurements, "median": medians}, indent=2))


if __name__ == "__main__":
    main()
