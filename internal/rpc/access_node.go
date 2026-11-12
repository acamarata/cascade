package rpc

// AccessNode reserves the access class after AccessIngest for node-facing methods.
const AccessNode Access = AccessIngest + 1

// AccessOf returns a method's declared access class and whether it was declared.
func AccessOf(method string) (Access, bool) {
	accessMu.RLock()
	defer accessMu.RUnlock()
	a, ok := methodAccess[method]
	return a, ok
}
