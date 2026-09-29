package service

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-039/internal/materialbalance"
)

// closeMaterialWindow 结算当前物料衡算窗口并记录事件；无窗口或已结算时为空操作。
// 账簿内的窗口结算不会失败，记录失败仅记入故障，不中断调用方的切换/停机流程。
func (p *Plant) closeMaterialWindow() {
	meta, balance, ok := p.MaterialBalance.CloseWindow(time.Now().Unix())
	if !ok {
		return
	}
	if err := p.record("material.window.closed", fmt.Sprintf("%d:%.4f:%s", meta.Sequence, balance.Loss, balance.Verdict)); err != nil {
		p.fail(err)
	}
}

// AddMaterialReading 接收一个计量点读数并按时间戳计入相应核算窗口。
// at 为 0 时取当前 unix 秒；读数可能实时计入打开窗口，也可能修订已结算窗口。
func (p *Plant) AddMaterialReading(point string, value float64, at int64) (materialbalance.Acknowledgement, error) {
	pointID, err := materialbalance.ParsePoint(point)
	if err != nil {
		return materialbalance.Acknowledgement{}, err
	}
	if at == 0 {
		at = time.Now().Unix()
	}
	ack, err := p.MaterialBalance.Add(materialbalance.Reading{Point: pointID, Value: value, At: at})
	if err != nil {
		return materialbalance.Acknowledgement{}, err
	}
	if err := p.record("material.reading", fmt.Sprintf("%s:%d:%.4f:%d:%t", point, at, value, ack.Sequence, ack.Revised)); err != nil {
		return materialbalance.Acknowledgement{}, err
	}
	return ack, nil
}

// MaterialWindow 返回指定序号（从 1 起）的物料衡算窗口视图。
func (p *Plant) MaterialWindow(seq int) (materialbalance.WindowView, bool) {
	return p.MaterialBalance.Window(seq)
}

// LatestMaterialWindow 返回最近一个物料衡算窗口视图。
func (p *Plant) LatestMaterialWindow() (materialbalance.WindowView, bool) {
	return p.MaterialBalance.Latest()
}

// MaterialWindows 返回全部物料衡算窗口（按时间顺序）。
func (p *Plant) MaterialWindows() []materialbalance.WindowView {
	return p.MaterialBalance.Windows()
}

// MaterialRevisionCount 返回所有窗口的迟到修订总数。
func (p *Plant) MaterialRevisionCount() int { return p.MaterialBalance.RevisionCount() }
