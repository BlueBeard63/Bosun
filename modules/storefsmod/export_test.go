package storefsmod

// SetVisitHook makes List report every path it visits to fn until the
// returned func is called.
func SetVisitHook(fn func(p string)) (restore func()) {
	testHookVisit = fn
	return func() { testHookVisit = nil }
}
