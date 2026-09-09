package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestRegionNameAcceptable(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
		why  string
	}{
		{"be", "be", true, "a bare name from an OTA answer"},
		{"#be", "be", true, "the same region as nodes.configured_scope spells it — normalizeScopeList adds the '#'"},
		{"nl-li-nth", "nl-li-nth", true, "hyphens are ordinary in region names"},
		{"*", "", false, "the flood wildcard is not a region, and hashing it would invent one nobody declared"},
		{"#*", "", false, "the wildcard stays the wildcard however it is spelled"},
		{"", "", false, "empty"},
		{"#", "", false, "a bare prefix names nothing"},
		{"be,nl", "", false, "a comma would split the name on the next round-trip"},
		{"be#nl", "", false, "a second '#' cannot come from either source intact"},
		{"be nl", "", false, "whitespace would hash bytes nobody intended"},
		{"bé", "", false, "non-ASCII, same reason"},
		{"be" + string(rune(0)), "", false, "NUL is block-cipher padding a stale client failed to trim"},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", false, "33 chars, past maxRegionNameLen"},
	}
	for _, c := range cases {
		got, ok := regionNameAcceptable(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("regionNameAcceptable(%q) = (%q, %v), want (%q, %v) — %s", c.name, got, ok, c.want, c.ok, c.why)
		}
	}
}

func TestRankDeclaredRegionsPrefersWidelyDeclared(t *testing.T) {
	// The cap must drop the long tail of one-off local names, never a region
	// half the network declares. On live data "be" is declared by 127
	// repeaters and "behss" by 3.
	stats := []declaredRegionStat{
		{Name: "behss", Declarers: 3, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "be", Declarers: 127, LastSeen: "2026-09-01T10:00:00Z"},
		{Name: "sol3", Declarers: 1, LastSeen: "2026-09-07T11:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 2)
	want := []string{"be", "behss"}
	if len(got) != len(want) {
		t.Fatalf("rankDeclaredRegions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rankDeclaredRegions = %v, want %v — declarer count must dominate recency", got, want)
		}
	}
}

func TestRankDeclaredRegionsIsDeterministic(t *testing.T) {
	// Equal declarer counts and equal timestamps must still produce a stable
	// order, or the derived tier churns between refreshes and the add/drop
	// logging becomes noise.
	stats := []declaredRegionStat{
		{Name: "zz", Declarers: 2, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "aa", Declarers: 2, LastSeen: "2026-09-07T10:00:00Z"},
	}
	for i := 0; i < 20; i++ {
		got := rankDeclaredRegions(stats, 10)
		if got[0] != "aa" || got[1] != "zz" {
			t.Fatalf("run %d: rankDeclaredRegions = %v, want [aa zz]", i, got)
		}
	}
}

func TestRankDeclaredRegionsBreaksTiesOnRecency(t *testing.T) {
	stats := []declaredRegionStat{
		{Name: "old", Declarers: 2, LastSeen: "2026-01-01T00:00:00Z"},
		{Name: "new", Declarers: 2, LastSeen: "2026-09-07T00:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 1)
	if len(got) != 1 || got[0] != "new" {
		t.Fatalf("rankDeclaredRegions = %v, want [new] — equal declarers break on recency", got)
	}
}

func TestRankDeclaredRegionsDropsUnacceptableNames(t *testing.T) {
	stats := []declaredRegionStat{
		{Name: "be", Declarers: 5, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "b,ad", Declarers: 99, LastSeen: "2026-09-07T10:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 10)
	if len(got) != 1 || got[0] != "be" {
		t.Fatalf("rankDeclaredRegions = %v, want [be] — a name with a comma cannot survive a comma-separated column, however widely declared", got)
	}
}

func TestSplitDeclaredRegionsCSV(t *testing.T) {
	// Exact inverse of the strings.Join the ingestor writes the column with.
	got := splitDeclaredRegionsCSV(" be , eu ,, nl ")
	want := []string{"be", "eu", "nl"}
	if len(got) != len(want) {
		t.Fatalf("splitDeclaredRegionsCSV = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitDeclaredRegionsCSV = %v, want %v", got, want)
		}
	}
	if n := len(splitDeclaredRegionsCSV("")); n != 0 {
		t.Errorf("empty csv produced %d entries, want 0", n)
	}
}

func TestRegionKeySetExplicitOnlyWhenDisabled(t *testing.T) {
	// Derivation off: the snapshot must be exactly what loadRegionKeys built,
	// and refreshDerived must be a no-op rather than a quiet opt-in.
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"behss", "fm-112"})

	snap := set.snapshot()
	if len(snap.all) != 1 {
		t.Fatalf("len(all) = %d, want 1 — refreshDerived must not add keys when disabled", len(snap.all))
	}
	if _, ok := snap.all["#be"]; !ok {
		t.Error("want the explicit #be key present")
	}
	if !snap.isExplicit("#be") {
		t.Error("isExplicit(#be) = false, want true")
	}
}

func TestRegionKeySetMergesDerivedWhenEnabled(t *testing.T) {
	cfg := &Config{
		HashRegions:    []string{"#be"},
		AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true},
	}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"behss", "be"}) // "be" duplicates the explicit key

	snap := set.snapshot()
	if len(snap.all) != 2 {
		t.Fatalf("len(all) = %d, want 2 (#be explicit + #behss derived), got keys %v", len(snap.all), keyNames(snap))
	}
	if _, ok := snap.all["#behss"]; !ok {
		t.Errorf("want the derived #behss key present, got %v", keyNames(snap))
	}
	if snap.isExplicit("#behss") {
		t.Error("isExplicit(#behss) = true, want false — a derived key is not operator config")
	}
	if !snap.isExplicit("#be") {
		t.Error("isExplicit(#be) = false, want true — an explicit key must not be demoted by a duplicate declaration")
	}
}

func TestRegionKeySetRefreshReplacesRatherThanAccumulates(t *testing.T) {
	// A region that stops being declared must leave the derived tier, or the
	// key set only ever grows and the cap stops meaning anything.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa"})
	set.refreshDerived([]string{"bb"})

	snap := set.snapshot()
	if _, ok := snap.all["#aa"]; ok {
		t.Error("want #aa gone after a refresh that no longer lists it")
	}
	if _, ok := snap.all["#bb"]; !ok {
		t.Error("want #bb present after the refresh that lists it")
	}
}

func TestRegionKeySetSnapshotIsStable(t *testing.T) {
	// A snapshot handed to a packet must not change under it mid-match.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa"})
	held := set.snapshot()
	set.refreshDerived([]string{"bb"})

	if _, ok := held.all["#aa"]; !ok {
		t.Error("the held snapshot lost #aa — snapshots must be immutable, not aliases of live state")
	}
}

// keyNames is a test helper for readable failure messages.
func keyNames(s *regionKeySnapshot) []string {
	out := make([]string, 0, len(s.all))
	for k := range s.all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// codeFor derives the on-wire code1 a sender in region `name` would emit for
// this payload - the same computation matchingRegions inverts. Used to build
// packets that genuinely belong to a region rather than asserting on a
// hardcoded string.
func codeFor(name string, payloadType byte, payload []byte) string {
	if !strings.HasPrefix(name, "#") {
		name = "#" + name
	}
	sum := sha256.Sum256([]byte(name))
	mac := hmac.New(sha256.New, sum[:16])
	mac.Write([]byte{payloadType})
	mac.Write(payload)
	h := mac.Sum(nil)
	code := uint16(h[0]) | uint16(h[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	return strings.ToUpper(hex.EncodeToString([]byte{byte(code & 0xFF), byte(code >> 8)}))
}

func TestScopeMatchUniqueNamesTheRegion(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	got := set.snapshot().match(5, payload, code)
	if got.Name != "#be" {
		t.Errorf("Name = %q, want %q", got.Name, "#be")
	}
	if got.Reason != scopeReasonUnique {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonUnique)
	}
}

func TestScopeMatchNoKeyMatches(t *testing.T) {
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)

	got := set.snapshot().match(5, []byte{1, 2, 3}, "0000")
	if got.Name != "" || got.Reason != scopeReasonNone {
		t.Errorf("got %+v, want an empty name with reason %q", got, scopeReasonNone)
	}
}

func TestScopeMatchExplicitBeatsDerived(t *testing.T) {
	// The ambiguity this feature introduces: a derived key collides with an
	// operator-configured one on this payload. Operator config wins - it is
	// intent, the derived name is hearsay picked up over RF.
	payload := []byte{0x01, 0x02, 0x03, 0x04}
	cfg := &Config{HashRegions: []string{"#be"}, AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	// Force the collision rather than searching for a natural one: inject a
	// derived key whose bytes are the explicit key's, so both match.
	snap := set.snapshot()
	collide := make(map[string][]byte, len(snap.all)+1)
	for k, v := range snap.all {
		collide[k] = v
	}
	collide["#collider"] = snap.all["#be"]
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}

	got := forced.match(5, payload, code)
	if got.Name != "#be" {
		t.Errorf("Name = %q, want %q - the explicit key must win", got.Name, "#be")
	}
	if got.Reason != scopeReasonExplicitOverDerived {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonExplicitOverDerived)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("Candidates = %v, want both names recorded for the log", got.Candidates)
	}
}

func TestScopeMatchTwoExplicitKeysStayAmbiguous(t *testing.T) {
	// Two equally-sourced candidates: naming either would be a guess, and
	// naming wrongly is worse than not naming. This is #1609's rule, unchanged.
	payload := []byte{0x09, 0x08, 0x07}
	cfg := &Config{HashRegions: []string{"#be", "#eu"}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	snap := set.snapshot()
	collide := map[string][]byte{"#be": snap.all["#be"], "#eu": snap.all["#be"]}
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}

	got := forced.match(5, payload, code)
	if got.Name != "" {
		t.Errorf("Name = %q, want empty - two explicit candidates must abstain", got.Name)
	}
	if got.Reason != scopeReasonAmbiguous {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonAmbiguous)
	}
}

func TestScopeMatchTwoDerivedKeysStayAmbiguous(t *testing.T) {
	// The tier-3 case, deliberately NOT resolved in M2. It must abstain rather
	// than pick, and the reason must say ambiguous so the log can measure how
	// often this happens before tier 3 is built.
	payload := []byte{0x11, 0x22}
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa", "bb"})

	snap := set.snapshot()
	collide := map[string][]byte{"#aa": snap.all["#aa"], "#bb": snap.all["#aa"]}
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}
	code := codeFor("#aa", 5, payload)

	got := forced.match(5, payload, code)
	if got.Name != "" || got.Reason != scopeReasonAmbiguous {
		t.Errorf("got %+v, want an empty name with reason %q", got, scopeReasonAmbiguous)
	}
}

// regionSetFromKeys wraps a raw key map as a *regionKeySet with every key
// treated as explicit. Tests written before M2's two-tier type keep working
// unchanged this way, and "all keys explicit" is precisely what a hashRegions
// map meant back then - so the #1609 ambiguity semantics they assert are
// preserved exactly: two explicit candidates still abstain.
func regionSetFromKeys(keys map[string][]byte) *regionKeySet {
	names := make(map[string]bool, len(keys))
	all := make(map[string][]byte, len(keys))
	for n, k := range keys {
		names[n] = true
		all[n] = k
	}
	s := &regionKeySet{}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: names})
	return s
}

// matchScopeName reproduces the removed matchScope's signature over the new
// type, so the tests written against it keep asserting the behaviour they were
// written for rather than being rewritten alongside the change they guard.
func matchScopeName(keys map[string][]byte, payloadType byte, payloadRaw []byte, code1 string) string {
	return regionSetFromKeys(keys).snapshot().match(payloadType, payloadRaw, code1).Name
}

func TestDeclaredRegionSourcesAggregatesLatestAnswerPerTarget(t *testing.T) {
	store := newTestStore(t)
	// Two answers from the same target: only the newer one counts, exactly as
	// CurrentDeclaredRegions orders (by observed_at, never ingested_at - a
	// drive buffered offline can arrive days late).
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-01T00:00:00Z", "be,old")
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "be,new")
	insertDeclaredRegionsRow(t, store, "bb"+strings.Repeat("22", 31), "2026-09-05T00:00:00Z", "be")

	stats, err := store.declaredRegionSources()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]declaredRegionStat{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	if got := byName["be"].Declarers; got != 2 {
		t.Errorf("be declarers = %d, want 2", got)
	}
	if got := byName["be"].LastSeen; got != "2026-09-07T00:00:00Z" {
		t.Errorf("be lastSeen = %q, want the greatest observed_at", got)
	}
	if _, ok := byName["old"]; ok {
		t.Error("want the superseded answer's region gone - only the latest answer per target counts")
	}
	if got := byName["new"].Declarers; got != 1 {
		t.Errorf("new declarers = %d, want 1", got)
	}
}

func TestDeclaredRegionSourcesIgnoresWildcard(t *testing.T) {
	// '*' is the wildcard, not a region name. Deriving a key for it would add
	// a permanent no-op entry to the cap on nearly every deployment.
	store := newTestStore(t)
	insertDeclaredRegionsRow(t, store, "cc"+strings.Repeat("33", 31), "2026-09-07T00:00:00Z", "*,be")
	stats, err := store.declaredRegionSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Name == "*" {
			t.Error("want '*' excluded - it is the wildcard, not a region")
		}
	}
	if len(stats) != 1 {
		t.Errorf("stats = %+v, want just be", stats)
	}
}

// insertDeclaredRegionsRow seeds one node_declared_regions answer, creating
// the table first. That table is optional: this ingestor never creates it, and
// declaredRegionSources probes sqlite_master before reading it. A test that
// wants the optional source has to bring it.
func insertDeclaredRegionsRow(t *testing.T, s *Store, target, observedAt, regionsCSV string) {
	t.Helper()
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS node_declared_regions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			target      TEXT NOT NULL,
			rx_pubkey   TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			ingested_at TEXT NOT NULL,
			regions_csv TEXT NOT NULL,
			truncated   INTEGER NOT NULL DEFAULT 0,
			UNIQUE(target, rx_pubkey, observed_at)
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		 VALUES (?, 'rx', ?, ?, ?, 0)`, target, observedAt, observedAt, regionsCSV); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshFromStoreIsNoOpWhenDisabled(t *testing.T) {
	store := newTestStore(t)
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "behss")

	cfg := &Config{HashRegions: []string{"#be"}} // autoRegionKeys absent
	set := newRegionKeySet(cfg)
	before := len(set.snapshot().all)
	set.refreshFromStore(store)

	if got := len(set.snapshot().all); got != before {
		t.Errorf("key count %d -> %d with autoRegionKeys off, want unchanged", before, got)
	}
	if _, ok := set.snapshot().all["#behss"]; ok {
		t.Error("a declared name became a key with the feature disabled - this is the safety property the default-off promise rests on")
	}
}

func TestRefreshFromStoreDerivesWhenEnabled(t *testing.T) {
	store := newTestStore(t)
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "behss,fm-112")

	cfg := &Config{HashRegions: []string{"#be"}, AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshFromStore(store)

	snap := set.snapshot()
	for _, want := range []string{"#be", "#behss", "#fm-112"} {
		if _, ok := snap.all[want]; !ok {
			t.Errorf("want %s in force, got %v", want, keyNames(snap))
		}
	}
	if !snap.isExplicit("#be") || snap.isExplicit("#behss") {
		t.Error("tiers crossed: #be must stay explicit, #behss must be derived")
	}
}

func TestRefreshFromStoreHonoursTheCap(t *testing.T) {
	store := newTestStore(t)
	// Three names, one declared twice so the ranking is not a coin flip.
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "wide,narrow1")
	insertDeclaredRegionsRow(t, store, "bb"+strings.Repeat("22", 31), "2026-09-07T00:00:00Z", "wide,narrow2")

	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true, MaxDerived: 1}}
	set := newRegionKeySet(cfg)
	set.refreshFromStore(store)

	snap := set.snapshot()
	if len(snap.all) != 1 {
		t.Fatalf("keys = %v, want exactly 1 under maxDerived=1", keyNames(snap))
	}
	if _, ok := snap.all["#wide"]; !ok {
		t.Errorf("keys = %v, want the twice-declared name kept, not a one-off", keyNames(snap))
	}
}

// BenchmarkScopeMatch sweeps key-set size because the cost is linear in it and
// cannot be reduced: code1 is an HMAC over the packet payload, so there is no
// payload-independent lookup key to index on. The sweep is the evidence for
// choosing maxDerived, not a single before/after number - the explicit tier's
// size is operator config and varies per deployment.
func BenchmarkScopeMatch(b *testing.B) {
	payload := make([]byte, 51) // a typical GRP_TXT payload
	for i := range payload {
		payload[i] = byte(i)
	}
	for _, n := range []int{16, 58, 180, 314} {
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			all := make(map[string][]byte, n)
			explicit := make(map[string]bool, n)
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("#r%04d", i)
				sum := sha256.Sum256([]byte(name))
				all[name] = sum[:16]
				explicit[name] = true
			}
			snap := &regionKeySnapshot{all: all, explicit: explicit}
			code := codeFor("#r0000", 5, payload)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = snap.match(5, payload, code)
			}
		})
	}
}

// TestRegionKeySetConcurrentRefreshAndMatch drives the shape the type exists
// for and that nothing else in this suite produces: ingest goroutines naming
// packets while the refresh ticker swaps the key set underneath them.
//
// It is written for `go test -race`. Run without it, it asserts only that the
// explicit tier keeps naming its packet across 200 swaps, which is worth
// little; run with it, the detector sees every unsynchronised read of the
// snapshot that a real refresh would expose. Until this existed, a race run of
// this package proved nothing about regionKeySet, because refreshDerived and
// match were never called concurrently anywhere in it — the absence of a
// finding was the absence of an experiment.
//
// The final assertion holds regardless of what the churn derives: a derived key
// colliding with #be on this payload resolves to the explicit key by tier 2,
// and #be is the only explicit key here, so there is no second explicit
// candidate that could make the answer ambiguous.
func TestRegionKeySetConcurrentRefreshAndMatch(t *testing.T) {
	const explicitName = "#be"
	payload := []byte{0x11, 0x22, 0x33, 0x44}
	code1 := codeFor(explicitName, 5, payload)

	set := newRegionKeySet(&Config{
		HashRegions:    []string{explicitName},
		AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true},
	})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// snapshot() then match() is exactly the ingest hot path:
				// one atomic load, then reads of the maps it points at.
				if got := set.snapshot().match(5, payload, code1); got.Name != explicitName && got.Reason != scopeReasonAmbiguous {
					// Reported rather than fataled: t.Fatalf from a
					// non-test goroutine is undefined behaviour.
					t.Errorf("match during refresh returned %q (%s), want %q", got.Name, got.Reason, explicitName)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 200; i++ {
			// A different derived set each time, so every iteration really
			// builds and swaps a new snapshot rather than reusing one.
			set.refreshDerived([]string{fmt.Sprintf("r%03d", i%7), fmt.Sprintf("q%03d", i)})
		}
	}()

	wg.Wait()

	if got := set.snapshot().match(5, payload, code1); got.Name != explicitName {
		t.Errorf("after 200 refreshes match = %q (%s), want %q — the explicit tier must survive every swap",
			got.Name, got.Reason, explicitName)
	}
}

// TestDeclaredRegionSourcesReadsConfiguredScope is the upstream-native source:
// nodes.configured_scope, written by the observer /neighbors ingestion. It is
// the only source a stock install has, so a derived tier that read only the
// optional table would be permanently empty here.
//
// The stored form carries the leading "#" (normalizeScopeList puts it there),
// which is exactly the spelling regionNameAcceptable has to accept.
func TestDeclaredRegionSourcesReadsConfiguredScope(t *testing.T) {
	store := newTestStore(t)
	seedNodeWithConfiguredScope(t, store, "aa"+strings.Repeat("11", 31), "#be,#nl-li,*", "2026-09-07T00:00:00Z")
	seedNodeWithConfiguredScope(t, store, "bb"+strings.Repeat("22", 31), "#be", "2026-09-08T00:00:00Z")

	stats, err := store.declaredRegionSources()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]declaredRegionStat{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	if got := byName["#be"].Declarers; got != 2 {
		t.Errorf("#be declarers = %d, want 2 — both nodes declare it", got)
	}
	if got := byName["#be"].LastSeen; got != "2026-09-08T00:00:00Z" {
		t.Errorf("#be lastSeen = %q, want the newer of the two answers", got)
	}
	if got := byName["#nl-li"].Declarers; got != 1 {
		t.Errorf("#nl-li declarers = %d, want 1", got)
	}
	if _, ok := byName["*"]; ok {
		t.Error("want '*' excluded from the declared-name count — it is the wildcard, and nearly every node declares it")
	}
}

// TestDeclaredRegionSourcesMergesBothSources: an instance that has both the
// column and the optional table must see a name declared in either, and count
// a node once per name rather than once per source.
func TestDeclaredRegionSourcesMergesBothSources(t *testing.T) {
	store := newTestStore(t)
	pk := "cc" + strings.Repeat("33", 31)
	seedNodeWithConfiguredScope(t, store, pk, "#be", "2026-09-07T00:00:00Z")
	insertDeclaredRegionsRow(t, store, pk, "2026-09-08T00:00:00Z", "fm-112")

	stats, err := store.declaredRegionSources()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, s := range stats {
		names[s.Name] = s.Declarers
	}
	if names["#be"] != 1 {
		t.Errorf("#be declarers = %d, want 1 (from configured_scope)", names["#be"])
	}
	if names["fm-112"] != 1 {
		t.Errorf("fm-112 declarers = %d, want 1 (from the optional table)", names["fm-112"])
	}
	// Both spellings of the same region reach the same derived key, which is
	// what regionNameAcceptable's "#"-stripping is for. Asserted there rather
	// than here; this only pins that neither source is dropped.
}

// seedNodeWithConfiguredScope writes one node with a confirmed scope list, the
// shape handleNeighborsReport produces.
func seedNodeWithConfiguredScope(t *testing.T, s *Store, pubkey, scopes, at string) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO nodes (public_key, configured_scope, configured_scope_at) VALUES (?, ?, ?)`,
		pubkey, scopes, at); err != nil {
		t.Fatal(err)
	}
}
