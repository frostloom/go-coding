package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// --- M5 evaluation set ---
//
// The correct full-file contents below are what the mock LLM "writes" so
// the deterministic CI path exercises the real loop. Live mode uses the
// same fixtures and requirements but a real LLM must figure the fix out
// itself (the mock contents are not used there).
//
// Each fixture ships two tests:
//   - a visible *_test.go the agent can read (it fails on the buggy code,
//     driving the repair loop);
//   - a hidden *_acceptance_test.go that the Harness stashes out of the
//     agent's view and applies only after the run (so the agent cannot edit
//     the very test that grades it).

const (
	addFix = `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`
	divFix = `package fixdiv

// Mean returns the arithmetic mean of the numbers. An empty input yields 0.
func Mean(nums []float64) float64 {
	if len(nums) == 0 {
		return 0
	}
	var sum float64
	for _, n := range nums {
		sum += n
	}
	return sum / float64(len(nums))
}
`
	reverseFix = `package fixreverse

// Reverse returns s with its characters reversed.
func Reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
`
	concurrentFix = `package counter

import "sync"

// Counter counts things. Safe for concurrent use.
type Counter struct {
	mu sync.Mutex
	n  int
}

// Increment adds 1 to the counter.
func (c *Counter) Increment() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

// Value returns the current count.
func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
`
	parseFix = `package parsekv

import (
	"fmt"
	"strings"
)

// Parse parses a semicolon-separated list of key=value pairs into a map.
func Parse(s string) (map[string]string, error) {
	m := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return m, nil
	}
	for _, seg := range strings.Split(s, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		i := strings.Index(seg, "=")
		if i <= 0 {
			return nil, fmt.Errorf("parsekv: malformed segment %q", seg)
		}
		key := strings.TrimSpace(seg[:i])
		val := strings.TrimSpace(seg[i+1:])
		m[key] = val
	}
	return m, nil
}
`
	multiFix = `package shop

// Rebate returns the discount amount for a total: 10% of the price.
func Rebate(total float64) float64 {
	return total * 0.1
}
`
	offByOneFix = `package fixoffbyone

// SumTo returns 1 + 2 + ... + n. SumTo(0) returns 0.
func SumTo(n int) int {
	total := 0
	for i := 1; i <= n; i++ {
		total += i
	}
	return total
}
`
	errSaveFix = `package fixerrcheck

import "os"

// Save writes data to path. It must return a non-nil error if the write
// fails.
func Save(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
`
	orderedSetFix = `package implorderedset

// Set keeps strings in insertion order with no duplicates: adding a value
// that is already present is a no-op. This is the "ordered set" data
// structure — distinct from a map alone because Values() must preserve the
// first-insertion order.
type Set struct {
	index map[string]bool
	order []string
}

// New returns an empty set.
func New() *Set {
	return &Set{index: map[string]bool{}}
}

// Add inserts v if it is not already present. It reports whether v was newly
// added (false for a duplicate).
func (s *Set) Add(v string) bool {
	if s.index[v] {
		return false
	}
	s.index[v] = true
	s.order = append(s.order, v)
	return true
}

// Contains reports whether v is in the set.
func (s *Set) Contains(v string) bool {
	return s.index[v]
}

// Len returns the number of distinct values.
func (s *Set) Len() int {
	return len(s.order)
}

// Values returns the values in insertion order.
func (s *Set) Values() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}
`
	legacyTimeFix = `package timefmt

import "time"

// FormatTimestamp renders a unix timestamp (seconds since epoch) in the
// legacy wire layout "06_002_150405" (year2_dayOfYear_HHMMSS).
func FormatTimestamp(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("06_002_150405")
}
`
	lruFix = `package lru

import "container/list"

// entry holds a key-value pair in the LRU list.
type entry struct {
	key   string
	value int
}

// Cache holds at most capacity items. When the cache is full and a new item
// is added, the least recently used item is evicted. Accessing an item counts
// as using it.
type Cache struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

// New returns an empty cache with the given capacity.
func New(capacity int) *Cache {
	if capacity < 1 {
		capacity = 1
	}
	return &Cache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get returns the value for key and whether it exists. A successful Get marks
// the item most recently used.
func (c *Cache) Get(key string) (int, bool) {
	el, ok := c.items[key]
	if !ok {
		return 0, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry).value, true
}

// Put inserts or updates key. If the cache is full, the least recently used
// item is evicted.
func (c *Cache) Put(key string, value int) {
	if el, ok := c.items[key]; ok {
		el.Value.(*entry).value = value
		c.order.MoveToFront(el)
		return
	}
	if c.order.Len() >= c.capacity {
		back := c.order.Back()
		if back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(*entry).key)
		}
	}
	el := c.order.PushFront(&entry{key: key, value: value})
	c.items[key] = el
}
`
)

// PlanStep builds a one-step plan JSON string for the mock LLM. Building it
// with json.Marshal (instead of hand-escaping) keeps fixtures readable and
// the JSON correct.
func PlanStep(file, content, desc string) string {
	plan := map[string]any{
		"steps": []map[string]string{
			{"file": file, "content": content, "desc": desc},
		},
	}
	b, _ := json.Marshal(plan)
	return string(b)
}

// OutlineStep builds a one-step OUTLINE plan JSON string (P22 executor mode):
// the planner emits file + description + acceptance criteria but NO inline
// content — a worker executor derives the content. In mock mode the happy-path
// script is the two-call FIFO [OutlineStep, PlanStep]: the parent's outline
// call, then the worker's content call (the worker's plan is the same fixture
// content PlanStep always carried).
func OutlineStep(file, desc, accept string) string {
	plan := map[string]any{
		"steps": []map[string]string{
			{"file": file, "desc": desc, "accept": accept},
		},
	}
	b, _ := json.Marshal(plan)
	return string(b)
}

// readFixture loads a file under repo root (cwd). Used for the self-hosted
// fixture whose mock answer is the correct copy of go-coding's own source.
func readFixture(rel string) string {
	b, err := os.ReadFile(rel)
	if err != nil {
		panic(fmt.Sprintf("harness: read %s: %v", rel, err))
	}
	return string(b)
}

// Cases returns the full M5 evaluation set. The mock scripts give the
// correct answer on the first plan (happy path, 1 round); the repair path
// (wrong-first-edit) is exercised separately by the harness tests.
func Cases() []Case {
	return []Case{
		{
			Name:        "fix_add",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_add"),
			Requirement: "A math utility in this project returns wrong results when adding two numbers. Find the faulty function and make it compute the correct sum.",
			MockScript:  []string{OutlineStep("mathops.go", "fix Add to sum", "Add must return the sum: the visible Add test must pass"), PlanStep("mathops.go", addFix, "fix Add to sum")},
			WantPass:    true,
		},
		{
			Name:        "fix_div",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_div"),
			Requirement: "A stats function gives wrong averages. It should return the mean of the given numbers — find the bug and fix it.",
			MockScript:  []string{OutlineStep("mean.go", "divide by count", "Mean must return the arithmetic mean, and 0 for an empty input"), PlanStep("mean.go", divFix, "divide by count")},
			WantPass:    true,
		},
		{
			Name:        "fix_reverse",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_reverse"),
			Requirement: "A string helper returns incorrectly reversed strings — the output is missing content. Make it reverse a string properly.",
			MockScript:  []string{OutlineStep("reverse.go", "reverse in place", "Reverse must produce the exact reversed string (the visible test must pass)"), PlanStep("reverse.go", reverseFix, "reverse in place")},
			WantPass:    true,
		},
		{
			Name:        "impl_concurrency",
			Type:        TypeImpl,
			FixtureDir:  filepath.Join("testdata", "cases", "impl_concurrency"),
			Requirement: "Make the counter safe for concurrent use so increments are never lost.",
			MockScript:  []string{OutlineStep("counter.go", "add mutex", "the visible counter test must pass: no lost increments under concurrency"), PlanStep("counter.go", concurrentFix, "add mutex")},
			WantPass:    true,
		},
		{
			Name:        "impl_parse",
			Type:        TypeImpl,
			FixtureDir:  filepath.Join("testdata", "cases", "impl_parse"),
			Requirement: "A config parser is missing: turn a semicolon-separated list of key=value pairs into a map, with sensible error handling for malformed input.",
			MockScript:  []string{OutlineStep("parse.go", "implement Parse", "Parse must map valid key=value pairs and return an error on malformed segments"), PlanStep("parse.go", parseFix, "implement Parse")},
			WantPass:    true,
		},
		{
			Name:        "fix_multi",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_multi"),
			Requirement: "The total price on an invoice is wrong — it's too low. When you sum the items and apply the discount, the final number should be 10% off the total, but it isn't. Trace the call chain to find where the bug really is.",
			MockScript:  []string{OutlineStep("rebate.go", "fix Rebate to 10%", "Rebate must return 10%% of the total (the visible invoice test must pass)"), PlanStep("rebate.go", multiFix, "fix Rebate to 10%")},
			WantPass:    true,
		},
		{
			Name:        "impl_lru",
			Type:        TypeImpl,
			FixtureDir:  filepath.Join("testdata", "cases", "impl_lru"),
			Requirement: "Implement a bounded cache with LRU eviction: it holds at most N items, evicts the least recently used item when full, and accessing an item counts as using it.",
			MockScript:  []string{OutlineStep("lru.go", "implement LRU cache", "the LRU test must pass: bounded capacity, LRU eviction when full, Get counts as use"), PlanStep("lru.go", lruFix, "implement LRU cache")},
			WantPass:    true,
		},
		{
			Name:        "fix_offbyone",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_offbyone"),
			Requirement: "A math helper returns wrong sums: SumTo(5) gives 10 instead of 15. The loop is off by one — it stops one step early and never adds the final value. Fix the boundary so every value from 1 to n is included.",
			MockScript:  []string{OutlineStep("range.go", "fix loop boundary to i <= n", "SumTo(n) must include every value 1..n — SumTo(5)==15, SumTo(0)==0"), PlanStep("range.go", offByOneFix, "fix loop boundary to i <= n")},
			WantPass:    true,
		},
		{
			Name:        "fix_errcheck",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_errcheck"),
			Requirement: "A file-save helper swallows errors: Save returns nil even when the write fails, so callers never find out their data was not persisted. Make Save return the underlying error on failure while still writing successfully on the happy path.",
			MockScript:  []string{OutlineStep("save.go", "propagate the write error", "Save must write on the happy path and return a non-nil error when the write fails"), PlanStep("save.go", errSaveFix, "propagate the write error")},
			WantPass:    true,
		},
		{
			Name:        "impl_orderedset",
			Type:        TypeImpl,
			FixtureDir:  filepath.Join("testdata", "cases", "impl_orderedset"),
			Requirement: "Implement an ordered set of strings: it keeps values in insertion order with no duplicates — adding an existing value is a no-op. Values() must return them in first-insertion order, and mutating that returned slice must not corrupt the set.",
			MockScript:  []string{OutlineStep("orderedset.go", "implement ordered set", "the ordered-set test must pass: insertion order, no duplicates, mutating Values() output must not corrupt the set"), PlanStep("orderedset.go", orderedSetFix, "implement ordered set")},
			WantPass:    true,
		},
		{
			// fix_legacy_time is the fixture the live MCP client smoke created:
			// a bug whose fix requires knowledge only an external tool holds (the
			// obscure wire layout). Wiring it into the suite makes the smoke a
			// first-class evaluation case — in mock mode the scripted fix is
			// graded against the same hidden acceptance the smoke ran.
			Name:        "fix_legacy_time",
			Type:        TypeBugfix,
			FixtureDir:  filepath.Join("testdata", "cases", "fix_legacy_time"),
			Requirement: "A timestamp helper returns the raw integer, but the legacy reporting API expects a compact wire format. Fix FormatTimestamp to render unix seconds in the legacy layout.",
			MockScript:  []string{OutlineStep("timefmt.go", "render legacy wire layout", "FormatTimestamp must render unix seconds in the legacy wire layout"), PlanStep("timefmt.go", legacyTimeFix, "render legacy wire layout")},
			WantPass:    true,
		},
		{
			// self_codeindex is the "dogfooding" case: the fixture is go-coding's
			// OWN symbol engine (internal/codeindex, real source, ~500 lines, 4
			// files) with one real bug planted in it. The mock answer is the
			// correct retriever.go (retriever.fixed.txt — a faithful copy of the
			// production file). Live mode uses the same fixture and a real LLM
			// must find and fix the bug itself. This proves the agent works on a
			// real, multi-file Go project, not just the tiny arithmetic toys.
			Name:        "self_codeindex",
			Type:        TypeSelf,
			FixtureDir:  filepath.Join("testdata", "cases", "self_codeindex"),
			Requirement: "The code retrieval engine returns the wrong context. Querying for a symbol should surface the functions that symbol depends on (the ones it calls), but the results are missing them. Find and fix the retrieval bug so that a query for a function includes the functions it calls.",
			MockScript: []string{OutlineStep("retriever.go",
				"expand callees in the graph layer of Retrieve",
				"querying a function must include the functions it calls (the retrieval test must pass)"),
				PlanStep("retriever.go",
					readFixture(filepath.Join("testdata", "cases", "self_codeindex", "retriever.fixed.txt")),
					"expand callees in the graph layer of Retrieve")},
			WantPass: true,
		},
	}
}
