package main

import (
	"strings"
	"testing"
)

// withTempPaths 把全局路径指向临时目录，供 SubStore 落盘测试使用。
func withTempPaths(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := P
	P = &Paths{AppDest: dir, Etc: dir, Var: dir, Tmp: dir, Home: dir}
	t.Cleanup(func() { P = old })
}

// TestDecideSwitchOrder 自动切换的备选顺序：从当前订阅的下一个开始，绕列表一圈；
// 因此「排序靠上」的订阅优先成为备选。
func TestDecideSwitchOrder(t *testing.T) {
	// 5 个订阅，当前第 1 个不可用，只有第 2 个和第 5 个可用 → 应切到第 2 个（不是第 5 个）
	usable := map[int]bool{1: true, 4: true}
	if got := decideSwitch(0, 5, func(i int) bool { return usable[i] }); got != 1 {
		t.Fatalf("期望切到下标 1（第 2 个订阅），实际 %d", got)
	}
	// 当前是第 2 个，只有第 5 个可用 → 从第 3 个开始依次找到第 5 个
	usable2 := map[int]bool{4: true}
	if got := decideSwitch(1, 5, func(i int) bool { return usable2[i] }); got != 4 {
		t.Fatalf("期望切到下标 4，实际 %d", got)
	}
	// 只有一个订阅 → 无从切换
	if got := decideSwitch(0, 1, func(i int) bool { return true }); got != -1 {
		t.Fatalf("单订阅不应切换，实际 %d", got)
	}
	// 其余全部不可用 → 返回 -1（保持原订阅）
	if got := decideSwitch(0, 3, func(i int) bool { return false }); got != -1 {
		t.Fatalf("全部不可用应返回 -1，实际 %d", got)
	}
}

// TestSubStoreSingleActivate 同一时间只能有一个订阅激活；排序持久化。
func TestSubStoreSingleActivate(t *testing.T) {
	withTempPaths(t)
	s := newSubStore()
	a, _ := s.Add("A", "https://a")
	b, _ := s.Add("B", "https://b")
	c, _ := s.Add("C", "https://c")
	if s.Active() != nil {
		t.Fatal("新增订阅默认不应处于激活状态")
	}
	if _, err := s.Activate(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if x := s.Active(); x == nil || x.ID != a.ID {
		t.Fatal("应激活 A")
	}
	if _, err := s.Activate(c.ID, true); err != nil {
		t.Fatal(err)
	}
	if x := s.Active(); x == nil || x.ID != c.ID {
		t.Fatal("激活 C 后应当只有 C 处于激活状态")
	}
	for _, x := range s.List() {
		if x.ID != c.ID && x.Enabled {
			t.Fatalf("订阅 %s 不应仍处于激活状态", x.Name)
		}
	}
	// 取消激活
	if _, err := s.Activate(c.ID, false); err != nil {
		t.Fatal(err)
	}
	if s.Active() != nil {
		t.Fatal("取消激活后不应有激活订阅")
	}
	// 排序并持久化
	if err := s.Reorder([]string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if got := subNames(s); got != "C,A,B" {
		t.Fatalf("排序结果错误：%s", got)
	}
	if got := subNames(newSubStore()); got != "C,A,B" {
		t.Fatalf("排序未持久化：%s", got)
	}
}

// TestSubStoreNormalize 旧数据（多个启用）迁移后只保留最靠前的一个激活订阅。
func TestSubStoreNormalize(t *testing.T) {
	withTempPaths(t)
	s := newSubStore()
	a, _ := s.Add("A", "u")
	b, _ := s.Add("B", "u")
	c, _ := s.Add("C", "u")
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if _, err := s.Update(id, func(x *Subscription) { x.Enabled = true }); err != nil {
			t.Fatal(err)
		}
	}
	dropped, err := s.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 2 {
		t.Fatalf("应取消激活 2 个订阅，实际 %v", dropped)
	}
	if x := s.Active(); x == nil || x.ID != a.ID {
		t.Fatal("应保留列表中最靠前的订阅 A 处于激活状态")
	}
	for _, x := range s.List() {
		if x.ID != a.ID && x.Enabled {
			t.Fatalf("订阅 %s 不应仍处于激活状态", x.Name)
		}
	}
}

func subNames(s *SubStore) string {
	out := []string{}
	for _, x := range s.List() {
		out = append(out, x.Name)
	}
	return strings.Join(out, ",")
}
