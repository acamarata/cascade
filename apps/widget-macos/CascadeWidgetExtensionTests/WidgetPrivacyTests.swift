import SwiftUI
import WidgetKit
import XCTest

// Privacy: every rendered string is a string from the snapshot file, a display form of a number
// from the file, or a fixed UI literal. The allowed set is built from the raw bytes with JSONSerialization,
// independent of WidgetModels.
final class WidgetPrivacyTests: XCTestCase {
    private static let families: [WidgetFamily] = [.systemSmall, .systemMedium, .systemLarge]

    static func allowedStrings(in data: Data) throws -> Set<String> {
        var allowed = WidgetLiteral.all
        func visit(_ value: Any) {
            switch value {
            case let text as String: allowed.insert(text)
            case let number as NSNumber where CFGetTypeID(number) != CFBooleanGetTypeID():
                allowed.insert(number.stringValue)
                allowed.insert(Formatting.percent(number.doubleValue))
            case let array as [Any]: array.forEach(visit)
            case let object as [String: Any]: object.values.forEach(visit)
            default: break
            }
        }
        visit(try JSONSerialization.jsonObject(with: data))
        return allowed
    }

    static func texts(_ content: WidgetContent, _ family: WidgetFamily) -> [String] {
        switch family {
        case .systemMedium: return content.mediumTexts
        case .systemLarge: return content.largeTexts
        default: return content.smallTexts
        }
    }

    // Collects every String stored anywhere in a view value tree: Text verbatim storage, stored subviews and the
    // bodies of the widget's own view types (primitive bodies are never called). A superset of the rendered
    // text, so the subset check is conservative.
    static func reflectedStrings(_ value: Any) -> Set<String> {
        var found = Set<String>()
        var seen = Set<ObjectIdentifier>()
        func walk(_ value: Any, _ depth: Int) {
            guard depth < 64 else { return }
            if let text = value as? String { found.insert(text); return }
            if let view = value as? any View, ownViewTypes.contains(ObjectIdentifier(type(of: view))) {
                walk(body(of: view), depth + 1)
            }
            let mirror = Mirror(reflecting: value)
            if mirror.displayStyle == .class {
                let id = ObjectIdentifier(value as AnyObject)
                guard seen.insert(id).inserted else { return }
            }
            for child in mirror.children { walk(child.value, depth + 1) }
        }
        walk(value, 0)
        return found
    }

    private static let ownViewTypes: Set<ObjectIdentifier> = [
        ObjectIdentifier(SmallWidgetView.self), ObjectIdentifier(MediumWidgetView.self),
        ObjectIdentifier(LargeWidgetView.self), ObjectIdentifier(WidgetRowLine.self), ObjectIdentifier(ProjectLine.self),
    ]

    private static func body<V: View>(of view: V) -> Any { view.body }

    func testRenderedTextIsASubsetOfFileStringsForEveryFamily() throws {
        for name in ["snapshot_seven_projects", "snapshot_no_projects"] {
            let data = try WidgetProviderTests.fixture(name)
            let allowed = try Self.allowedStrings(in: data)
            let entry = CascadeEntry(date: Date(), snapshot: try WidgetSnapshot.decode(data))
            for family in Self.families {
                let texts = Self.texts(WidgetContent(entry.snapshot), family)
                XCTAssertFalse(texts.isEmpty)
                XCTAssertEqual(Set(texts).subtracting(allowed), [], "\(name) \(family) content")
                let view = WidgetLayouts.make(family, entry: entry)
                let reflected = Self.reflectedStrings(view)
                XCTAssertTrue(Set(texts).isSubset(of: reflected), "\(name) \(family): walk missed rendered text")
                XCTAssertEqual(reflected.subtracting(allowed), [], "\(name) \(family) view tree")
            }
        }
    }

    func testLabelAbsentFromTheFileNeverAppears() throws {
        let original = String(decoding: try WidgetProviderTests.fixture("snapshot_seven_projects"), as: UTF8.self)
        let redacted = original.replacingOccurrences(of: "\"label\": \"acct-a-pool\"", with: "\"label\": \"redacted\"")
            .replacingOccurrences(of: "\"label\": \"project-a\"", with: "\"label\": \"redacted\"")
        XCTAssertNotEqual(redacted, original)
        let data = Data(redacted.utf8)
        let allowed = try Self.allowedStrings(in: data)
        let entry = CascadeEntry(date: Date(), snapshot: try WidgetSnapshot.decode(data))
        for family in Self.families {
            let texts = Self.texts(WidgetContent(entry.snapshot), family)
            let reflected = Self.reflectedStrings(WidgetLayouts.make(family, entry: entry))
            for rendered in [Set(texts), reflected] {
                XCTAssertFalse(rendered.contains("acct-a-pool"), "\(family) resolved a redacted row label")
                XCTAssertFalse(rendered.contains("project-a"), "\(family) resolved a redacted project label")
                XCTAssertFalse(rendered.contains { $0.hasPrefix("ref-") }, "\(family) rendered a row ref")
                XCTAssertEqual(rendered.subtracting(allowed), [], "\(family)")
            }
            XCTAssertTrue(texts.contains("redacted"), "\(family) dropped the redacted label")
        }
    }

    func testNoDataEntryRendersOnlyFixedLiterals() {
        let entry = CascadeEntry(date: Date(), snapshot: nil)
        for family in Self.families {
            let texts = Set(Self.texts(WidgetContent(nil), family))
            XCTAssertTrue(texts.isSubset(of: WidgetLiteral.all), "\(family)")
            XCTAssertTrue(Self.reflectedStrings(WidgetLayouts.make(family, entry: entry)).isSubset(of: WidgetLiteral.all))
        }
    }
}
