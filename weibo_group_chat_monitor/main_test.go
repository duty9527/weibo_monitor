package main

import (
	"reflect"
	"testing"
)

func TestParseModeArgsSubcommand(t *testing.T) {
	mode, remaining, err := parseModeArgs([]string{"groupchat", "-config", "a.yaml"})
	if err != nil {
		t.Fatalf("parseModeArgs failed: %v", err)
	}
	if mode != "groupchat" {
		t.Fatalf("unexpected mode: %s", mode)
	}
	if !reflect.DeepEqual(remaining, []string{"-config", "a.yaml"}) {
		t.Fatalf("unexpected remaining args: %#v", remaining)
	}
}

func TestParseModeArgsFlag(t *testing.T) {
	mode, remaining, err := parseModeArgs([]string{"-mode=weibo", "-config", "b.yaml"})
	if err != nil {
		t.Fatalf("parseModeArgs failed: %v", err)
	}
	if mode != "weibo" {
		t.Fatalf("unexpected mode: %s", mode)
	}
	if !reflect.DeepEqual(remaining, []string{"-config", "b.yaml"}) {
		t.Fatalf("unexpected remaining args: %#v", remaining)
	}
}

func TestParseModeArgsGroupChatHistoryAlias(t *testing.T) {
	mode, remaining, err := parseModeArgs([]string{"groupchat-history", "-config", "c.yaml"})
	if err != nil {
		t.Fatalf("parseModeArgs failed: %v", err)
	}
	if mode != "groupchat-history" {
		t.Fatalf("unexpected mode: %s", mode)
	}
	if !reflect.DeepEqual(remaining, []string{"-config", "c.yaml"}) {
		t.Fatalf("unexpected remaining args: %#v", remaining)
	}
}
