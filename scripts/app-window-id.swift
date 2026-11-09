// Prints the CoreGraphics window number of an app's main on-screen window
// (argument: the owning app's name), for `screencapture -l`.
import CoreGraphics
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as! [[String: Any]]
for w in list where (w[kCGWindowOwnerName as String] as? String) == CommandLine.arguments[1] && (w[kCGWindowLayer as String] as? Int) == 0 {
  print(w[kCGWindowNumber as String]!); break
}
