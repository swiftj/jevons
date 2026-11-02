package router

import "container/list"

// lru is a bounded set of keys with least-recently-added eviction.
// It is not safe for concurrent use; the Engine guards it with its mutex.
type lru struct {
	cap   int
	order *list.List
	items map[string]*list.Element
}

func newLRU(capacity int) *lru {
	if capacity < 1 {
		capacity = 1
	}
	return &lru{cap: capacity, order: list.New(), items: make(map[string]*list.Element, capacity)}
}

// add inserts key and reports true, or reports false if key is already present.
func (l *lru) add(key string) bool {
	if el, ok := l.items[key]; ok {
		l.order.MoveToFront(el)
		return false
	}
	l.items[key] = l.order.PushFront(key)
	if l.order.Len() > l.cap {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		if k, ok := oldest.Value.(string); ok {
			delete(l.items, k)
		}
	}
	return true
}

func (l *lru) len() int { return l.order.Len() }
