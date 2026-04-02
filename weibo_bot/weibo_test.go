package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/playwright-community/playwright-go"
)

func TestExtractWeiboID(t *testing.T) {
	cases := map[string]string{
		"https://weibo.com/1401527553/NB4vXy3aP":       "NB4vXy3aP",
		"https://weibo.com/detail/4962291583582458":    "4962291583582458",
		"https://m.weibo.cn/status/NB4vXy3aP":          "NB4vXy3aP",
		"https://weibo.com/u/123?mid=5033881810701546": "5033881810701546",
	}

	for input, want := range cases {
		got, err := extractWeiboID(input)
		if err != nil {
			t.Fatalf("extractWeiboID(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("extractWeiboID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanHTML(t *testing.T) {
	input := "<p>Hello<br />World</p>&nbsp;<a href=\"https://weibo.com\">link</a>"
	want := "Hello\nWorld\n link"
	if got := cleanHTML(input); got != want {
		t.Fatalf("cleanHTML() = %q, want %q", got, want)
	}
}

func TestSplitText(t *testing.T) {
	got := splitText("第一行\n第二行\n第三行", 5)
	want := []string{"第一行", "第二行", "第三行"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitText() = %#v, want %#v", got, want)
	}
}

func TestSerializeCookies(t *testing.T) {
	cookies := []playwright.Cookie{
		{Name: "SUB", Value: "a"},
		{Name: "SUBP", Value: "b"},
		{Name: "SUB", Value: "override-ignored"},
	}
	want := "SUB=a; SUBP=b"
	if got := serializeCookies(cookies); got != want {
		t.Fatalf("serializeCookies() = %q, want %q", got, want)
	}
}

func TestFlexibleStringUnmarshal(t *testing.T) {
	var got FlexibleString
	if err := json.Unmarshal([]byte(`"video"`), &got); err != nil {
		t.Fatalf("unmarshal string failed: %v", err)
	}
	if got != "video" {
		t.Fatalf("got %q, want %q", got, "video")
	}

	if err := json.Unmarshal([]byte(`123`), &got); err != nil {
		t.Fatalf("unmarshal number failed: %v", err)
	}
	if got != "123" {
		t.Fatalf("got %q, want %q", got, "123")
	}
}
