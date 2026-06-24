import Foundation

// MARK: - Cache models
// Ported from ClawFleet Sources/Shared/UsageCache.swift.
// Schema matches both ~/.cascade/accounts/quota.json (primary) and
// ~/.claude/usage-cache.json (legacy fallback): accounts:[{account,provider,
// email,usage:{five_hour,seven_day,...},quota_opaque,last_pull_at,status,...}]

struct UsageCache: Codable {
    var accounts: [AccountEntry]
    var queried_at: Double?
    var sanity: SanityBlock?
}

struct SanityBlock: Codable {
    var checked_at: Double?
    var ok: Bool?
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
    var status: String?
    var key_count: Int?
    var opencode_meta: OpencodeMeta?
}

struct OpencodeMeta: Codable {
    var balance: Int?
    var use_balance: Bool?
    var monthly_limit: Double?
    var subscription_plan: String?
    var lite_subscription_id: String?
    var is_admin: Bool?
    var limits_usd: GoLimitsUSD?
    var spent_usd:  GoSpentUSD?
}

struct GoLimitsUSD: Codable {
    var rolling: Int?
    var weekly:  Int?
    var monthly: Int?
}

struct GoSpentUSD: Codable {
    var rolling: Double?
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
    var status: String?
}

struct ExtraUsage: Codable {
    var is_enabled: Bool?
    var monthly_limit: Double?
    var used_credits: Double?
}

// MARK: - Cache error

enum CacheError: Error {
    case notFound
    case decodeFailure(Error)
}

// MARK: - Cache loader (two-path: Cascade primary, legacy fallback)

/// Decode a UsageCache from a given file URL. Returns nil on any failure.
private func tryLoad(url: URL) -> UsageCache? {
    guard let data = try? Data(contentsOf: url) else { return nil }
    return try? JSONDecoder().decode(UsageCache.self, from: data)
}

/// Loads the usage cache from disk.
///
/// Preference order:
///   1. ~/.cascade/accounts/quota.json  — Cascade daemon's file (when it has accounts)
///   2. ~/.claude/usage-cache.json      — Legacy fleet cache (Codex/OpenCode data)
///
/// Whichever file decodes first and contains at least one account wins.
func loadUsageCache() -> Result<UsageCache, CacheError> {
    let home = FileManager.default.homeDirectoryForCurrentUser
    let primary  = home.appendingPathComponent(".cascade/accounts/quota.json")
    let fallback = home.appendingPathComponent(".claude/usage-cache.json")

    // Try primary first; accept it only when it has accounts.
    if let c = tryLoad(url: primary), !c.accounts.isEmpty {
        return .success(c)
    }
    // Try legacy fallback.
    if let c = tryLoad(url: fallback) {
        return .success(c)
    }
    // Neither file exists / decodes.
    if !FileManager.default.fileExists(atPath: primary.path),
       !FileManager.default.fileExists(atPath: fallback.path) {
        return .failure(.notFound)
    }
    return .failure(.notFound)
}
