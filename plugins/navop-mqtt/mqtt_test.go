package main

// navop-mqtt 插件单测（§331）：MQTT 协议编解码与主题过滤是插件质量核心，
// 此前零测试。这里覆盖变长长度编解码（1/2/3/4 字节边界）与主题通配符语义。

import (
	"bytes"
	"testing"
)

func TestRemainingLengthRoundTrip(t *testing.T) {
	cases := []int{0, 1, 127, 128, 16383, 16384, 2097151, 2097152, 268435455}
	for _, n := range cases {
		enc := encodeRemainingLength(n)
		got, err := readRemainingLength(bytes.NewReader(enc))
		if err != nil {
			t.Fatalf("read(%d): %v", n, err)
		}
		if got != n {
			t.Fatalf("round trip %d → %d", n, got)
		}
	}
}

func TestReadRemainingLengthRejectsOverlong(t *testing.T) {
	// MQTT caps remaining length at 4 bytes; a 5th continuation byte is invalid.
	overlong := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x7F}
	if _, err := readRemainingLength(bytes.NewReader(overlong)); err == nil {
		t.Fatal("overlong encoding must be rejected")
	}
}

func TestTopicMatchesSemantics(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"a/#", "a/b/c", true},
		{"a/#", "a", true},       // '#' matches zero or more levels
		{"a/+/c", "a/b/c", true},
		{"a/+/c", "a/b/x/c", false}, // '+' matches exactly one level
		{"+", "a/b", false},
		{"#", "a/b/c", true},
	}
	for _, c := range cases {
		if got := topicMatches(c.filter, c.topic); got != c.want {
			t.Errorf("topicMatches(%q, %q) = %v, want %v", c.filter, c.topic, got, c.want)
		}
	}
}

func TestMQTTStringRoundTrip(t *testing.T) {
	dst := appendMQTTString(nil, "hello")
	got, rest, ok := readMQTTString(dst)
	if !ok || got != "hello" || len(rest) != 0 {
		t.Fatalf("round trip: got %q rest %v ok %v", got, rest, ok)
	}
}
