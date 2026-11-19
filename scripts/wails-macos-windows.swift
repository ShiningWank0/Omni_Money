import CoreGraphics
import Foundation

guard CommandLine.arguments.count == 2, let target = Int(CommandLine.arguments[1]), target > 1 else {
    fatalError("Pass the owned application PID")
}
guard let windows = CGWindowListCopyWindowInfo(.optionAll, kCGNullWindowID) as? [[String: Any]] else {
    fatalError("Window inventory is unavailable")
}
// Window titles and other applications' metadata are never returned.
let owned = windows.compactMap { window -> [String: Any]? in
    guard let pid = window[kCGWindowOwnerPID as String] as? Int, pid == target,
          let layer = window[kCGWindowLayer as String] as? Int,
          let bounds = window[kCGWindowBounds as String] as? [String: Any] else { return nil }
    return ["pid": pid, "layer": layer, "width": bounds["Width"] ?? 0, "height": bounds["Height"] ?? 0]
}
let data = try JSONSerialization.data(withJSONObject: owned)
FileHandle.standardOutput.write(data)
