package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ChromeTab struct {
	Window int    `json:"window"`
	Index  int    `json:"index"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

func ChromeActive() (url, title string, err error) {
	out, err := Tell("Google Chrome",
		`set theUrl to URL of active tab of front window`,
		`set theTitle to title of active tab of front window`,
		`return theUrl & "\n" & theTitle`)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(out, "\n", 2)
	url = parts[0]
	if len(parts) > 1 {
		title = parts[1]
	}
	return url, title, nil
}

func ChromeTabs() ([]ChromeTab, error) {

	out, err := Tell("Google Chrome", `
set out to ""
set wCount to count windows
repeat with w from 1 to wCount
  set tCount to count tabs of window w
  repeat with t from 1 to tCount
    set theTab to tab t of window w
    set out to out & w & "￨" & t & "￨" & (URL of theTab) & "￨" & (title of theTab) & "
"
  end repeat
end repeat
return out`)
	if err != nil {
		return nil, err
	}
	var tabs []ChromeTab
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(line, "￨", 4)
		if len(f) != 4 {
			continue
		}
		var w, i int
		_, _ = fmt.Sscanf(f[0], "%d", &w)
		_, _ = fmt.Sscanf(f[1], "%d", &i)
		tabs = append(tabs, ChromeTab{Window: w, Index: i, URL: f[2], Title: f[3]})
	}
	return tabs, nil
}

func ChromeGoto(url string) error {
	_, err := Tell("Google Chrome",
		`set URL of active tab of front window to `+quote(url))
	return err
}

func ChromeNewTab(url string) error {
	_, err := Tell("Google Chrome",
		`if (count windows) is 0 then make new window`,
		`make new tab at end of tabs of front window with properties {URL:`+quote(url)+`}`,
		`activate`)
	return err
}

func ChromeActivateTab(window, index int) error {
	_, err := Tell("Google Chrome",
		fmt.Sprintf(`set active tab index of window %d to %d`, window, index),
		fmt.Sprintf(`set index of window %d to 1`, window),
		`activate`)
	return err
}

func ChromeCloseTab(window, index int) error {
	_, err := Tell("Google Chrome",
		fmt.Sprintf(`close tab %d of window %d`, index, window))
	return err
}

func ChromeBack() error {
	_, err := Tell("Google Chrome", `tell active tab of front window to go back`)
	return err
}

func ChromeReload() error {
	_, err := Tell("Google Chrome", `tell active tab of front window to reload`)
	return err
}

var ErrJSFromAppleEvents = errors.New("chrome's 'Allow JavaScript from Apple Events' is off — enable it in Chrome: View → Developer → Allow JavaScript from Apple Events")

func ChromeJS(js string) (string, error) {
	out, err := Tell("Google Chrome",
		`tell active tab of front window to execute javascript `+quote(js))
	if err != nil && (strings.Contains(err.Error(), "execute javascript") || strings.Contains(err.Error(), "not allowed") || strings.Contains(err.Error(), "1743") || strings.Contains(err.Error(), "Allow JavaScript")) {
		return "", ErrJSFromAppleEvents
	}
	return out, err
}

func ChromeFindTab(urlPart string) (*ChromeTab, error) {
	tabs, err := ChromeTabs()
	if err != nil {
		return nil, err
	}
	for _, t := range tabs {
		if strings.Contains(t.URL, urlPart) {
			return &t, nil
		}
	}
	return nil, nil
}

func ChromeState() (string, error) {
	url, title, err := ChromeActive()
	if err != nil {
		return "", err
	}
	tabs, err := ChromeTabs()
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(map[string]any{
		"active": ChromeTab{URL: url, Title: title},
		"tabs":   tabs,
	})
	return string(data), nil
}
