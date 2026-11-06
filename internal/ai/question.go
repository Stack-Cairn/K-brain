package ai

// QuestionAnswer is the user's answer to one question of a question tool call
// (question, AskUserQuestion). The /v1 protocol uses this shape for answers.
type QuestionAnswer struct {
	Prompt     string `json:"prompt,omitempty"`
	QuestionID string `json:"question_id"`
	// SelectedLabel is the user's own words when Custom is set; otherwise the
	// chosen option's label, or, with SelectedLabels, the chosen labels joined
	// by ", " for display.
	SelectedLabel string `json:"selected_label"`
	// SelectedLabels are the chosen options, each exactly one of the
	// question's labels, in option order. Set whenever the answer carries a
	// structured selection; Custom may add the user's own words beside them.
	SelectedLabels []string `json:"selected_labels,omitempty"`
	Custom         bool     `json:"custom,omitempty"`
}
