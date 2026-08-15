package lru

// Cache holds at most capacity items. When the cache is full and a new item
// is added, the least recently used item is evicted. Accessing an item counts
// as using it.
type Cache struct{}

// New returns an empty cache with the given capacity.
func New(capacity int) *Cache {
	return &Cache{}
}

// Get returns the value for key and whether it exists. A successful Get marks
// the item most recently used.
func (c *Cache) Get(key string) (int, bool) {
	return 0, false
}

// Put inserts or updates key. If the cache is full, the least recently used
// item is evicted.
func (c *Cache) Put(key string, value int) {
}
