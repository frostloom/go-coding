package registry

import (
	"context"
	"strings"
	"testing"
)

func TestRegisterListCall(t *testing.T) {
	r := New()
	var gotArgs map[string]any
	if err := r.Register(ToolDef{
		Name:        "echo",
		Description: "echo args back",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{"msg": map[string]any{"type": "string"}},
		},
		Run: func(_ context.Context, args map[string]any) (any, error) {
			gotArgs = args
			return args["msg"], nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi" {
		t.Errorf("call returned %v, want hi", out)
	}
	if gotArgs["msg"] != "hi" {
		t.Errorf("handler args = %v", gotArgs)
	}
	list := r.List()
	if len(list) != 1 || list[0].Name != "echo" {
		t.Errorf("list = %v", list)
	}
}

func TestRegisterDuplicateFails(t *testing.T) {
	r := New()
	run := func(_ context.Context, _ map[string]any) (any, error) { return nil, nil }
	if err := r.Register(ToolDef{Name: "x", Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ToolDef{Name: "x", Run: run}); err == nil {
		t.Error("duplicate registration must fail")
	}
}

func TestCallUnknownFails(t *testing.T) {
	r := New()
	if _, err := r.Call(context.Background(), "nope", nil); err == nil {
		t.Error("unknown tool call must fail")
	}
}

func TestDescribeAllEmptyIsEmpty(t *testing.T) {
	if s := DescribeAll(New().List()); s != "" {
		t.Errorf("empty registry should render nothing, got %q", s)
	}
}

func TestDescribeAllRendersNameAndSchema(t *testing.T) {
	r := New()
	_ = r.Register(ToolDef{
		Name:        "ping",
		Description: "ping a host",
		Schema: map[string]any{
			"type":     "object",
			"properties": map[string]any{"host": map[string]any{"type": "string"}},
			"required":  []any{"host"},
		},
		Run: func(_ context.Context, _ map[string]any) (any, error) { return "pong", nil },
	})
	s := DescribeAll(r.List())
	if !strings.Contains(s, "ping") || !strings.Contains(s, "host: string (required)") {
		t.Errorf("describe should render name + schema, got:\n%s", s)
	}
}
