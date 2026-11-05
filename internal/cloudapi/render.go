package cloudapi

// Button kinds, which decide the webhook a tap produces.
const (
	ButtonKindReply      = "reply"
	ButtonKindQuickReply = "quick_reply"
	ButtonKindLink       = "link"
)

// Rendered is a message as the simulated WhatsApp user sees it. The console
// shows this instead of re-deriving it from each request shape.
type Rendered struct {
	TemplateName string           `json:"template_name,omitempty"`
	Header       string           `json:"header,omitempty"`
	Body         string           `json:"body,omitempty"`
	Footer       string           `json:"footer,omitempty"`
	Buttons      []RenderedButton `json:"buttons,omitempty"`
	List         *RenderedList    `json:"list,omitempty"`
}

// RenderedButton is one tappable button. ID is what the tap reports back.
type RenderedButton struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// RenderedList is a list message's opener and its sections.
type RenderedList struct {
	Button   string            `json:"button"`
	Sections []RenderedSection `json:"sections"`
}

// RenderedSection is one group of list rows.
type RenderedSection struct {
	Title string    `json:"title,omitempty"`
	Rows  []ListRow `json:"rows"`
}

// RenderText renders a text message.
func RenderText(text TextContent) Rendered {
	return Rendered{Body: text.Body}
}

// RenderInteractive renders a reply-button or list message.
func RenderInteractive(interactive Interactive) Rendered {
	rendered := Rendered{}
	if interactive.Header != nil {
		rendered.Header = interactive.Header.Text
		if interactive.Header.Type != "text" {
			rendered.Header = "[" + interactive.Header.Type + "]"
		}
	}
	if interactive.Body != nil {
		rendered.Body = interactive.Body.Text
	}
	if interactive.Footer != nil {
		rendered.Footer = interactive.Footer.Text
	}
	for _, button := range interactive.Action.Buttons {
		rendered.Buttons = append(rendered.Buttons, RenderedButton{
			ID:    button.Reply.ID,
			Title: button.Reply.Title,
			Kind:  ButtonKindReply,
		})
	}
	if interactive.Type == interactiveList {
		list := &RenderedList{Button: interactive.Action.Button}
		for _, section := range interactive.Action.Sections {
			list.Sections = append(list.Sections, RenderedSection(section))
		}
		rendered.List = list
	}
	return rendered
}

// FindButton returns the tappable button with this id.
func (r Rendered) FindButton(id string) (RenderedButton, bool) {
	for _, button := range r.Buttons {
		if button.ID == id && button.Kind != ButtonKindLink {
			return button, true
		}
	}
	return RenderedButton{}, false
}

// FindRow returns the list row with this id.
func (r Rendered) FindRow(id string) (ListRow, bool) {
	if r.List == nil {
		return ListRow{}, false
	}
	for _, section := range r.List.Sections {
		for _, row := range section.Rows {
			if row.ID == id {
				return row, true
			}
		}
	}
	return ListRow{}, false
}
