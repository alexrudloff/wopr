package codingagent

import (
	"cmp"

	"github.com/alexrudloff/wopr/tui"
)

// ChatViewportOptions are the components of the fullscreen layout.
// WidgetsAbove, WidgetsBelow and Sidebar are optional (nil omits the slot);
// Scrollbar "" takes the "auto" default; nil styles take the ScrollView
// defaults.
type ChatViewportOptions struct {
	Document        tui.Component
	PendingMessages tui.Component
	Status          tui.Component
	Editor          tui.Component
	Footer          tui.Component
	WidgetsAbove    tui.Component
	WidgetsBelow    tui.Component
	Sidebar         tui.Component
	// SidebarVisible decides the sidebar's visibility at a terminal width;
	// nil shows it on terminals wider than 120 columns.
	SidebarVisible      func(width int) bool
	Scrollbar           string
	ScrollbarTrackStyle func(text string) string
	ScrollbarThumbStyle func(text string) string
}

// ChatViewport is the fullscreen layout root and its transcript scroll view.
type ChatViewport struct {
	Root       tui.Component
	Transcript *tui.ScrollView
}

// Fullscreen layout geometry.
const (
	// viewportGutter is the blank column band on each side of the main column.
	viewportGutter = 2
	// SidebarWidth is the sidebar's column count.
	SidebarWidth = 42
	// sidebarMinTerminalWidth is the terminal width the sidebar needs to be
	// shown beside the transcript; narrower terminals hide it.
	sidebarMinTerminalWidth = 121
)

// CreateChatViewport builds the fullscreen layout: a main column with the
// transcript scrolling above a fixed input dock, inset by a gutter on each
// side, and an optional sidebar on the right on wide terminals.
func CreateChatViewport(options ChatViewportOptions) ChatViewport {
	scrollbar := cmp.Or(options.Scrollbar, "auto")
	transcript := tui.NewScrollView(tui.NewContainer(tui.NewSpacer(1), options.Document), tui.ScrollViewOptions{
		Follow:              "end",
		Primary:             true,
		Overscroll:          "chain",
		Scrollbar:           scrollbar,
		ScrollbarTrackStyle: options.ScrollbarTrackStyle,
		ScrollbarThumbStyle: options.ScrollbarThumbStyle,
	})
	shrinking := func(component tui.Component, minSize int) tui.StackChild {
		return tui.StackChild{Component: component, Shrink: new(1), MinSize: new(minSize)}
	}
	fixed := func(component tui.Component, size int) tui.StackChild {
		return tui.StackChild{Component: component, Basis: new(size), Grow: new(0), Shrink: new(0)}
	}
	dockChildren := []tui.StackChild{
		shrinking(options.PendingMessages, 0),
		shrinking(options.Status, 0),
	}
	if options.WidgetsAbove != nil {
		dockChildren = append(dockChildren, shrinking(options.WidgetsAbove, 0))
	}
	dockChildren = append(dockChildren, shrinking(options.Editor, 3))
	if options.WidgetsBelow != nil {
		dockChildren = append(dockChildren, shrinking(options.WidgetsBelow, 0))
	}
	if options.Footer != nil {
		dockChildren = append(dockChildren, shrinking(options.Footer, 0))
	}
	dock := tui.NewVStack(dockChildren, tui.StackOptions{})
	column := tui.NewVStack([]tui.StackChild{
		{Component: transcript, Basis: new(0), Grow: new(1), Shrink: new(1), MinSize: new(1)},
		fixed(tui.NewSpacer(1), 1),
		{Component: dock, Grow: new(0), Shrink: new(1), MinSize: new(1)},
		fixed(tui.NewSpacer(1), 1),
	}, tui.StackOptions{})
	main := tui.NewHStack([]tui.StackChild{
		fixed(tui.NewSpacer(0), viewportGutter),
		{Component: column, Basis: new(0), Grow: new(1), Shrink: new(1), MinSize: new(1)},
		fixed(tui.NewSpacer(0), viewportGutter),
	}, tui.StackOptions{})
	rootChildren := []tui.StackChild{
		{Component: main, Basis: new(0), Grow: new(1), Shrink: new(1), MinSize: new(1)},
	}
	if options.Sidebar != nil {
		sidebar := fixed(options.Sidebar, SidebarWidth)
		visible := options.SidebarVisible
		if visible == nil {
			visible = func(width int) bool { return width >= sidebarMinTerminalWidth }
		}
		sidebar.Visible = func(viewport tui.LayoutViewport) bool { return visible(viewport.Width) }
		rootChildren = append(rootChildren, sidebar)
	}
	return ChatViewport{
		Transcript: transcript,
		Root:       tui.NewHStack(rootChildren, tui.StackOptions{}),
	}
}
