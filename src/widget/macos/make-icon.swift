// make-icon.swift — render the Cascade app icon (mountain on a blue gradient) to PNG.
// Usage: swift make-icon.swift <output.png> <size>
import AppKit

let args = CommandLine.arguments
let outPath = args.count > 1 ? args[1] : "icon.png"
let size = args.count > 2 ? Int(args[2]) ?? 1024 : 1024

let dim = CGFloat(size)
let image = NSImage(size: NSSize(width: dim, height: dim))
image.lockFocus()

// Rounded-rect gradient background (macOS app-icon style, slight inset).
let inset = dim * 0.06
let rect = NSRect(x: inset, y: inset, width: dim - 2 * inset, height: dim - 2 * inset)
let radius = (dim - 2 * inset) * 0.225
let path = NSBezierPath(roundedRect: rect, xRadius: radius, yRadius: radius)
let gradient = NSGradient(colors: [
    NSColor(calibratedRed: 0.36, green: 0.62, blue: 0.95, alpha: 1.0), // top  #5B9EF2
    NSColor(calibratedRed: 0.17, green: 0.36, blue: 0.78, alpha: 1.0), // bot  #2C5CC7
])!
gradient.draw(in: path, angle: -90)

// Mountain glyph, white, centered.
let cfg = NSImage.SymbolConfiguration(pointSize: dim * 0.5, weight: .semibold)
if let sym = NSImage(systemSymbolName: "mountain.2.fill", accessibilityDescription: "Cascade")?
    .withSymbolConfiguration(cfg) {
    let tinted = NSImage(size: sym.size)
    tinted.lockFocus()
    NSColor.white.set()
    let r = NSRect(origin: .zero, size: sym.size)
    sym.draw(in: r)
    r.fill(using: .sourceAtop)
    tinted.unlockFocus()
    let w = dim * 0.56, h = w * (sym.size.height / sym.size.width)
    tinted.draw(in: NSRect(x: (dim - w) / 2, y: (dim - h) / 2 - dim * 0.02, width: w, height: h),
                from: .zero, operation: .sourceOver, fraction: 1.0)
}

image.unlockFocus()

guard let tiff = image.tiffRepresentation,
      let rep = NSBitmapImageRep(data: tiff),
      let png = rep.representation(using: .png, properties: [:]) else {
    FileHandle.standardError.write("failed to render\n".data(using: .utf8)!)
    exit(1)
}
try? png.write(to: URL(fileURLWithPath: outPath))
print("wrote \(outPath) (\(size)px)")
