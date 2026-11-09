// ansi2png: render ANSI-colored terminal text (e.g. `tmux capture-pane -p -e`)
// to a PNG so colors and contrast can be inspected without a screen capture.
//
//   swiftc -O scripts/ansi2png.swift -o /tmp/ansi2png
//   tmux capture-pane -p -e -t %1 | /tmp/ansi2png --out /tmp/pane.png --bg '#181825' --fg '#cdd6f4'
//
// --bg / --fg are the pane's default colors (tmux window-style is not part of
// captured cells, so cells with the default background are painted with --bg).
import AppKit
import Foundation

var outPath = "/tmp/ansi.png"
var defBG = NSColor(srgbRed: 0x18/255, green: 0x18/255, blue: 0x25/255, alpha: 1)
var defFG = NSColor(srgbRed: 0xcd/255, green: 0xd6/255, blue: 0xf4/255, alpha: 1)
var fontSize: CGFloat = 14
var minCols = 0

func hex(_ s: String) -> NSColor? {
    var h = s; if h.hasPrefix("#") { h.removeFirst() }
    guard h.count == 6, let v = Int(h, radix: 16) else { return nil }
    return NSColor(srgbRed: CGFloat((v >> 16) & 255)/255, green: CGFloat((v >> 8) & 255)/255, blue: CGFloat(v & 255)/255, alpha: 1)
}
var args = Array(CommandLine.arguments.dropFirst())
while !args.isEmpty {
    let a = args.removeFirst()
    switch a {
    case "--out": outPath = args.removeFirst()
    case "--bg": if let c = hex(args.removeFirst()) { defBG = c }
    case "--fg": if let c = hex(args.removeFirst()) { defFG = c }
    case "--size": fontSize = CGFloat(Double(args.removeFirst()) ?? 14)
    case "--cols": minCols = Int(args.removeFirst()) ?? 0
    default: break
    }
}

// 256-color palette.
func xterm(_ n: Int) -> NSColor {
    let base: [(Int, Int, Int)] = [(0,0,0),(205,49,49),(13,188,121),(229,229,16),(36,114,200),(188,63,188),(17,168,205),(229,229,229),
                                   (102,102,102),(241,76,76),(35,209,139),(245,245,67),(59,142,234),(214,112,214),(41,184,219),(255,255,255)]
    func c(_ r: Int, _ g: Int, _ b: Int) -> NSColor { NSColor(srgbRed: CGFloat(r)/255, green: CGFloat(g)/255, blue: CGFloat(b)/255, alpha: 1) }
    if n < 16 { let t = base[n]; return c(t.0, t.1, t.2) }
    if n < 232 { let i = n - 16; let lv = [0, 95, 135, 175, 215, 255]; return c(lv[i/36], lv[(i/6)%6], lv[i%6]) }
    let g = 8 + (n - 232) * 10; return c(g, g, g)
}

struct Cell { var ch: Character = " "; var fg: NSColor? ; var bg: NSColor?; var bold = false; var italic = false; var underline = false; var reverse = false }

let input = String(decoding: FileHandle.standardInput.readDataToEndOfFile(), as: UTF8.self)
var rows: [[Cell]] = []
var fg: NSColor? = nil, bg: NSColor? = nil
var bold = false, italic = false, underline = false, reverse = false, faint = false

func applySGR(_ params: [Int]) {
    var i = 0
    let p = params.isEmpty ? [0] : params
    while i < p.count {
        let v = p[i]
        switch v {
        case 0: fg = nil; bg = nil; bold = false; italic = false; underline = false; reverse = false; faint = false
        case 1: bold = true
        case 2: faint = true
        case 3: italic = true
        case 4: underline = true
        case 7: reverse = true
        case 22: bold = false; faint = false
        case 23: italic = false
        case 24: underline = false
        case 27: reverse = false
        case 30...37: fg = xterm(v - 30)
        case 90...97: fg = xterm(v - 90 + 8)
        case 39: fg = nil
        case 40...47: bg = xterm(v - 40)
        case 100...107: bg = xterm(v - 100 + 8)
        case 49: bg = nil
        case 38, 48:
            if i + 1 < p.count, p[i+1] == 2, i + 4 < p.count {
                let col = NSColor(srgbRed: CGFloat(p[i+2])/255, green: CGFloat(p[i+3])/255, blue: CGFloat(p[i+4])/255, alpha: 1)
                if v == 38 { fg = col } else { bg = col }
                i += 4
            } else if i + 1 < p.count, p[i+1] == 5, i + 2 < p.count {
                if v == 38 { fg = xterm(p[i+2]) } else { bg = xterm(p[i+2]) }
                i += 2
            }
        default: break
        }
        i += 1
    }
}

for line in input.split(separator: "\n", omittingEmptySubsequences: false) {
    var row: [Cell] = []
    let chars = Array(line.unicodeScalars)
    var k = 0
    while k < chars.count {
        let s = chars[k]
        if s == "\u{1b}", k + 1 < chars.count, chars[k+1] == "[" {
            var j = k + 2; var body = ""
            while j < chars.count, !(chars[j].value >= 0x40 && chars[j].value <= 0x7e) { body.unicodeScalars.append(chars[j]); j += 1 }
            if j < chars.count, chars[j] == "m" {
                applySGR(body.split(separator: ";", omittingEmptySubsequences: false).map { Int($0) ?? 0 })
            }
            k = j + 1; continue
        }
        if s.value < 0x20 { k += 1; continue }
        var c = Cell(); c.ch = Character(s); c.fg = fg; c.bg = bg; c.bold = bold; c.italic = italic; c.underline = underline; c.reverse = reverse
        if faint, c.fg == nil { c.fg = defFG.withAlphaComponent(0.5) }
        row.append(c); k += 1
    }
    rows.append(row)
}
while let last = rows.last, last.isEmpty, rows.count > 1 { rows.removeLast() }

func font(_ bold: Bool, _ italic: Bool) -> NSFont {
    for name in bold ? ["JetBrainsMonoNFM-Bold", "JetBrainsMono Nerd Font Mono Bold", "JetBrainsMonoNerdFont-Bold", "Menlo-Bold"] : ["JetBrainsMonoNFM-Regular", "JetBrainsMono Nerd Font Mono", "JetBrainsMonoNerdFont-Regular", "Menlo-Regular"] {
        if let f = NSFont(name: name, size: fontSize) { return f }
    }
    return NSFont.monospacedSystemFont(ofSize: fontSize, weight: bold ? .bold : .regular)
}
let regular = font(false, false), boldF = font(true, false)
let cw = ("M" as NSString).size(withAttributes: [.font: regular]).width.rounded(.up)
let ch = (regular.ascender - regular.descender + regular.leading).rounded(.up) + 2
let cols = max(minCols, rows.map { $0.count }.max() ?? 1)
let W = Int(CGFloat(cols) * cw), H = Int(CGFloat(rows.count) * ch)

let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: W, pixelsHigh: H, bitsPerSample: 8, samplesPerPixel: 4,
                           hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
let ctx = NSGraphicsContext(bitmapImageRep: rep)!
NSGraphicsContext.saveGraphicsState(); NSGraphicsContext.current = ctx
defBG.setFill(); NSRect(x: 0, y: 0, width: W, height: H).fill()
for (y, row) in rows.enumerated() {
    for (x, cell) in row.enumerated() {
        var f = cell.fg ?? defFG, b = cell.bg ?? defBG
        if cell.reverse { swap(&f, &b) }
        let rect = NSRect(x: CGFloat(x) * cw, y: CGFloat(H) - CGFloat(y + 1) * ch, width: cw, height: ch)
        if cell.bg != nil || cell.reverse { b.setFill(); rect.fill() }
        if cell.ch != " " {
            let attrs: [NSAttributedString.Key: Any] = [.font: cell.bold ? boldF : regular, .foregroundColor: f,
                                                        .underlineStyle: cell.underline ? NSUnderlineStyle.single.rawValue : 0]
            (String(cell.ch) as NSString).draw(at: NSPoint(x: rect.minX, y: rect.minY + 1), withAttributes: attrs)
        }
    }
}
NSGraphicsContext.restoreGraphicsState()
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: outPath))
print("wrote \(outPath) \(W)x\(H) (\(cols)x\(rows.count) cells)")
