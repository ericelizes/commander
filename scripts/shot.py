import os, pty, sys, time, select, fcntl, termios, struct, signal, subprocess
import pyte
from PIL import Image, ImageDraw, ImageFont

# usage: shot.py [out.png] [window]   (window = tmux window index/name; default: what the user sees)
out = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser('~/.commander/screens/screen.png')
window = sys.argv[2] if len(sys.argv) > 2 else None
os.makedirs(os.path.dirname(out), exist_ok=True)
size = subprocess.check_output(['tmux', 'list-clients', '-t', 'commander', '-F', '#{client_width} #{client_height}']).decode().split('\n')[0].split()
cols, rows = int(size[0]), int(size[1])
target = 'commander'
group = None
if window is not None:
    # A grouped session has its own current window, so looking at another window
    # never changes what the user sees.
    group = 'shot-%d' % os.getpid()
    subprocess.check_call(['tmux', 'new-session', '-d', '-t', 'commander', '-s', group])
    subprocess.check_call(['tmux', 'select-window', '-t', '%s:%s' % (group, window)])
    target = group

pid, fd = pty.fork()
if pid == 0:
    os.environ['TERM'] = 'xterm-256color'
    os.environ.pop('TMUX', None)
    os.execvp('tmux', ['tmux', 'attach', '-t', target, '-f', 'read-only,ignore-size'])
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))

class S(pyte.Screen):
    def report_device_status(self, *a, **k):
        pass
    def report_device_attributes(self, *a, **k):
        pass
    def write_process_input(self, data):
        pass
screen = S(cols, rows)
stream = pyte.ByteStream(screen)
end = time.time() + 2.5
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            stream.feed(os.read(fd, 65536))
        except OSError:
            break
os.kill(pid, signal.SIGTERM)
if group:
    subprocess.call(['tmux', 'kill-session', '-t', group])

font = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', 14)
cw, ch = 9, 18
img = Image.new('RGB', (cols * cw, rows * ch), '#000000')
d = ImageDraw.Draw(img)

NAMED = {'black': '#000000', 'red': '#cd3131', 'green': '#0dbc79', 'brown': '#e5e510', 'blue': '#2472c8',
         'magenta': '#bc3fbc', 'cyan': '#11a8cd', 'white': '#e5e5e5'}

def col(c, default):
    if c == 'default':
        return default
    if c in NAMED:
        return NAMED[c]
    if len(c) == 6:
        return '#' + c
    return default

for y in range(rows):
    line = screen.buffer[y]
    for x in range(cols):
        cell = line[x]
        fg, bg = col(cell.fg, '#cdd6f4'), col(cell.bg, '#000000')
        if cell.reverse:
            fg, bg = bg, fg
        d.rectangle([x * cw, y * ch, (x + 1) * cw - 1, (y + 1) * ch - 1], fill=bg)
        if cell.data.strip():
            d.text((x * cw, y * ch), cell.data, font=font, fill=fg)
img.save(out)
print(out, cols, rows)
