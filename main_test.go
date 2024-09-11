package main_test

import "testing"

func add(a, b int) int {
	return a + b
}

func BenchmarkDummy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		add(1, 2)
	}
}

func BenchmarkSummy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b := add(1, 2)
		c := add(1, 2)
		e := add(b, i)
		f := add(c, i)
		g := add(e, f)
		h := add(g, i)
		_ = h
	}
}

func TestDummy(t *testing.T) {
	if add(1, 2) != 3 {
		t.Error("1 + 2 != 3")
	}
	t.Fail()
}
