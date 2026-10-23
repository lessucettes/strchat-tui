package client

// A classification check needs no formatted error for every ordinary chat
// name. Keep this byte predicate equivalent to geohash.Validate; actual catalog
// validation (including its separate nonempty check) retains diagnostic errors.
var geohashNameBytes = func() [256]bool {
	var valid [256]bool
	for _, b := range []byte("0123456789bcdefghjkmnpqrstuvwxyz") {
		valid[b] = true
	}
	return valid
}()

func isGeohashName(name string) bool {
	if len(name) > 12 {
		return false
	}
	for i := range len(name) {
		if !geohashNameBytes[name[i]] {
			return false
		}
	}
	return true
}
