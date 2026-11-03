package launcher

import (
	"slices"
	"testing"
)

func TestBuildArgv_PutsTheFixedPrefixFirst(t *testing.T) {
	argv := BuildArgv(nil, Secrets{JWT: "jwt", AdminPassword: "pw"}, "/data", 51234)
	want := []string{
		"leafwiki",
		"--data-dir=/data",
		"--jwt-secret=jwt",
		"--admin-password=pw",
		"--port=51234",
		"--allow-insecure=true",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("BuildArgv() = %v, want %v", argv, want)
	}
}

func TestBuildArgv_UsesTheBoundPortAndDropsEmptyValues(t *testing.T) {
	params := []Param{
		{Name: "port", Kind: KindString, Value: "8081"},
		{Name: "base-path", Kind: KindString, Value: ""},
		{Name: "snapshot", Kind: KindBool, Value: "false"},
		{Name: "editor-limit", Kind: KindInt, Value: "7"},
		{Name: "custom-stylesheet", Kind: KindString, Value: "/tmp/a b.css"},
		{Name: "weird", Kind: KindUnsupported, Value: "x"},
		{Name: "jwt-secret", Kind: KindString, Value: "leak"},
	}

	argv := BuildArgv(params, Secrets{JWT: "jwt", AdminPassword: "pw"}, "/data", 51234)

	for _, unexpected := range []string{"--port=8081", "--base-path=", "--weird=x", "--jwt-secret=leak"} {
		if slices.Contains(argv, unexpected) {
			t.Fatalf("argv contains %q: %v", unexpected, argv)
		}
	}
	if !slices.Contains(argv, "--port=51234") {
		t.Fatalf("argv misses the bound port: %v", argv)
	}
	if !slices.Contains(argv, "--snapshot=false") || !slices.Contains(argv, "--editor-limit=7") {
		t.Fatalf("argv misses the changed values: %v", argv)
	}
	if !slices.Contains(argv, "--custom-stylesheet=/tmp/a b.css") {
		t.Fatalf("argv should pass a spaced value as one element: %v", argv)
	}

	// Keys are sorted after the fixed prefix.
	rest := argv[6:]
	for i := 1; i < len(rest); i++ {
		if rest[i-1] > rest[i] {
			t.Fatalf("parameters are not sorted: %v", rest)
		}
	}
}
