package config

func (c *Config) RemoveProvider(name string) {
	delete(c.Providers, name)
	for id, m := range c.Models {
		var keep []string
		for _, p := range m.Providers {
			if p != name {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			delete(c.Models, id)
			continue
		}
		m.Providers = keep
		c.Models[id] = m
	}
	for _, pin := range []*string{&c.DefaultProvider, &c.CompactProvider, &c.TaskProvider} {
		if *pin == name {
			*pin = ""
		}
	}

	for _, pin := range []*string{&c.DefaultModel, &c.CompactModel, &c.TaskModel} {
		if _, ok := c.Models[*pin]; *pin != "" && !ok {
			*pin = ""
		}
	}

	c.allowEmptySave = len(c.Providers) == 0 && len(c.Models) == 0
	cats := LoadCatalogs()
	if _, ok := cats[name]; ok {
		delete(cats, name)
		_ = SaveCatalogs(cats)
	}
	logf("config.provider", "removed provider %s", name)
}
