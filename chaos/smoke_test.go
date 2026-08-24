package chaos

import "testing"

func TestFillDeterministic(t *testing.T) {
	a := make([]byte, 64)
	b := make([]byte, 64)
	Fill(a, 42)
	Fill(b, 42)
	if string(a) != string(b) {
		t.Fatal("non-deterministic")
	}
	t.Logf("sample: %q", a[:24])
}
