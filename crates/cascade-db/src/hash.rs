//! Purpose: Canonical content hashing for Cascade. BLAKE3 everywhere — no MD5/SHA-256.
//! Inputs: bytes or string content.
//! Outputs: lowercase hex BLAKE3 digest, used for dedup, cache keys, and delta detection.
//! Constraints: this is THE content-hash function; locked decision #2. Do not reintroduce SHA-256.
//! SPORT: cascade-db / hash

/// Compute the canonical BLAKE3 content hash as a lowercase hex string.
pub fn content_hash(bytes: &[u8]) -> String {
    blake3::hash(bytes).to_hex().to_string()
}

/// Convenience wrapper for hashing string content.
pub fn content_hash_str(s: &str) -> String {
    content_hash(s.as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stable_and_distinct() {
        assert_eq!(content_hash_str("hello"), content_hash_str("hello"));
        assert_ne!(content_hash_str("hello"), content_hash_str("world"));
        // BLAKE3 hex is 64 chars.
        assert_eq!(content_hash_str("x").len(), 64);
    }
}
