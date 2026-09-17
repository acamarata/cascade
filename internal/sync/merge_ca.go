package sync

// Purpose (this file): the content-addressed union for the blob domain.
//
// THERE IS NO CONFLICT TO RESOLVE HERE, and that is the design rather than
//
//	a simplification. The key IS the digest, so two blobs sharing an
//	address are byte-identical by definition. A blob domain merge is
//	therefore presence or absence, never a choice — which is why it is the
//	one strategy that cannot lose anybody's data.
//
// ADMITTED BLOBS ONLY. The union refuses an address whose bytes the
//
//	staging layer never verified. An address is only a name until something
//	has hashed the bytes under it: admitting on the declared address would
//	let a truncated transfer or a substituted payload enter the store under
//	a digest that does not describe it, and every later reader would trust
//	the name.
//
// RE-CHUNKING DOES NOT DEDUPLICATE, deliberately. Two encodings of the
//
//	same logical payload have different digests and both survive the union.
//	That costs storage and it is the correct trade: collapsing them would
//	require deciding which encoding is canonical, and a content-addressed
//	store that rewrites addresses is no longer content-addressed.
//
// Inputs: two sides keyed by content address, and what was admitted.
// Outputs: the union, or a refusal naming the unadmitted address.
// SPORT: internal/sync content-address union (ADD) — P1-E17-W4-S38-T2.

// BlobRef is one blob in the union: its content address and whether the
// staging layer admitted it.
type BlobRef struct {
	// Address is the hex content address the blob is stored under.
	Address string
	// Admitted reports whether staging verified the bytes under that
	// address. It is a field rather than an assumption because the union
	// is the last place the question can still be asked cheaply.
	Admitted bool
}

// MergeContentAddressed unions two sides of the blob domain.
//
// An unadmitted blob on EITHER side is an error, not a skip. A skip would
// make a corrupted transfer look like a blob that simply was not there,
// and the union's caller would carry on with a store missing an object it
// believes it has.
func MergeContentAddressed(sideA, sideB map[string]BlobRef) (map[string]BlobRef, error) {
	merged := make(map[string]BlobRef, len(sideA)+len(sideB))
	for _, side := range []map[string]BlobRef{sideA, sideB} {
		for addr, ref := range side {
			if !ref.Admitted {
				return nil, ErrBlobNotAdmittedf(addr)
			}
			merged[addr] = ref
		}
	}
	return merged, nil
}
