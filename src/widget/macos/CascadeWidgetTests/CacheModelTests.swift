import XCTest
@testable import CascadeWidgetExtension

// MARK: - CacheModelTests
//
// Purpose: Verify CacheModel decoding, age calculation, staleness threshold, and placeholder.
// Inputs: JSON fixtures + Date-based age calculation
// Outputs: Decoded structs with verified field values and threshold boundary conditions
// Constraints: 120s isStale threshold; ageString formats (Ns ago, Nm ago); placeholder non-nil
// Test harness: XCTest (macOS 14.0+)

class CacheModelTests: XCTestCase {

    // MARK: - testDecodeFullFixture
    /// Decode a full cache.json matching P2 schema.
    /// Verifies all top-level and nested fields populate correctly.
    func testDecodeFullFixture() throws {
        let jsonString = """
        {
          "generated_at": 1748700000,
          "daemon_version": "0.1.0",
          "_widget": {
            "cascade_tiers": {
              "gci": {"rules": 14, "references": 44, "memory": 15, "skills": 22, "agents": 6},
              "asi": {"rules": 3, "references": 8, "memory": 4, "skills": 2, "agents": 1}
            },
            "active_project": "cascade",
            "active_phase": "P2",
            "projects": [{"label": "cascade", "active": 3, "ready": 1, "planned": 12,
                          "review": 0, "blocked": 0, "done": 7, "inbox": 2, "ideas": 5, "memory": 4}],
            "totals": {"active": 3, "ready": 1, "planned": 12, "review": 0, "blocked": 0, "inbox": 2, "ideas": 5},
            "inbox_unread": 2,
            "ideas_count": 5
          }
        }
        """

        let data = jsonString.data(using: .utf8)!
        let cache = try JSONDecoder().decode(CascadeCache.self, from: data)

        // Assert top-level fields
        XCTAssertEqual(cache.generated_at, 1748700000)
        XCTAssertEqual(cache.daemon_version, "0.1.0")

        // Assert _widget presence and nested fields
        XCTAssertNotNil(cache._widget)
        XCTAssertEqual(cache._widget?.active_project, "cascade")
        XCTAssertEqual(cache._widget?.active_phase, "P2")
        XCTAssertEqual(cache._widget?.inbox_unread, 2)
        XCTAssertEqual(cache._widget?.ideas_count, 5)

        // Assert cascade_tiers.gci values
        XCTAssertNotNil(cache._widget?.cascade_tiers?.gci)
        XCTAssertEqual(cache._widget?.cascade_tiers?.gci?.rules, 14)
        XCTAssertEqual(cache._widget?.cascade_tiers?.gci?.references, 44)
        XCTAssertEqual(cache._widget?.cascade_tiers?.gci?.memory, 15)
        XCTAssertEqual(cache._widget?.cascade_tiers?.gci?.skills, 22)
        XCTAssertEqual(cache._widget?.cascade_tiers?.gci?.agents, 6)

        // Assert cascade_tiers.asi values
        XCTAssertNotNil(cache._widget?.cascade_tiers?.asi)
        XCTAssertEqual(cache._widget?.cascade_tiers?.asi?.rules, 3)
        XCTAssertEqual(cache._widget?.cascade_tiers?.asi?.references, 8)
    }

    // MARK: - testDecodeMinimalFixture
    /// Decode JSON with only generated_at.
    /// Verifies graceful handling when Optional fields are absent.
    func testDecodeMinimalFixture() throws {
        let jsonString = """
        {
          "generated_at": 1748700000
        }
        """

        let data = jsonString.data(using: .utf8)!
        let cache = try JSONDecoder().decode(CascadeCache.self, from: data)

        // Assert minimal required field
        XCTAssertEqual(cache.generated_at, 1748700000)

        // Assert Optional fields are nil
        XCTAssertNil(cache._widget)
        XCTAssertNil(cache.daemon_version)
    }

    // MARK: - testAgeString
    /// Verify ageString formatting for seconds.
    /// generated_at = now - 45 seconds → ageString == "45s ago"
    func testAgeString() throws {
        let now = Int(Date().timeIntervalSince1970)
        let cache = CascadeCache(
            _widget: nil,
            generated_at: now - 45,
            daemon_version: nil
        )

        XCTAssertEqual(cache.ageString, "45s ago")
    }

    // MARK: - testAgeStringMinutes
    /// Verify ageString formatting for minutes.
    /// generated_at = now - 90 seconds → ageString == "1m ago"
    func testAgeStringMinutes() throws {
        let now = Int(Date().timeIntervalSince1970)
        let cache = CascadeCache(
            _widget: nil,
            generated_at: now - 90,
            daemon_version: nil
        )

        XCTAssertEqual(cache.ageString, "1m ago")
    }

    // MARK: - testIsStaleTrue
    /// Verify isStale returns true when age > 120 seconds.
    /// generated_at = now - 121 seconds → isStale == true
    func testIsStaleTrue() throws {
        let now = Int(Date().timeIntervalSince1970)
        let cache = CascadeCache(
            _widget: nil,
            generated_at: now - 121,
            daemon_version: nil
        )

        XCTAssertTrue(cache.isStale)
    }

    // MARK: - testIsStaleJustUnder
    /// Verify isStale returns false at the 120s boundary (just under).
    /// generated_at = now - 119 seconds → isStale == false
    /// This test confirms the 120s threshold is correctly enforced.
    func testIsStaleJustUnder() throws {
        let now = Int(Date().timeIntervalSince1970)
        let cache = CascadeCache(
            _widget: nil,
            generated_at: now - 119,
            daemon_version: nil
        )

        XCTAssertFalse(cache.isStale)
    }

    // MARK: - testPlaceholderNonNil
    /// Verify CascadeCache.placeholder._widget?.cascade_tiers?.gci is non-nil.
    /// Confirms the placeholder provides realistic test data for the widget.
    func testPlaceholderNonNil() throws {
        let placeholder = CascadeCache.placeholder

        XCTAssertNotNil(placeholder._widget)
        XCTAssertNotNil(placeholder._widget?.cascade_tiers)
        XCTAssertNotNil(placeholder._widget?.cascade_tiers?.gci)
    }

    // MARK: - testPlaceholderActiveProject
    /// Verify CascadeCache.placeholder._widget?.active_project is non-nil.
    /// Confirms the placeholder provides a valid active project value.
    func testPlaceholderActiveProject() throws {
        let placeholder = CascadeCache.placeholder

        XCTAssertNotNil(placeholder._widget?.active_project)
        XCTAssertEqual(placeholder._widget?.active_project, "cascade")
    }
}
