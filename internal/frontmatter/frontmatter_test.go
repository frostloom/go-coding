package frontmatter

import "testing"

func TestParseFrontmatter(t *testing.T) {
	meta, body := Parse(`---
name: review
description: Review Go code against a checklist
---
Follow the checklist step by step.`)
	if meta["name"] != "review" {
		t.Errorf("name = %q, want review", meta["name"])
	}
	if meta["description"] != "Review Go code against a checklist" {
		t.Errorf("description = %q", meta["description"])
	}
	if want := "Follow the checklist step by step."; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	meta, body := Parse("just prose")
	if meta != nil {
		t.Errorf("meta should be nil, got %v", meta)
	}
	if body != "just prose" {
		t.Errorf("body = %q", body)
	}
}

func TestParseSkipsCommentsAndBlanks(t *testing.T) {
	meta, _ := Parse("---\n# a comment\n\ndescription: x: with colon\n---\nbody")
	if meta["description"] != "x: with colon" {
		t.Errorf("description = %q, want 'x: with colon'", meta["description"])
	}
}

func TestParseMalformedFallsBack(t *testing.T) {
	// "---" without a closing block: treat as body.
	meta, body := Parse("---\nname: broken\n")
	if meta != nil {
		t.Errorf("meta should be nil for unterminated frontmatter")
	}
	if body == "" {
		t.Error("body should not be empty")
	}
}
