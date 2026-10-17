package account

import "crypto/rand"

func readRand(b []byte) (int, error) {
	n, err := rand.Read(b)
	if err == nil && n != len(b) {
		return n, errShortRead
	}
	return n, err
}
