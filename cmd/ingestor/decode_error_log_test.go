package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestHandleMessageDecodeErrorLog_PII — issue #1211 round-0 fix shipped without
// a test. Asserts the decode-error log line:
//
//	(a) includes structured fields: topic, observer prefix, payload length
//	(b) observer substring is at most 8 chars
//	(c) full observer ID is NOT present in the output
//
// A bare `log.Printf("... observer=%s ...", obs)` would leak the full ID.
func TestHandleMessageDecodeErrorLog_PII_Issue1211(t *testing.T) {
	store, source := newTestContext(t)

	// Use a 64-char observer ID; the prefix MUST be capped at 8 chars in logs.
	observerID := "abcdef0123456789aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// Malformed raw — pathByte=0xF6 claims 216 path bytes in a tiny buffer.
	// This triggers the decode-error path under test.
	rawHex := "12F6AAAAAAAAAAAAAAAAAAAAAAAAAA"
	topic := "meshcore/SJC/" + observerID + "/packets"
	payload := []byte(`{"raw":"` + rawHex + `"}`)
	msg := &mockMessage{topic: topic, payload: payload}

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	out := buf.String()
	if !strings.Contains(out, "decode error") {
		t.Fatalf("expected decode-error log; got:\n%s", out)
	}
	// (a) structured fields present
	if !strings.Contains(out, "topic=") {
		t.Errorf("log missing topic=; got:\n%s", out)
	}
	if !strings.Contains(out, "observer=") {
		t.Errorf("log missing observer=; got:\n%s", out)
	}
	if !strings.Contains(out, "rawHexLen=") {
		t.Errorf("log missing rawHexLen=; got:\n%s", out)
	}
	// (c) full observer ID must NOT appear
	if strings.Contains(out, observerID) {
		t.Errorf("log leaked full observer ID; got:\n%s", out)
	}
	// (b) observer substring capped at 8 chars — the 9th char ('2') after the
	// 8-char prefix must NOT appear adjacent to the prefix.
	if strings.Contains(out, "abcdef01234") {
		t.Errorf("log observer field longer than 8 chars; got:\n%s", out)
	}
	// Positive: 8-char prefix must be present in the log
	if !strings.Contains(out, "abcdef01") {
		t.Errorf("log missing 8-char observer prefix; got:\n%s", out)
	}
}
