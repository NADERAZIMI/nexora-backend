package observability

import "testing"

func TestGRPCEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://localhost:4317":      "localhost:4317",
		"https://collector:4317":     "collector:4317",
		"localhost:4317":             "localhost:4317",
		"http://127.0.0.1:4317/path": "127.0.0.1:4317",
	}
	for input, want := range cases {
		if got := grpcEndpoint(input); got != want {
			t.Fatalf("grpcEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
