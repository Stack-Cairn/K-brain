package tui

func (m *model) cyclePermissionMode() {
	switch m.permissionMode {
	case "plan":
		m.setPermissionMode("always")
	case "always":
		m.setPermissionMode("normal")
	default:
		m.setPermissionMode("plan")
	}
}

func (m *model) setPermissionMode(mode string) {
	m.permissionMode = mode
	if m.agent != nil {
		m.agent.SetPlanMode(mode == "plan")
	}
	m.append(dimStyle.Render(m.tr("Mode: ") + m.permissionModeLabel()))
}

func (m *model) permissionModeLabel() string {
	switch m.permissionMode {
	case "plan":
		return m.tr("Plan")
	case "always":
		return m.tr("Always allow")
	default:
		return m.tr("Normal")
	}
}

func (m *model) permissionCommand(args []string) {
	if len(args) == 0 {
		m.cyclePermissionMode()
		return
	}
	if len(args) == 1 {
		switch args[0] {
		case "normal", "plan", "always":
			m.setPermissionMode(args[0])
			return
		}
	}
	m.append(errStyle.Render(m.tr("usage: /permissions [normal|plan|always]")))
}
