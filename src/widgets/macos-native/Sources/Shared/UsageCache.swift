import Foundation

// MARK: - Cache models

/// Top-level structure of ~/.claude/usage-cache.json.
struct UsageCache: Codable {
    var accounts: [AccountEntry]
    var queried_at: Double?
    var sanity: SanityBlock?
}

struct SanityBlock: Codable {
    var checked_at: Double?
    var ok: Bool?
    /// The sanity daemon writes each anomaly as `{account, kind, detail}`.
    /// Earlier versions of this model declared `[String]?` which fails the
    /// whole-cache decode the moment any anomaly is recorded.
    var anomalies: [SanityAnomaly]?
}

struct SanityAnomaly: Codable {
    var account: String?
    var kind: String?
    var detail: String?
}

struct AccountEntry: Codable, Identifiable {
    var id: String { account }
    var account: String
    var provider: String?
    var email: String?
    var usage: UsageBlock?
    var last_pull_at: Double?
    var last_error: String?
    var back_off_until: Double?
    var quota_opaque: Bool?
    var plan_type: String?
    /// Per-row status string written by gemini and opencode probes
    /// ("ok", "no_auth", "auth_expired", "throttled", "api_error",
    /// "network_error"). Older Claude/Codex rows may omit it.
    var status: String?
    /// OpenCode-only metadata block. Carries the published Go-plan dollar
    /// limits, computed dollar spend per window, Zen balance state, and
    /// subscription IDs. Lets the widget render dollar context (e.g.
    /// "$30 / $30") and surface the unblock path (top up + enable
    /// useBalance) without re-scraping. Nil for all non-opencode rows.
    var opencode_meta: OpencodeMeta?
}

struct OpencodeMeta: Codable {
    var balance: Int?              // Zen balance in cents
    var use_balance: Bool?         // Spend balance after limits hit?
    var monthly_limit: Double?     // Custom monthly cap override (Go default: nil)
    var subscription_plan: String? // "pro"/"max"/nil. Nil = lite/Go
    var lite_subscription_id: String?
    var is_admin: Bool?
    var limits_usd: GoLimitsUSD?
    var spent_usd:  GoSpentUSD?
}

struct GoLimitsUSD: Codable {
    var rolling: Int?   // 5h limit ($12 on Go)
    var weekly:  Int?   // 7d limit ($30 on Go)
    var monthly: Int?   // 30d limit ($60 on Go)
}

struct GoSpentUSD: Codable {
    var rolling: Double?   // dollars spent in 5h window
    var weekly:  Double?
    var monthly: Double?
}

struct UsageBlock: Codable {
    var five_hour: QuotaSlot?
    var seven_day: QuotaSlot?
    var seven_day_sonnet: QuotaSlot?
    var seven_day_opus: QuotaSlot?
    var extra_usage: ExtraUsage?
}

struct QuotaSlot: Codable {
    var utilization: Double?
    var resets_at: Double?
    var resets_in: String?
    /// Per-slot state string. OpenCode emits "ok" or "rate-limited" per
    /// rolling/weekly/monthly window; using it as the authoritative cap
    /// signal is more reliable than comparing `utilization` to 100 because
    /// the provider can rate-limit before the rounded percentage reaches
    /// 100. Older Claude/Codex slots leave this nil.
    var status: String?
}

struct ExtraUsage: Codable {
    var is_enabled: Bool?
    var monthly_limit: Double?
    var used_credits: Double?
}

// MARK: - Cache loader

enum CacheError: Error {
    case notFound
    case decodeFailure(Error)
}

/// Reads and decodes the usage cache from disk.
///
/// The file is read synchronously — callers should dispatch off the main thread
/// when strict latency requirements apply. For the 60-second poll loop in the
/// menu bar app this is fine; the file is small and on local storage.
func loadUsageCache() -> Result<UsageCache, CacheError> {
    let home = FileManager.default.homeDirectoryForCurrentUser
    let url = home
        .appendingPathComponent(".claude", isDirectory: true)
        .appendingPathComponent("usage-cache.json")

    guard let data = try? Data(contentsOf: url) else {
        return .failure(.notFound)
    }

    do {
        let cache = try JSONDecoder().decode(UsageCache.self, from: data)
        return .success(cache)
    } catch {
        return .failure(.decodeFailure(error))
    }
}
