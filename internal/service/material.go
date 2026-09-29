package service

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/material"
	"github.com/wyw14/solo-039/internal/model"
)

type readingPayload struct {
	Point    string  `json:"point"`
	Value    float64 `json:"value"`
	At       int64   `json:"at"`
	Recorded int64   `json:"recorded"`
	Seq      int64   `json:"seq"`
}

type windowPayload struct {
	Reason       string `json:"reason"`
	Source       string `json:"source"`
	At           int64  `json:"at"`
	Campaign     string `json:"campaign"`
	Revision     string `json:"revision"`
	FeedRevision string `json:"feed_revision"`
}

type correctionPayload struct {
	Seq      int64    `json:"seq"`
	Value    float64  `json:"value"`
	At       int64    `json:"at"`
	Recorded int64    `json:"recorded"`
	Reason   string   `json:"reason"`
	Windows  []string `json:"windows"`
}

// beginMaterialWindow 切开衡算窗口（开车/换进料源），并持久化窗口事件。
func (p *Plant) beginMaterialWindow(current model.Campaign, source, reason string) error {
	at := p.now()
	if _, err := p.Material.Begin(current.ID, current.Revision, source, p.Feed.Current.Revision, at, reason); err != nil {
		return err
	}
	payload, _ := json.Marshal(windowPayload{
		Reason: reason, Source: source, At: at,
		Campaign: current.ID.String(), Revision: current.Revision.String(),
		FeedRevision: p.Feed.Current.Revision.String(),
	})
	return p.record("material.window", string(payload))
}

// closeMaterialWindow 在停机时结账当前衡算窗口，并持久化窗口事件。
func (p *Plant) closeMaterialWindow() error {
	at := p.now()
	if _, err := p.Material.Close(at); err != nil {
		return err
	}
	payload, _ := json.Marshal(windowPayload{Reason: material.ReasonShutdown, At: at})
	return p.record("material.window", string(payload))
}

// RecordMaterialReading 登记接收进料、塔顶、塔底或设备存量计量点读数。
// 读数按测量时刻归入对应衡算时段；补录已结账时段的读数即迟到修订。
// 返回读数与命中的窗口编号（边界读数可能同时命中两个窗口）。
func (p *Plant) RecordMaterialReading(point string, value float64, at int64) (material.Reading, []string, error) {
	mp, err := material.ParsePoint(point)
	if err != nil {
		return material.Reading{}, nil, err
	}
	if at == 0 {
		at = p.now()
	}
	recorded := p.now()
	reading, windowIDs, err := p.Material.RecordReading(mp, value, at, recorded)
	if err != nil {
		return material.Reading{}, nil, err
	}
	payload, _ := json.Marshal(readingPayload{Point: point, Value: value, At: at, Recorded: recorded, Seq: reading.Seq})
	if err := p.record("material.reading", string(payload)); err != nil {
		return material.Reading{}, nil, err
	}
	return reading, windowIDs, nil
}

// CorrectMaterialReading 更正既有计量读数；受影响时段（含切窗边界
// 共享的相邻时段）全部重算，窗口关闭时冻结的原结论保留。
func (p *Plant) CorrectMaterialReading(seq int64, value float64, reason string) ([]material.Balance, error) {
	original, ok := p.Material.Reading(seq)
	if !ok {
		return nil, errors.New("reading not found")
	}
	recorded := p.now()
	affected, err := p.Material.CorrectReading(seq, value, recorded, reason)
	if err != nil {
		return nil, err
	}
	windows := make([]string, 0, len(affected))
	for _, b := range affected {
		windows = append(windows, b.Window)
	}
	payload, _ := json.Marshal(correctionPayload{Seq: seq, Value: value, At: original.At, Recorded: recorded, Reason: reason, Windows: windows})
	if err := p.record("material.correction", string(payload)); err != nil {
		return nil, err
	}
	return affected, nil
}

// MaterialBalances 返回全部衡算时段（含已冻结的原结论与修订痕迹）。
func (p *Plant) MaterialBalances() []material.Balance { return p.Material.Balances() }

// rebuildMaterial 从持久事件重建物料衡算台账，
// 使迟到读数修订与切窗结论在进程重启后仍然可查。
func (p *Plant) rebuildMaterial(events []model.Event) error {
	ledger, err := RebuildMaterialLedger(events)
	if err != nil {
		return err
	}
	p.Material = ledger
	return nil
}

// RebuildMaterialLedger 按事件流重放物料衡算窗口、读数与修订。
func RebuildMaterialLedger(events []model.Event) (*material.Ledger, error) {
	ledger := material.NewLedger()
	for _, event := range events {
		switch event.Kind {
		case "material.window":
			var payload windowPayload
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				return nil, err
			}
			if payload.Reason == material.ReasonShutdown {
				if _, err := ledger.Close(payload.At); err != nil {
					return nil, err
				}
				continue
			}
			campaignID, err := uuid.Parse(payload.Campaign)
			if err != nil {
				return nil, err
			}
			revision, err := uuid.Parse(payload.Revision)
			if err != nil {
				return nil, err
			}
			feedRevision, err := uuid.Parse(payload.FeedRevision)
			if err != nil {
				return nil, err
			}
			if _, err := ledger.Begin(campaignID, revision, payload.Source, feedRevision, payload.At, payload.Reason); err != nil {
				return nil, err
			}
		case "material.reading":
			var payload readingPayload
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				return nil, err
			}
			mp, err := material.ParsePoint(payload.Point)
			if err != nil {
				return nil, err
			}
			if _, _, err := ledger.RecordReading(mp, payload.Value, payload.At, payload.Recorded); err != nil {
				return nil, err
			}
		case "material.correction":
			var payload correctionPayload
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				return nil, err
			}
			if _, err := ledger.CorrectReading(payload.Seq, payload.Value, payload.Recorded, payload.Reason); err != nil {
				return nil, err
			}
		}
	}
	return ledger, nil
}
