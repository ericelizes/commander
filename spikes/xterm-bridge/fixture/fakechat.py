#!/usr/bin/env python3
"""Stand-in for the Claude Code chat pane.

Raw mode; turns on bracketed paste and SGR mouse reporting (like Claude Code /
any full-screen TUI does), prints a 24-bit colour + unicode banner, then echoes
every input chunk as `IN <repr>` so a test can see exactly which bytes arrived.
On SIGWINCH it prints `SIZE <cols>x<rows>`.
Usage: fakechat.py [--mouse]
"""
import os, sys, tty, termios, signal, shutil

MOUSE = '--mouse' in sys.argv


def size(*_):
    c, r = shutil.get_terminal_size()
    os.write(1, f'SIZE {c}x{r}\r\n'.encode())


def main():
    fd = 0
    old = termios.tcgetattr(fd)
    tty.setraw(fd)
    out = '\x1b[?2004h'  # bracketed paste on
    if MOUSE:
        out += '\x1b[?1000h\x1b[?1006h'  # mouse click/wheel, SGR encoding
    out += '\x1b[38;2;255;100;0mTRUECOLOR-orange\x1b[0m \x1b[48;2;0;80;200mBG-blue\x1b[0m '
    out += 'unicode: héllo ✓ 日本語 🚀\r\n'
    os.write(1, out.encode())
    size()
    signal.signal(signal.SIGWINCH, size)
    try:
        while True:
            try:
                b = os.read(fd, 4096)
            except InterruptedError:
                continue
            if not b or b == b'\x1d':  # Ctrl-] quits
                break
            os.write(1, f'IN {b!r}\r\n'.encode())
            if b == b'spew\r':  # history to scroll back through
                os.write(1, ''.join(f'line {i:03d}\r\n' for i in range(300)).encode())
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)


main()
