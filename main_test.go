package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPrettyMessageShortensAnchorsAndURLs(t *testing.T) {
	message := `voir <a href="https://example.org/a/very/long/path">le site</a> ou https://example.net/a/long/path.`
	want := "voir https://example.org ou https://example.net."
	if got := prettyMessage(message); got != want {
		t.Fatalf("prettyMessage() = %q, want %q", got, want)
	}
}

func TestPrettyMessageKeepsNonHTTPURLs(t *testing.T) {
	message := "git@github.com:devnewton/gb3"
	if got := prettyMessage(message); got != message {
		t.Fatalf("prettyMessage() = %q, want %q", got, message)
	}
}

func TestStyledMessageColorsURLsAndClocks(t *testing.T) {
	got := styledMessage("voir https://example.org/path à 12:34:56")
	if !strings.Contains(got, "https://example.org") || !strings.Contains(got, "12:34:56") {
		t.Fatalf("styledMessage() = %q, missing shortened content", got)
	}
}

func TestStyledMessageRendersSupportedHTMLTags(t *testing.T) {
	got := styledMessage("<b>bold <i>italic</i></b> <u>underlined</u> <s>gone</s>")
	for _, text := range []string{"bold", "italic", "underlined", "gone"} {
		if !strings.Contains(got, text) {
			t.Fatalf("styledMessage() = %q, missing %q", got, text)
		}
	}
	if strings.Contains(got, "<b>") || strings.Contains(got, "<i>") || strings.Contains(got, "<u>") || strings.Contains(got, "<s>") {
		t.Fatalf("styledMessage() = %q, contains raw HTML tags", got)
	}
}

func TestInlineFormatMapsToLipgloss(t *testing.T) {
	style := (inlineFormat{bold: true, italic: true, underline: true, strike: true}).style()
	if !style.GetBold() || !style.GetItalic() || !style.GetUnderline() || !style.GetStrikethrough() {
		t.Fatal("inline format did not enable every requested terminal style")
	}
}

func TestStyledMessageClosesUnterminatedFormattingTags(t *testing.T) {
	tests := []struct {
		name    string
		message string
		text    string
	}{
		{"bold", "<b>bold without end", "bold without end"},
		{"italic", "<i>italic without end", "italic without end"},
		{"underline", "<u>underline without end", "underline without end"},
		{"strike", "<s>strike without end", "strike without end"},
		{"nested", "<b>bold <i>and italic <u>and underlined", "bold and italic and underlined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := styledMessage(test.message)
			if !strings.Contains(got, test.text) {
				t.Fatalf("styledMessage() = %q, want text %q", got, test.text)
			}
			if strings.Contains(got, "<") || strings.Contains(got, ">") {
				t.Fatalf("styledMessage() = %q, contains a raw unterminated tag", got)
			}
		})
	}
}

func TestConversationGraphConnectsRepliesToTheirRoot(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"},
		{ID: 2, Time: "20260926120100", Tribune: "moules", Message: "12:00:00 reply"},
		{ID: 3, Time: "20260926120200", Tribune: "moules", Message: "12:01:00 reply again"},
	}
	graph := buildConversationGraph(posts)
	root := postKey(posts[0])
	if got := graph.root(postKey(posts[2])); got != root {
		t.Fatalf("root() = %q, want %q", got, root)
	}
	rootMarker, _, ok := graph.decoration(root)
	if !ok || rootMarker != "┏" {
		t.Fatalf("root decoration = %q, %t; want ┏, true", rootMarker, ok)
	}
	leafMarker, _, ok := graph.decoration(postKey(posts[2]))
	if !ok || leafMarker != "┗" {
		t.Fatalf("leaf decoration = %q, %t; want ┗, true", leafMarker, ok)
	}
}

func TestConversationLanesSeparateOverlapAndReuseFinishedLane(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "thread A"},
		{ID: 2, Time: "20260926120100", Tribune: "moules", Message: "thread B"},
		{ID: 3, Time: "20260926120200", Tribune: "moules", Message: "12:00:00 reply A"},
		{ID: 4, Time: "20260926120300", Tribune: "moules", Message: "12:02:00 nested reply A"},
		{ID: 5, Time: "20260926120400", Tribune: "moules", Message: "12:01:00 reply B"},
		{ID: 6, Time: "20260926120500", Tribune: "moules", Message: "thread C"},
		{ID: 7, Time: "20260926120600", Tribune: "moules", Message: "12:05:00 reply C"},
	}
	graph := buildConversationGraph(posts)
	lanes := buildConversationLanes(posts, graph)
	rootA := postKey(posts[0])
	rootB := postKey(posts[1])
	rootC := postKey(posts[5])

	if lanes.count != 2 {
		t.Fatalf("lane count = %d, want 2", lanes.count)
	}
	if lanes.byRoot[rootA].lane == lanes.byRoot[rootB].lane {
		t.Fatal("overlapping conversations A and B share a lane")
	}
	if lanes.byRoot[rootA].lane != lanes.byRoot[rootC].lane {
		t.Fatal("conversation C did not reuse A's finished lane")
	}
	if got := lanes.rootByKey[postKey(posts[3])]; got != rootA {
		t.Fatalf("nested reply root = %q, want %q", got, rootA)
	}
}

func TestConversationLanesDescribeSingleVisibleMember(t *testing.T) {
	root := Post{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"}
	reply := Post{ID: 2, Time: "20260926120100", Tribune: "moules", Message: "12:00:00 reply"}
	graph := buildConversationGraph([]Post{root, reply})

	rootOnly := buildConversationLanes([]Post{root}, graph)
	if got := rootOnly.byRoot[postKey(root)].singleMarker; got != "╷" {
		t.Fatalf("single root marker = %q, want ╷", got)
	}

	replyOnly := buildConversationLanes([]Post{reply}, graph)
	if got := replyOnly.byRoot[postKey(root)].singleMarker; got != "╵" {
		t.Fatalf("single reply marker = %q, want ╵", got)
	}
}

func TestShortBrowserUserAgent(t *testing.T) {
	tests := []struct {
		userAgent string
		want      string
	}{
		{
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			"Chrome 140 · Linux",
		},
		{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0",
			"Edge 140 · Windows",
		},
		{
			"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 Version/18.6 Mobile/15E148 Safari/604.1",
			"Safari 18 · iOS",
		},
		{
			"Mozilla/5.0 (Android 16; Mobile; rv:143.0) Gecko/143.0 Firefox/143.0",
			"Firefox 143 · Android",
		},
	}
	for _, test := range tests {
		if got := shortBrowserUserAgent(test.userAgent); got != test.want {
			t.Errorf("shortBrowserUserAgent() = %q, want %q", got, test.want)
		}
	}
}

func TestShortBrowserUserAgentKeepsCustomInfo(t *testing.T) {
	const info = "coincoin-from-the-future"
	if got := shortBrowserUserAgent(info); got != info {
		t.Fatalf("shortBrowserUserAgent() = %q, want %q", got, info)
	}
}

func TestFindPostByClock(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"},
		{ID: 2, Time: "20260926120100", Tribune: "moules", Message: "reply"},
		{ID: 3, Time: "20260926120000", Tribune: "dlfp", Message: "same clock, other tribune"},
	}
	target, ok := findPostByClock(posts, "moules", "120000")
	if !ok || target.ID != 1 {
		t.Fatalf("findPostByClock() = %v, %t; want post 1, true", target, ok)
	}
	if _, ok := findPostByClock(posts, "moules", "123456"); ok {
		t.Fatal("findPostByClock() should not match a missing clock")
	}
}

func TestFindPostByClockPrefersMostRecent(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "toto"},
		{ID: 5, Time: "20260926120000", Tribune: "moules", Message: "michel"},
	}
	target, ok := findPostByClock(posts, "moules", "120000")
	if !ok || target.ID != 5 {
		t.Fatalf("findPostByClock() = %v, %t; want post 5, true", target, ok)
	}
}

func TestConversationGraphMostRecentMatchFormsNewThread(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "toto root"},
		{ID: 2, Time: "20260926120000", Tribune: "moules", Message: "michel root"},
		{ID: 3, Time: "20260926120500", Tribune: "moules", Message: "12:00:00 answer"},
	}
	graph := buildConversationGraph(posts)
	lanes := buildConversationLanes(posts, graph)

	toto := postKey(posts[0])
	michel := postKey(posts[1])
	answer := postKey(posts[2])

	if got := graph.root(answer); got != michel {
		t.Fatalf("root(answer) = %q, want %q", got, michel)
	}
	if contains(graph.children[toto], answer) {
		t.Fatal("answer should not be a child of toto")
	}
	if !contains(graph.children[michel], answer) {
		t.Fatal("answer should be a child of michel")
	}
	if lanes.rootByKey[answer] != michel {
		t.Fatalf("answer lane root = %q, want %q", lanes.rootByKey[answer], michel)
	}
	if _, ok := lanes.byRoot[toto]; ok {
		t.Fatal("toto should not own a conversation lane")
	}
}

func TestPostClockLinks(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"},
		{ID: 2, Time: "20260926120100", Tribune: "moules", Message: "12:00:00 reply"},
		{ID: 3, Time: "20260926120000", Tribune: "dlfp", Message: "dlfp root"},
		{ID: 4, Time: "20260926120200", Tribune: "moules", Message: "12:00:00@dlfp cross"},
	}

	links := postClockLinks(posts, posts[1])
	if len(links) != 1 || !links[0].found || links[0].target.ID != 1 {
		t.Fatalf("postClockLinks() = %+v; want one resolved link to post 1", links)
	}

	cross := postClockLinks(posts, posts[3])
	if len(cross) != 1 || !cross[0].found || cross[0].target.ID != 3 || cross[0].target.Tribune != "dlfp" {
		t.Fatalf("postClockLinks() cross-tribune = %+v; want link to dlfp post 3", cross)
	}
}

func TestClockForPost(t *testing.T) {
	post := Post{ID: 1, Time: "20260926150405", Tribune: "moules", Message: "hello"}
	if got := clockForPost(post); got != "15:04:05" {
		t.Fatalf("clockForPost() = %q, want %q", got, "15:04:05")
	}
}

func TestWrapWordsHangingIndent(t *testing.T) {
	lines := wrapWords("aa bb cc dd ee ff", 10, 20)
	want := []string{"aa bb cc", "dd ee ff"}
	if len(lines) != len(want) {
		t.Fatalf("wrapWords() = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("wrapWords()[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestHiddenPostsPropagatesThroughReplies(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Login: "troll", Message: "root"},
		{ID: 2, Time: "20260926120001", Tribune: "moules", Message: "12:00:00 reply"},
		{ID: 3, Time: "20260926120002", Tribune: "moules", Message: "12:00:01 nested reply"},
		{ID: 4, Time: "20260926120003", Tribune: "moules", Message: "unrelated"},
	}
	index := newPostIndex(posts)
	m := model{tribunes: []string{"moules"}, blocked: map[string]bool{"troll": true}}
	hidden := m.hiddenPosts(index)
	for _, post := range posts[:3] {
		if !hidden[postKey(post)] {
			t.Fatalf("post %d should be hidden", post.ID)
		}
	}
	if hidden[postKey(posts[3])] {
		t.Fatal("unrelated post should remain visible")
	}

	visible := m.visiblePostsFrom(index, hidden)
	if len(visible) != 1 || visible[0].ID != 4 {
		t.Fatalf("visiblePostsFrom() = %+v, want only post 4", visible)
	}

	m.gray = true
	visible = m.visiblePostsFrom(index, hidden)
	if len(visible) != len(posts) {
		t.Fatalf("gray visible post count = %d, want %d", len(visible), len(posts))
	}
	if visible[0].Message != "[message hidden]" {
		t.Fatalf("gray hidden message = %q, want masked message", visible[0].Message)
	}
}

func TestSelectedIndexFollowsClock(t *testing.T) {
	posts := []Post{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"},
		{ID: 2, Time: "20260926120005", Tribune: "moules", Message: "12:00:00 reply"},
		{ID: 3, Time: "20260926120010", Tribune: "moules", Message: "unrelated"},
	}
	index := newPostIndex(posts)
	m := model{
		tribunes:    []string{"moules"},
		selectedKey: postKey(posts[1]),
		clockIndex:  0,
	}
	if got := m.selectedIndexFrom(index, posts); got != 0 {
		t.Fatalf("selectedIndexFrom() with clock = %d, want 0", got)
	}
	m.clockIndex = -1
	if got := m.selectedIndexFrom(index, posts); got != 1 {
		t.Fatalf("selectedIndexFrom() without clock = %d, want 1", got)
	}
}

func TestWaitForStreamBatchesBufferedPosts(t *testing.T) {
	stream := &postStream{
		posts:  make(chan Post, 4),
		errors: make(chan error, 1),
	}
	stream.posts <- Post{ID: 1}
	stream.posts <- Post{ID: 2}
	stream.posts <- Post{ID: 3}

	msg := (model{stream: stream}).waitForStream()()
	batch, ok := msg.(postsMessage)
	if !ok {
		t.Fatalf("waitForStream() returned %T, want postsMessage", msg)
	}
	if len(batch) != 3 {
		t.Fatalf("batch length = %d, want 3", len(batch))
	}
}

func TestUpdateBuildsCachedView(t *testing.T) {
	stream := &postStream{
		posts:  make(chan Post, 4),
		errors: make(chan error, 1),
	}
	m := model{
		tribunes: []string{"moules"},
		posts:    make(map[string]Post),
		stream:   stream,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	updated, _ = m.Update(postsMessage{
		{ID: 1, Time: "20260926120000", Tribune: "moules", Message: "root"},
		{ID: 2, Time: "20260926120005", Tribune: "moules", Message: "12:00:00 reply"},
	})
	m = updated.(model)

	if len(m.view.visible) != 2 {
		t.Fatalf("cached visible post count = %d, want 2", len(m.view.visible))
	}
	if m.view.totalLines == 0 || len(m.view.lines) == 0 {
		t.Fatal("cached view did not render any lines")
	}
}
