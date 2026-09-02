package lock

import "testing"

func TestOperationLockIsExclusive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(); err == nil {
		t.Fatal("第二个写操作本应被锁拒绝")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire()
	if err != nil {
		t.Fatalf("释放后应能重新获取锁: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
