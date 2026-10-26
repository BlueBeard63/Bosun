package util

func Double(n int) int { return n * 2 }

// Pair is two ints, in a package that doesn't import bosun.
type Pair struct {
	A, B int // The values.
}
