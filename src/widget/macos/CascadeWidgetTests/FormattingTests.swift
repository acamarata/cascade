import XCTest

final class FormattingTests: XCTestCase {
    func testSingleAccountLabelsPerProvider() {
        let rows = labelledAccounts(from: [
            account("claude", provider: "claude"),
            account("codex", provider: "codex"),
            account("gemini", provider: "gemini"),
            account("opencode", provider: "opencode"),
            account("zai", provider: "zai"),
            account("gfp", provider: "gfp"),
        ])

        XCTAssertEqual(rows.map { $0.label }, ["An", "Co", "GP", "Oc", "Za", "GF"])
    }

    func testGlmAliasUsesZaiLabel() {
        let rows = labelledAccounts(from: [
            account("glm", provider: "glm"),
        ])

        XCTAssertEqual(rows.map { $0.label }, ["Za"])
    }

    func testMultiAccountNumberingUsesSuffixesWhenAllHaveSuffixes() {
        let rows = labelledAccounts(from: [
            account("claude-acc1", provider: "claude"),
            account("claude-acc2", provider: "claude"),
            account("zai-acc1", provider: "zai"),
            account("zai-acc2", provider: "zai"),
        ])

        XCTAssertEqual(rows.map { $0.label }, ["A1", "A2", "Z1", "Z2"])
    }

    func testMixedSuffixGroupUsesPositionalNumbering() {
        let rows = labelledAccounts(from: [
            account("claude", provider: "claude"),
            account("claude-acc1", provider: "claude"),
        ])

        XCTAssertEqual(rows.map { $0.label }, ["A1", "A2"])
    }

    func testUnknownProviderAppendedAfterKnownProviders() {
        let rows = labelledAccounts(from: [
            account("new-provider", provider: "newprovider"),
            account("claude", provider: "claude"),
        ])

        XCTAssertEqual(rows.map { $0.label }, ["An", "Nx"])
        XCTAssertEqual(rows.map { $0.entry.provider ?? "" }, ["claude", "newprovider"])
    }

    func testShouldAutoHideOnlyHidesUnusedClaudeRows() {
        XCTAssertTrue(shouldAutoHide(account("claude-empty", provider: "claude", usage: nil, lastPull: nil)))
        XCTAssertFalse(shouldAutoHide(account("claude-seen", provider: "claude", usage: nil, lastPull: 1)))
        XCTAssertFalse(shouldAutoHide(account("codex-empty", provider: "codex", usage: nil, lastPull: nil)))
        XCTAssertFalse(shouldAutoHide(account("claude-used", provider: "claude", usage: usage(), lastPull: nil)))
    }

    private func account(
        _ id: String,
        provider: String?,
        usage: UsageBlock? = nil,
        lastPull: Double? = 1,
        status: String? = "ok",
        authenticated: Bool? = true
    ) -> AccountEntry {
        AccountEntry(
            account: id,
            provider: provider,
            email: nil,
            usage: usage,
            last_pull_at: lastPull,
            last_error: nil,
            back_off_until: nil,
            quota_opaque: nil,
            plan_type: nil,
            status: status,
            authenticated: authenticated,
            key_count: nil,
            opencode_meta: nil,
            config_dir: nil
        )
    }

    private func usage() -> UsageBlock {
        UsageBlock(
            five_hour: QuotaSlot(utilization: 1, resets_at: nil, resets_in: nil, status: nil),
            seven_day: nil,
            seven_day_sonnet: nil,
            seven_day_opus: nil,
            extra_usage: nil
        )
    }
}
