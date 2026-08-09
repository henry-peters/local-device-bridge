package daemonlock

import "testing"

func TestAcquirePreventsDuplicateAndReleases(t *testing.T) {
	path := t.TempDir() + "/daemon.lock"
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); err == nil {
		_ = first.Close()
		t.Fatal("expected duplicate daemon lock to fail")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
