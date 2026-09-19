package tui

import "slices"

func (m *model) clearQueuedTools() {
	changed := false
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind != blockToolQueued {
			continue
		}
		m.blocks = slices.Delete(m.blocks, i, i+1)
		for j, at := range m.msgBlock {
			switch {
			case at == i:
				m.msgBlock[j] = -1
			case at > i:
				m.msgBlock[j]--
			}
		}
		changed = true
	}
	if changed {
		m.refreshVP()
	}
}
