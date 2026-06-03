package main

import cryptorand "crypto/rand"

// cryptoRandRead is the seam for crypto/rand.Read (injectable for tests).
// Default implementation uses the system CSPRNG.
var cryptoRandRead = func(b []byte) (int, error) {
	return cryptorand.Read(b)
}
