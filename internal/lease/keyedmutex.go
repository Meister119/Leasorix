package lease

import "sync"

// keyedMutex provides one mutex per key, created on demand and dropped when
// no longer used.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*kmEntry
}

type kmEntry struct {
	mu  sync.Mutex
	ref int
}

// Lock locks the mutex for key and returns the unlock function.
func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*kmEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &kmEntry{}
		k.m[key] = e
	}
	e.ref++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.ref--
		if e.ref == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
