package main

import (
	"testing"
	"time"
)

// A retained MQTT status message is a snapshot the broker replays on every
// subscribe — it says what the observer last published, not that the observer
// is alive now. Treating the replay as liveness makes dead observers immortal:
// last_seen jumps to the ingestor's restart time, RemoveStaleObservers can
// never age them out, and any row it did mark gets reactivated on the next
// restart. Observed on live 2026-08-11: 23 observers, all stamped 15s after
// container start, 18 of them with no real packet in over a month.

const retainedStatusTopic = "meshcore/LAX/obs-retained/status"

func retainedStatusMsg(topic string, payload string) *mockMessage {
	return &mockMessage{topic: topic, payload: []byte(payload), retained: true}
}

// seedObserver inserts an observer and forces last_seen to ageDays ago.
func seedObserver(t *testing.T, s *Store, id string, ageDays int) string {
	t.Helper()
	if err := s.UpsertObserver(id, id, "LAX", nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -ageDays).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE observers SET last_seen = ? WHERE id = ?`, old, id); err != nil {
		t.Fatal(err)
	}
	return old
}

func observerLastSeen(t *testing.T, s *Store, id string) string {
	t.Helper()
	var got string
	if err := s.db.QueryRow(`SELECT last_seen FROM observers WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRetainedStatusDoesNotAdvanceLastSeen(t *testing.T) {
	store := newTestStore(t)
	want := seedObserver(t, store, "obs-retained", 60)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		retainedStatusMsg(retainedStatusTopic, `{"origin":"obs-retained","noise_floor":-95.5}`),
		nil, nil, &Config{})

	if got := observerLastSeen(t, store, "obs-retained"); got != want {
		t.Errorf("last_seen = %s, want %s (retained replay is not liveness)", got, want)
	}
}

func TestRetainedStatusDoesNotReactivateInactiveObserver(t *testing.T) {
	store := newTestStore(t)
	seedObserver(t, store, "obs-retained", 60)
	if _, err := store.db.Exec(`UPDATE observers SET inactive = 1 WHERE id = ?`, "obs-retained"); err != nil {
		t.Fatal(err)
	}

	handleMessage(store, "test", MQTTSource{Name: "test"},
		retainedStatusMsg(retainedStatusTopic, `{"origin":"obs-retained","noise_floor":-95.5}`),
		nil, nil, &Config{})

	var inactive int
	if err := store.db.QueryRow(`SELECT inactive FROM observers WHERE id = ?`, "obs-retained").Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 1 {
		t.Errorf("inactive = %d, want 1 (retained replay must not resurrect a soft-deleted observer)", inactive)
	}
}

// A retained snapshot from an observer the analyzer has never heard from live
// describes a past that may be months old. Creating a row for it puts a
// permanently dead observer in the list.
func TestRetainedStatusDoesNotCreateUnknownObserver(t *testing.T) {
	store := newTestStore(t)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		retainedStatusMsg("meshcore/LAX/obs-never-seen/status", `{"origin":"Ghost","noise_floor":-95.5}`),
		nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, "obs-never-seen").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("observers row count = %d, want 0 (retained-only observer must not be created)", count)
	}
}

// The metrics sample is stamped with ingest time, so a replay would file a
// months-old reading as a present-tense measurement.
func TestRetainedStatusDoesNotInsertMetricsSample(t *testing.T) {
	store := newTestStore(t)
	seedObserver(t, store, "obs-retained", 60)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		retainedStatusMsg(retainedStatusTopic, `{"origin":"obs-retained","noise_floor":-95.5,"battery_mv":3900}`),
		nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM observer_metrics WHERE observer_id = ?`, "obs-retained",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("observer_metrics rows = %d, want 0 (retained replay is not a new sample)", count)
	}
}

// The retained snapshot is still the observer's last known state, so its
// metadata is worth keeping — only the liveness signal is suppressed.
func TestRetainedStatusStillUpdatesMetadata(t *testing.T) {
	store := newTestStore(t)
	seedObserver(t, store, "obs-retained", 60)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		retainedStatusMsg(retainedStatusTopic, `{"origin":"obs-retained","firmware":"v1.2.3","noise_floor":-95.5}`),
		nil, nil, &Config{})

	var firmware string
	var noiseFloor float64
	if err := store.db.QueryRow(
		`SELECT firmware, noise_floor FROM observers WHERE id = ?`, "obs-retained",
	).Scan(&firmware, &noiseFloor); err != nil {
		t.Fatal(err)
	}
	if firmware != "v1.2.3" {
		t.Errorf("firmware = %q, want %q", firmware, "v1.2.3")
	}
	if noiseFloor != -95.5 {
		t.Errorf("noise_floor = %v, want -95.5", noiseFloor)
	}
}

// Regression guard for #1465: a live status message must still stamp last_seen
// with ingest time. Only the retained flag changes the behaviour.
func TestLiveStatusStillAdvancesLastSeen(t *testing.T) {
	store := newTestStore(t)
	before := seedObserver(t, store, "obs-live", 60)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		&mockMessage{topic: "meshcore/LAX/obs-live/status", payload: []byte(`{"origin":"obs-live","noise_floor":-95.5}`)},
		nil, nil, &Config{})

	if got := observerLastSeen(t, store, "obs-live"); got == before {
		t.Errorf("last_seen = %s, want it advanced past %s (live status is liveness)", got, before)
	}
}

func TestLiveStatusStillCreatesUnknownObserver(t *testing.T) {
	store := newTestStore(t)

	handleMessage(store, "test", MQTTSource{Name: "test"},
		&mockMessage{topic: "meshcore/LAX/obs-fresh/status", payload: []byte(`{"origin":"Fresh","noise_floor":-95.5}`)},
		nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, "obs-fresh").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("observers row count = %d, want 1 (live status creates the observer)", count)
	}
}
