package materialbalance

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

// Book 是物料衡算的协调器：管理按进料源切换/停机切开的核算窗口，
// 路由计量读数，并以追加方式保留迟到读数引发的修订。单一互斥锁保护全部状态。
type Book struct {
	mu        sync.Mutex
	windows   []*window
	tolerance float64
}

// New 创建使用默认容差的衡算账簿。
func New() *Book { return &Book{tolerance: DefaultTolerance} }

// NewWithTolerance 创建指定容差（绝对量，>=0）的账簿。
func NewWithTolerance(tolerance float64) *Book {
	b := New()
	b.Configure(tolerance)
	return b
}

// Configure 调整损失判定容差；负值被忽略。已冻结的原结论不受影响。
func (b *Book) Configure(tolerance float64) {
	b.mu.Lock()
	if tolerance >= 0 {
		b.tolerance = tolerance
	}
	b.mu.Unlock()
}

// Tolerance 返回当前容差。
func (b *Book) Tolerance() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tolerance
}

// OpenWindow 在换进料源/开车时切开一个新窗口。上一窗必须已经结算；
// 上一窗的期末设备存量自动结转为本窗期初存量。
func (b *Book) OpenWindow(at int64, source string, feedRevision uuid.UUID, c model.Campaign) (Meta, error) {
	if source == "" {
		return Meta{}, ErrSourceRequired
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.windows); n > 0 && !b.windows[n-1].meta.Settled {
		return Meta{}, ErrWindowAlreadyOpen
	}
	var carry *float64
	if n := len(b.windows); n > 0 {
		carry = b.windows[n-1].endingCarry()
	}
	w := newWindow(len(b.windows)+1, at, source, feedRevision, c, carry)
	b.windows = append(b.windows, w)
	return w.meta, nil
}

// CloseWindow 在换进料源/停机时结算当前窗口。无窗口或已结算时 ok=false（幂等空操作）。
func (b *Book) CloseWindow(at int64) (Meta, Balance, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.windows)
	if n == 0 {
		return Meta{}, Balance{}, false
	}
	w := b.windows[n-1]
	bal, froze := w.settle(at, b.tolerance)
	if !froze {
		return Meta{}, Balance{}, false
	}
	return w.meta, bal, true
}

// Add 接收一条计量读数并按时间戳路由：
//   - 早于首窗开窗时刻 → ErrBeforeFirstWindow；
//   - 落在打开窗口 → 计入实时衡算；
//   - 落在已结算窗口 → 重算并追加一条修订（原结论保留）；
//   - 晚于末窗（停机后）关窗时刻 → ErrAfterLastWindow。
func (b *Book) Add(r Reading) (Acknowledgement, error) {
	if err := validateReading(r); err != nil {
		return Acknowledgement{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.windows) == 0 || r.At < b.windows[0].meta.OpenedAt {
		return Acknowledgement{}, ErrBeforeFirstWindow
	}
	last := len(b.windows) - 1
	target := -1
	for i, w := range b.windows {
		if w.contains(r.At, i == last) {
			target = i
			break
		}
	}
	if target == -1 {
		// 首窗之前已在上面排除；走到这里只可能晚于已关闭的末窗。
		return Acknowledgement{}, ErrAfterLastWindow
	}
	w := b.windows[target]
	if err := w.add(r); err != nil {
		return Acknowledgement{}, err
	}
	ack := Acknowledgement{Sequence: w.meta.Sequence, Settled: w.meta.Settled}
	if w.meta.Settled {
		w.appendRevision(time.Now().Unix(), r, b.tolerance)
		ack.Revised = true
	}
	return ack, nil
}

// Window 返回指定序号（从 1 起）窗口的视图。
func (b *Book) Window(seq int) (WindowView, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq < 1 || seq > len(b.windows) {
		return WindowView{}, false
	}
	return b.windows[seq-1].view(b.tolerance), true
}

// Latest 返回最近一个窗口的视图。
func (b *Book) Latest() (WindowView, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.windows) == 0 {
		return WindowView{}, false
	}
	return b.windows[len(b.windows)-1].view(b.tolerance), true
}

// Windows 返回全部窗口（按时间顺序）的视图。
func (b *Book) Windows() []WindowView {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]WindowView, 0, len(b.windows))
	for _, w := range b.windows {
		out = append(out, w.view(b.tolerance))
	}
	return out
}

// Revisions 返回某窗口的全部迟到修订。
func (b *Book) Revisions(seq int) []Revision {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq < 1 || seq > len(b.windows) {
		return nil
	}
	return append([]Revision(nil), b.windows[seq-1].revisions...)
}

// RevisionCount 返回所有窗口的修订总数。
func (b *Book) RevisionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0
	for _, w := range b.windows {
		total += len(w.revisions)
	}
	return total
}

// Summary 汇总物料衡算状态，供运行状态接口展示。
func (b *Book) Summary() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	settled := 0
	for _, w := range b.windows {
		if w.meta.Settled {
			settled++
		}
	}
	summary := map[string]any{
		"windows":   len(b.windows),
		"settled":   settled,
		"open":      len(b.windows) > 0 && !b.windows[len(b.windows)-1].meta.Settled,
		"revisions": b.RevisionCountLocked(),
	}
	if len(b.windows) > 0 {
		summary["latest"] = b.windows[len(b.windows)-1].view(b.tolerance)
	} else {
		summary["latest"] = nil
	}
	return summary
}

// RevisionCountLocked 在已持锁时统计修订数，避免 Summary 重复加锁。
func (b *Book) RevisionCountLocked() int {
	total := 0
	for _, w := range b.windows {
		total += len(w.revisions)
	}
	return total
}
